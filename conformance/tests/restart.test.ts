import assert from 'node:assert/strict';
import { after, before, describe, it } from 'node:test';
import { CLIENT, instanceKey, pieces, rid, type Json, checked } from '../support/client.js';
import { joinSession, linkItems, needsHosted, needsRestart, opText, startHosted, startLocal } from '../support/flows.js';
import { startEnv, type Env } from '../support/target.js';

const KINDS = ['participants', 'locks', 'live', 'notices', 'saving'] as const;

describe('restart: retries within the 10-minute window, running numbers, open chains, versions', () => {
  let env: Env;
  before(async () => {
    env = await startEnv();
  });
  after(async () => env.close());

  for (const hard of [false, true]) {
    it(`after a ${hard ? 'hard (SIGKILL)' : 'clean'} restart every retry gets the answer from before and nothing happens twice`, async (t) => {
      if (!needsRestart(env, t)) return;
      const host = await startLocal(env);
      const { m: ben } = await joinSession(env.ben, host.sessionId);

      const ops = [host.op(opText(1)), host.op(opText(2))];
      const sent = await host.sendOk(ops);
      const lockSeq = host.lockSeq++;
      const locked = await env.anna.ok('locks/set', { ...host.ids(), lockSeq, elements: ['kiste-1', 'kiste-2'] });
      const inviteBody = { requestId: rid(), ...host.ids() };
      const invited = await env.anna.ok('invite/create', inviteBody);
      await ben.lock(['fass-9']);
      const releaseBody = { requestId: rid(), ...host.ids(), target: ben.participantId };
      await env.anna.ok('locks/release', releaseBody);
      const createBody = {
        requestId: rid(),
        client: CLIENT,
        projectName: 'Zweite',
        map: 'chernarus',
        kind: 'local',
        instanceKey: instanceKey(),
        snapshotBytes: 2,
        snapshotParts: 1,
      };
      const created = await env.anna.ok('session/create', createBody);
      const joinBody = { requestId: rid(), client: CLIENT, sessionId: host.sessionId, instanceKey: instanceKey() };
      const joined = await env.ben.ok('session/join', joinBody);

      const opId = host.newOpId();
      const first = host.clientSeq;
      host.clientSeq += 4;
      const links = Array.from({ length: 4 }, (_, i) => linkItems(first + i, opId, i, 4, pieces(`{"v":1,"link":${i},"src":"Größe"}`)));
      assert.deepEqual((await host.sendOk(links[0])).results, []);
      assert.deepEqual((await host.sendOk(links[1])).results, []);

      await host.drain();
      await ben.drain();
      const seen = { ...ben.versions };

      await env.restart!({ hard });

      assert.deepEqual((await host.sendOk(ops)).results, sent.results, 'ops/send: the same seqs');
      assert.deepEqual(await env.anna.ok('locks/set', { ...host.ids(), lockSeq, elements: ['other'] }), locked, 'locks/set: the same answer');
      assert.equal((await env.anna.ok('invite/create', inviteBody)).code, invited.code, 'invite/create: the same code');
      await env.anna.ok('locks/release', releaseBody);
      const again = await env.anna.ok('session/create', createBody);
      assert.equal(again.sessionId, created.sessionId);
      assert.equal(again.participantId, created.participantId);
      assert.deepEqual(await env.ben.ok('session/join', joinBody), joined, 'session/join: the same participant');

      assert.deepEqual((await host.sendOk(links[1])).results, [], 'a repeated open link still changes nothing');
      await host.sendOk(links[2]);
      const done = await host.sendOk(links[3]);
      assert.deepEqual(done.results.map((r: Json) => r.clientSeq), [first, first + 1, first + 2, first + 3]);

      const res = await ben.poll({ waitMs: 0 });
      assert.equal(res.ok, true);
      for (const k of KINDS) {
        assert.ok(res[k], `${k} comes with the first poll after the restart`);
        assert.ok(res[k].version > seen[k], `${k}: version ${res[k].version} is higher than ${seen[k]} from before`);
      }
      await ben.drain();
      const chain = [...ben.log.values()].filter((e) => e.opId === opId);
      assert.deepEqual(chain.map((e) => e.chain!.index), [0, 1, 2, 3], 'the chain stands once and whole in the log');
      assert.equal([...ben.log.values()].filter((e) => e.opId === ops[0].opId).length, 1, 'no operation twice');
      await env.anna.ok('session/end', host.ids());
      await env.anna.ok('session/end', { sessionId: created.sessionId, participantId: created.participantId });
    });
  }

  it('project/save answered, then a hard restart: the retry gets the same answer and the store has one new revision', async (t) => {
    if (!needsRestart(env, t) || !needsHosted(env, t)) return;
    const ref = await env.hosted!.ref();
    const anna = await startHosted(env, env.anna, ref);
    await anna.sendOk([anna.op(opText(1))]);
    await anna.drain();
    const body = {
      requestId: rid(),
      ...anna.ids(),
      seq: anna.cursor,
      files: checked([{ name: 'manifest', pieces: ['{"v":1}'] }, { name: 'f-a', pieces: ['ä'] }]),
      deleted: [],
    };
    const saved = await env.anna.ok('project/save', body);
    const rev = await env.hosted!.revision(ref);
    assert.equal(rev, saved.hostedRevision);
    await env.restart!({ hard: true });
    assert.deepEqual(await env.anna.ok('project/save', body), saved);
    assert.equal(await env.hosted!.revision(ref), rev, 'no second revision');
    const s = (await anna.poll({ waitMs: 0, fresh: true })).saving;
    assert.equal(s.savedSeq, anna.cursor);
    assert.equal(s.unsavedCount, 0);
    await env.anna.ok('session/end', anna.ids());
  });

  it('after a restart a client whose locks are gone sets them again with a new lockSeq', async (t) => {
    if (!needsRestart(env, t)) return;
    const host = await startLocal(env);
    const { m: ben } = await joinSession(env.ben, host.sessionId);
    await host.lock(['a']);
    await env.restart!({ hard: true });
    const res = await ben.poll({ waitMs: 0 });
    assert.ok(res.locks, 'the first poll after the restart brings the locks');
    const back = await host.lock(['a']);
    assert.deepEqual(back.granted, ['a']);
    await env.anna.ok('session/end', host.ids());
  });
});
