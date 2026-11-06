import assert from 'node:assert/strict';
import { after, before, describe, it } from 'node:test';
import { CLIENT, instanceKey, Member, partsOf, pieces, rid, type Json } from '../support/client.js';
import { invite, joinSession, linkItems, needsClock, needsHosted, opText, startHosted, startLocal, uploadSnapshot } from '../support/flows.js';
import { startEnv, type Env } from '../support/target.js';

async function resume(m: Member): Promise<Json> {
  return m.user.ok('session/join', {
    requestId: rid(),
    client: CLIENT,
    sessionId: m.sessionId,
    instanceKey: m.key,
    resume: { participantId: m.participantId, after: m.cursor },
  });
}

describe('rules: live end, kept parts and links, leaving, creator gone, hosted checkpoints', () => {
  let env: Env;
  before(async () => {
    env = await startEnv();
  });
  after(async () => env.close());

  it('live/put with mode end is always accepted, also right after a state and for a gesture without one', async () => {
    const host = await startLocal(env);
    const liveId = `${host.key}-L1`;
    const v = await env.anna.ok('live/put', { ...host.ids(), liveId, n: 5, mode: 'values', values: [{ element: 'e', position: [1, 2, 3], ypr: [0, 0, 0], scale: 1 }] });
    assert.equal(v.accepted, true);
    const end = await env.anna.ok('live/put', { ...host.ids(), liveId, n: 6, mode: 'end', end: 'cancel' });
    assert.equal(end.accepted, true, 'no 180 ms gap for end');
    const other = await env.anna.ok('live/put', { ...host.ids(), liveId: `${host.key}-L2`, n: 1, mode: 'end', end: 'commit' });
    assert.equal(other.accepted, true);
    await env.anna.ok('session/end', host.ids());
  });

  it('after session/leave every request of that participant is not_participant timeout; a repeated leave is ok', async () => {
    const host = await startLocal(env);
    const { m: ben } = await joinSession(env.ben, host.sessionId);
    await ben.leave();
    for (const [ep, body] of [
      ['poll', { ...ben.ids(), after: ben.cursor, waitMs: 0 }],
      ['ops/send', { ...ben.ids(), ops: [ben.op(opText(1))] }],
      ['locks/set', { ...ben.ids(), lockSeq: 99, elements: [] }],
      ['live/put', { ...ben.ids(), liveId: 'l', n: 1, mode: 'end', end: 'cancel' }],
      ['invite/create', { requestId: rid(), ...ben.ids() }],
      ['session/join', { requestId: rid(), client: CLIENT, sessionId: host.sessionId, instanceKey: ben.key, resume: { participantId: ben.participantId, after: ben.cursor } }],
    ] as [string, Json][]) {
      const e = await env.ben.err(ep, body, 'not_participant');
      assert.equal(e.reason, 'timeout', ep);
    }
    await ben.leave();
    await env.anna.err('participant/remove', { ...host.ids(), target: 'p999' }, 'bad_request');
    await env.anna.ok('session/end', host.ids());
  });

  it('parts of a large operation and links of a chain are kept over 30 s without contact', async (t) => {
    if (!needsClock(env, t)) return;
    const host = await startLocal(env);
    const { m: ben } = await joinSession(env.ben, host.sessionId);
    const parts = partsOf(pieces('ä'.repeat(800_000)));
    assert.equal(parts.length, 2);
    const cs = ben.clientSeq++;
    const opId = ben.newOpId();
    await ben.sendOk([{ clientSeq: cs, opId, part: 0, parts: 2, pieces: parts[0] }]);
    const chainId = ben.newOpId();
    await env.advance!(35_000);
    await host.poll({ waitMs: 0 });
    assert.equal((await resume(ben)).mode, 'resume');
    const done = await ben.sendOk([{ clientSeq: cs, opId, part: 1, parts: 2, pieces: parts[1] }]);
    assert.equal(done.results.length, 1, 'the first part was kept');

    const c0 = ben.clientSeq++;
    ben.clientSeq++;
    await ben.sendOk(linkItems(c0, chainId, 0, 2, pieces('{"link":0}')));
    await env.advance!(35_000);
    await host.poll({ waitMs: 0 });
    assert.equal((await resume(ben)).mode, 'resume');
    const linked = await ben.sendOk(linkItems(c0 + 1, chainId, 1, 2, pieces('{"link":1}')));
    assert.deepEqual(linked.results.map((r: Json) => r.clientSeq), [c0, c0 + 1], 'the first link was kept');
    await host.drain();
    assert.equal(host.text(done.results[0].seq), parts.flat().join(''));
    await env.anna.ok('session/end', host.ids());
  });

  it('who leaves with an open chain leaves nothing of it in the log', async () => {
    const host = await startLocal(env);
    const { m: ben } = await joinSession(env.ben, host.sessionId);
    const chainId = ben.newOpId();
    await ben.sendOk(linkItems(ben.clientSeq++, chainId, 0, 3, pieces('{"link":0}')));
    await ben.sendOk(linkItems(ben.clientSeq++, chainId, 1, 3, pieces('{"link":1}')));
    await ben.leave();
    await host.sendOk([host.op(opText(1))]);
    await host.drain();
    assert.ok(![...host.log.values()].some((e) => e.opId === chainId));
    await env.anna.ok('session/end', host.ids());
  });

  it('the creator without contact for 300 s before snapshot 0 is complete: the session is gone (session_unknown)', async (t) => {
    if (!needsClock(env, t)) return;
    const create = async (body: Json) =>
      env.anna.ok('session/create', {
        requestId: rid(),
        client: CLIENT,
        projectName: 'Halb',
        map: 'chernarus',
        instanceKey: instanceKey(),
        snapshotBytes: 2000,
        snapshotParts: 2,
        ...body,
      });
    const local = await create({ kind: 'local' });
    await env.anna.ok('snapshot/put', { sessionId: local.sessionId, participantId: local.participantId, seq: 0, part: 0, parts: 2, pieces: ['{"v":1'] });
    let hosted: Json | undefined;
    let ref: Json | undefined;
    if (env.info.capabilities.includes('hosted') && env.hosted) {
      ref = await env.hosted.ref();
      hosted = await create({ kind: 'hosted', hostedRef: ref, hostedRevision: await env.hosted.revision(ref) });
    }
    await env.advance!(301_000);
    await env.ben.err('session/join', { requestId: rid(), client: CLIENT, sessionId: local.sessionId, instanceKey: instanceKey() }, 'session_unknown');
    if (hosted && ref) {
      await env.ben.err('session/join', { requestId: rid(), client: CLIENT, sessionId: hosted.sessionId, instanceKey: instanceKey() }, 'session_unknown');
      const again = await startHosted(env, env.anna, ref);
      assert.notEqual(again.sessionId, hosted.sessionId, 'a new session can start for the project');
      await env.anna.ok('session/end', again.ids());
    }
  });

  it('hosted: the checkpoint is asked from the participant with the most recent contact', async (t) => {
    if (!needsClock(env, t) || !needsHosted(env, t)) return;
    const ref = await env.hosted!.ref();
    const anna = await startHosted(env, env.anna, ref);
    const { m: ben } = await joinSession(env.ben, anna.sessionId);
    await ben.sendOk(Array.from({ length: 30 }, (_, i) => ben.op(opText(i))));
    await env.advance!(11_000);
    await ben.drain();
    await ben.sendOk(Array.from({ length: 20 }, (_, i) => ben.op(opText(100 + i))));
    const res = await ben.drain();
    assert.ok(res.checkpointWanted === true || ben.last.checkpointWanted === true, 'Ben, the one with contact, is asked');
    const a = await anna.drain();
    assert.notEqual(a.checkpointWanted, true, 'Anna, without contact for 11 s, is not asked');
    await env.anna.err('snapshot/put', { ...anna.ids(), seq: anna.cursor, part: 0, parts: 1, pieces: ['{}'] }, 'not_allowed');
    await uploadSnapshot(ben, ben.cursor, '{"v":1,"checkpoint":"ben"}');
    let late;
    if (env.hosted!.joinOnly) {
      late = await joinSession(env.dana, anna.sessionId);
    } else {
      await env.carla.ok('invite/redeem', { requestId: rid(), code: await invite(ben) });
      late = await joinSession(env.carla, anna.sessionId);
    }
    assert.equal(late.snapshot, '{"v":1,"checkpoint":"ben"}');
    await env.anna.ok('session/end', anna.ids());
  });
});
