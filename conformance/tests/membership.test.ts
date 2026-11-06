import assert from 'node:assert/strict';
import { after, before, describe, it } from 'node:test';
import { CLIENT, instanceKey, rid, sleep, type Json } from '../support/client.js';
import { invite, joinSession, needsClock, opText, redeemAndJoin, startLocal, uploadSnapshot, whoAmI } from '../support/flows.js';
import { startEnv, type Env } from '../support/target.js';

const ALPHABET = /^[ABCDEFGHJKLMNPQRSTUVWXYZ23456789]{8}$/;

describe('invitations, removing, leaving, ending, coming back', () => {
  let env: Env;
  before(async () => {
    env = await startEnv();
  });
  after(async () => env.close());

  it('codes: 8 characters from the alphabet, distinct, one person each; case, spaces and hyphens do not count', async () => {
    const host = await startLocal(env);
    const codes = [await invite(host), await invite(host), await invite(host)];
    for (const c of codes) assert.match(c, ALPHABET);
    assert.equal(new Set(codes).size, 3);
    const typed = `${codes[0].slice(0, 4).toLowerCase()} - ${codes[0].slice(4).toLowerCase()}`;
    const r = await env.carla.ok('invite/redeem', { requestId: rid(), code: typed });
    assert.equal(r.sessionId, host.sessionId);
    assert.equal(r.kind, 'local');
    assert.equal(r.projectName, 'North Base');
    assert.equal(r.map, 'chernarus');
    assert.equal(r.host.name, 'Anna');
    const again = await env.carla.ok('invite/redeem', { requestId: rid(), code: codes[0] });
    assert.equal(again.sessionId, host.sessionId, 'the same user redeeming again gets the same answer');
    await env.dana.err('invite/redeem', { requestId: rid(), code: codes[0] }, 'invite_used');
    const list = await env.carla.ok('sessions/list');
    assert.equal(list.sessions.find((s: Json) => s.sessionId === host.sessionId).access, 'invited');
    await env.anna.ok('session/end', host.ids());
  });

  it('the same invite/create twice gives one code', async () => {
    const host = await startLocal(env);
    const requestId = rid();
    const a = await env.anna.ok('invite/create', { requestId, ...host.ids() });
    const b = await env.anna.ok('invite/create', { requestId, ...host.ids() });
    assert.equal(a.code, b.code);
    assert.ok(a.expiresInSeconds > 0 && a.expiresInSeconds <= 86_400);
    await env.anna.ok('session/end', host.ids());
  });

  it('who came by code may not invite; who leaves comes back without a new code', async () => {
    const host = await startLocal(env);
    const carla = await redeemAndJoin(env.carla, await invite(host), host.sessionId);
    const joined = await carla.poll({ waitMs: 0, fresh: true });
    assert.equal(joined.participants.list.find((p: Json) => p.participantId === carla.participantId).canInvite, false);
    await env.carla.err('invite/create', { requestId: rid(), ...carla.ids() }, 'not_allowed');
    await carla.leave();
    const back = await joinSession(env.carla, host.sessionId);
    assert.notEqual(back.m.participantId, carla.participantId, 'a new participantId without resume');
    await env.anna.ok('session/end', host.ids());
  });

  it('a code for an ended session is session_ended; after 24 hours invite_expired', async (t) => {
    const host = await startLocal(env);
    const unused = await invite(host);
    await env.anna.ok('session/end', host.ids());
    const e = await env.carla.err('invite/redeem', { requestId: rid(), code: unused }, 'session_ended');
    assert.equal(e.reason, 'host_ended');
    if (!needsClock(env, t)) return;
    const running = await startLocal(env);
    const code = await invite(running);
    for (let i = 0; i < 361; i++) {
      await env.advance!(240_000);
      await running.poll({ waitMs: 0 });
    }
    await env.carla.err('invite/redeem', { requestId: rid(), code }, 'invite_expired');
    await env.anna.ok('session/end', running.ids());
  });

  it('11 wrong codes in a minute: the eleventh is throttled', async (t) => {
    const user = await env.throwaway();
    if (!user) return t.skip('no throwaway user for this target');
    for (let i = 0; i < 10; i++) await user.err('invite/redeem', { requestId: rid(), code: 'ZZZZ2222' }, 'invite_invalid');
    const e = await user.err('invite/redeem', { requestId: rid(), code: 'ZZZZ2223' }, 'throttled');
    assert.ok(e.retryAfterMs > 0);
  });

  it('invite.user: the session shows up in that user’s list as invited, without redeeming', async (t) => {
    if (!env.info.capabilities.includes('invite.user')) return t.skip('no invite.user');
    const carlaId = await whoAmI(env, env.carla);
    const host = await startLocal(env);
    const res = await env.anna.ok('invite/create', { requestId: rid(), ...host.ids(), forUserId: carlaId });
    assert.equal(res.forUserId, carlaId);
    const list = await env.carla.ok('sessions/list');
    assert.equal(list.sessions.find((s: Json) => s.sessionId === host.sessionId)?.access, 'invited');
    const { m } = await joinSession(env.carla, host.sessionId);
    assert.ok(m.participantId);
    await env.anna.ok('session/end', host.ids());
  });

  it('remove: the target’s waiting poll ends within 1 s, locks freed, old code used up, others get participant_removed with the list', async () => {
    const host = await startLocal(env);
    const { m: ben } = await joinSession(env.ben, host.sessionId);
    const code = await invite(host);
    const carla = await redeemAndJoin(env.carla, code, host.sessionId);
    await carla.lock(['crate-7']);
    await carla.drain();
    await host.drain();
    await ben.drain();
    await env.ben.err('participant/remove', { ...ben.ids(), target: carla.participantId }, 'not_allowed');
    await env.anna.err('participant/remove', { ...host.ids(), target: host.participantId }, 'bad_request');
    await env.anna.err('participant/remove', { ...host.ids(), target: 'p999' }, 'bad_request');

    const waiting = carla.poll({ waitMs: 8000 });
    await sleep(200);
    const t0 = Date.now();
    await env.anna.ok('participant/remove', { ...host.ids(), target: carla.participantId });
    const res = await waiting;
    assert.ok(Date.now() - t0 < 1000);
    assert.equal(res.error.code, 'not_participant');
    assert.equal(res.error.reason, 'removed');
    assert.equal(res.error.by, 'Anna');
    await env.anna.ok('participant/remove', { ...host.ids(), target: carla.participantId });
    await env.carla.err('ops/send', { ...carla.ids(), ops: [carla.op(opText(1))] }, 'not_participant');

    const b = await ben.poll({ waitMs: 0 });
    const notice = b.notices.list.find((n: Json) => n.kind === 'participant_removed');
    assert.deepEqual(notice, { kind: 'participant_removed', participantId: carla.participantId, name: 'Carla', by: 'Anna' });
    assert.ok(!b.participants.list.some((p: Json) => p.participantId === carla.participantId), 'same answer as the list without her');
    assert.ok(!(b.locks?.holders ?? []).some((h: Json) => h.participantId === carla.participantId));

    await env.carla.err('invite/redeem', { requestId: rid(), code }, 'invite_used');
    await env.carla.err('session/join', { requestId: rid(), client: CLIENT, sessionId: host.sessionId, instanceKey: instanceKey() }, 'not_allowed');
    const e = await env.carla.err(
      'session/join',
      { requestId: rid(), client: CLIENT, sessionId: host.sessionId, instanceKey: carla.key, resume: { participantId: carla.participantId, after: 0 } },
      'not_participant',
    );
    assert.equal(e.reason, 'removed');
    const again = await redeemAndJoin(env.carla, await invite(host), host.sessionId);
    assert.ok(again.participantId);
    await env.anna.err('participant/remove', { ...host.ids(), target: host.participantId }, 'bad_request');
    await env.anna.ok('session/end', host.ids());
  });

  it('nobody removes the host of a local session', async () => {
    const host = await startLocal(env);
    const { m: ben } = await joinSession(env.ben, host.sessionId);
    await env.ben.err('participant/remove', { ...ben.ids(), target: host.participantId }, 'not_allowed');
    await env.anna.ok('session/end', host.ids());
  });

  it('host leaves: the others’ polls end within 1 s with session_ended host_left; leave and end repeat as ok', async () => {
    const host = await startLocal(env);
    const { m: ben } = await joinSession(env.ben, host.sessionId);
    await env.ben.err('session/end', ben.ids(), 'not_allowed');
    const waiting = ben.poll({ waitMs: 8000 });
    await sleep(200);
    const t0 = Date.now();
    await host.leave();
    const res = await waiting;
    assert.ok(Date.now() - t0 < 1000);
    assert.equal(res.error.code, 'session_ended');
    assert.equal(res.error.reason, 'host_left');
    await host.leave();
    await env.anna.ok('session/end', host.ids());
    await env.ben.err('ops/send', { ...ben.ids(), ops: [ben.op(opText(1))] }, 'session_ended');
  });

  it('the end stays session_ended for 10 minutes, then session_unknown', async (t) => {
    if (!needsClock(env, t)) return;
    const host = await startLocal(env);
    const { m: ben } = await joinSession(env.ben, host.sessionId);
    await env.anna.ok('session/end', host.ids());
    await env.advance!(9 * 60_000);
    const e = await env.ben.err('poll', { ...ben.ids(), after: ben.cursor, waitMs: 0 }, 'session_ended');
    assert.equal(e.reason, 'host_ended');
    await env.advance!(2 * 60_000);
    await env.ben.err('poll', { ...ben.ids(), after: ben.cursor, waitMs: 0 }, 'session_unknown');
  });

  it('host 300 s without contact: host_gone', async (t) => {
    if (!needsClock(env, t)) return;
    const host = await startLocal(env);
    const { m: ben } = await joinSession(env.ben, host.sessionId);
    await env.advance!(150_000);
    assert.equal((await ben.poll({ waitMs: 0 })).ok, true);
    await env.advance!(151_000);
    const e = await env.ben.err('poll', { ...ben.ids(), after: ben.cursor, waitMs: 0 }, 'session_ended');
    assert.equal(e.reason, 'host_gone');
  });

  it('server ends the session: reason server', async (t) => {
    if (!env.endByServer) return t.skip('no test controls');
    const host = await startLocal(env);
    const { m: ben } = await joinSession(env.ben, host.sessionId);
    await env.endByServer(host.sessionId);
    const e = await env.ben.err('poll', { ...ben.ids(), after: ben.cursor, waitMs: 0 }, 'session_ended');
    assert.equal(e.reason, 'server');
  });

  it('back after 60 s: join with resume gives mode resume and poll brings every missed operation', async (t) => {
    if (!needsClock(env, t)) return;
    const host = await startLocal(env);
    const { m: ben, join } = await joinSession(env.ben, host.sessionId);
    await ben.sendOk([ben.op(opText(1))]);
    await ben.lock(['crate-1']);
    await env.advance!(60_000);
    await host.sendOk([host.op(opText(2)), host.op(opText(3))]);
    const res = await env.ben.ok('session/join', {
      requestId: rid(),
      client: CLIENT,
      sessionId: host.sessionId,
      instanceKey: ben.key,
      resume: { participantId: ben.participantId, after: ben.cursor },
    });
    assert.equal(res.mode, 'resume');
    assert.equal(res.participantId, ben.participantId);
    assert.equal(res.colour, join.colour);
    await ben.drain();
    const texts = [...ben.log.values()].map((e) => e.parts.flat().join(''));
    assert.deepEqual(texts, [opText(1), opText(2), opText(3)]);
    const next = await ben.sendOk([ben.op(opText(4))]);
    assert.equal(next.results[0].clientSeq, 2, 'clientSeq continues');
    const locks = await ben.poll({ waitMs: 0 });
    assert.ok(!(locks.locks?.holders ?? []).some((h: Json) => h.participantId === ben.participantId), 'locks were freed while away');
    await env.anna.ok('session/end', host.ids());
  });

  it('after 15 minutes without contact: not_participant timeout (or mode snapshot), and a fresh join works', async (t) => {
    if (!needsClock(env, t)) return;
    const host = await startLocal(env);
    const { m: ben } = await joinSession(env.ben, host.sessionId);
    for (let i = 0; i < 6; i++) {
      await env.advance!(150_000);
      await host.poll({ waitMs: 0 });
    }
    const res = await env.ben.call('session/join', {
      requestId: rid(),
      client: CLIENT,
      sessionId: host.sessionId,
      instanceKey: ben.key,
      resume: { participantId: ben.participantId, after: ben.cursor },
    });
    assert.ok((res.ok === false && res.error.code === 'not_participant' && res.error.reason === 'timeout') || res.mode === 'snapshot');
    const fresh = await joinSession(env.ben, host.sessionId);
    assert.notEqual(fresh.m.participantId, ben.participantId);
    await env.anna.ok('session/end', host.ids());
  });

  it('checkpoints: the host is asked, uploads, newcomers start from it, others may not upload; old log gives cursor_too_old', async (t) => {
    const host = await startLocal(env);
    const { m: ben } = await joinSession(env.ben, host.sessionId);
    let asked = false;
    for (let round = 0; round < 12 && !asked; round++) {
      await host.sendOk(Array.from({ length: 100 }, (_, i) => host.op(opText(round * 100 + i))));
      const res = await host.drain();
      asked = res.checkpointWanted === true || host.last.checkpointWanted === true;
    }
    if (!asked) return t.skip('server did not ask for a checkpoint within 1200 operations');
    assert.ok(host.cursor > 0);
    const seq = host.cursor;
    await env.ben.err('snapshot/put', { ...ben.ids(), seq, part: 0, parts: 1, pieces: ['{}'] }, 'not_allowed');
    await uploadSnapshot(host, seq, `{"v":1,"state":"after ${seq}"}`);
    const late = await joinSession(env.ben, host.sessionId);
    assert.equal(late.join.snapshot.seq, seq);
    assert.equal(late.snapshot, `{"v":1,"state":"after ${seq}"}`);
    await host.drain();
    await ben.drain();
    await late.m.drain();
    for (const m of [host, ben, late.m]) await m.poll({ waitMs: 0 });
    if (env.advance) {
      await env.advance(0);
      await sleep(50);
      const old = await env.ben.call('poll', { ...late.m.ids(), after: 0, waitMs: 0 });
      assert.equal(old.error?.code, 'cursor_too_old');
    }
    await env.anna.ok('session/end', host.ids());
  });
});
