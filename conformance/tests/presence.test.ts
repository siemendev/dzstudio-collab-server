import assert from 'node:assert/strict';
import { after, before, describe, it } from 'node:test';
import { rid, sleep, type Json } from '../support/client.js';
import { joinSession, needsClock, opText, startLocal } from '../support/flows.js';
import { startEnv, type Env } from '../support/target.js';

const LIVE_GAP = 200;
const holder = (res: Json, pid: string) => res.locks?.holders?.find((h: Json) => h.participantId === pid);
const presenceOf = (res: Json, pid: string) => res.presence.find((p: Json) => p.participantId === pid);

describe('long poll, presence, locks, live states', () => {
  let env: Env;
  before(async () => {
    env = await startEnv();
  });
  after(async () => env.close());

  it('without news poll answers empty after waitMs (5 s ± 0.5 s)', async () => {
    const host = await startLocal(env);
    const t0 = Date.now();
    const res = await host.poll({ waitMs: 5000 });
    const took = Date.now() - t0;
    assert.equal(res.ok, true);
    assert.deepEqual(res.ops, []);
    assert.ok(took >= 4500 && took <= 5500, `took ${took} ms`);
    await env.anna.ok('session/end', host.ids());
  });

  it("another's operation wakes a waiting poll within 200 ms; a change of place does not wake it; a save report does", async () => {
    const host = await startLocal(env);
    const { m: ben } = await joinSession(env.ben, host.sessionId);
    await host.drain();
    const waiting = host.poll({ waitMs: 8000 });
    await sleep(300);
    const sent = Date.now();
    await ben.sendOk([ben.op(opText(1))]);
    const res = await waiting;
    assert.ok(Date.now() - sent < 200 + 50, 'woken quickly');
    assert.equal(res.ops.length, 1);

    const quiet = host.poll({ waitMs: 1500 });
    await sleep(200);
    await ben.poll({ waitMs: 0, presence: { position: [9000, 5, 9000], idleSeconds: 3 } });
    const t0 = Date.now();
    const q = await quiet;
    assert.ok(Date.now() - t0 >= 1000, 'a change of place does not wake');
    assert.deepEqual(presenceOf(q, ben.participantId).position, [9000, 5, 9000]);

    const benWaits = ben.poll({ waitMs: 8000 });
    await sleep(200);
    const t1 = Date.now();
    await host.poll({ waitMs: 0, saving: { state: 'saved', unsavedCount: 0 } });
    const w = await benWaits;
    assert.ok(Date.now() - t1 < 250, 'a save wakes');
    assert.equal(w.saving.state, 'saved');
    assert.equal(w.saving.unsavedCount, 0);
    assert.equal(typeof w.saving.savedSecondsAgo, 'number');
    await env.anna.ok('session/end', host.ids());
  });

  it('presence: positions as float32 text, idleSeconds counted on, idle from 300 s', async () => {
    const host = await startLocal(env);
    const { m: ben } = await joinSession(env.ben, host.sessionId);
    await ben.poll({ waitMs: 0, presence: { position: [6512.000001, 210.4, 0.1], idleSeconds: 301 } });
    const raw = await env.t.raw('poll', {
      protocol: '1.0',
      token: env.anna.token,
      ...host.ids(),
      after: host.cursor,
      waitMs: 0,
      versions: host.versions,
      presence: { position: [1, 2, 3], idleSeconds: 0 },
    });
    const text = raw.raw.toString('utf8');
    assert.match(text, /"position":\[6512\.0,210\.4,0\.1\]/, 'shortest float32 text without exponent');
    const res = JSON.parse(text.slice(text.indexOf('\n') + 1));
    const p = presenceOf(res, ben.participantId);
    assert.equal(p.state, 'idle');
    assert.ok(p.idleSeconds >= 301);
    assert.equal(typeof p.lastSeenSeconds, 'number');
    await env.anna.ok('session/end', host.ids());
  });

  it('a participant who only long-polls is never interrupted; 30 s without contact makes him disconnected and frees his locks', async (t) => {
    if (!needsClock(env, t)) return;
    const host = await startLocal(env);
    const { m: ben } = await joinSession(env.ben, host.sessionId);
    await ben.lock(['el-1', 'el-2']);
    await ben.drain();
    const open = ben.poll({ waitMs: 3000 });
    await sleep(100);
    await env.advance!(40_000);
    let res = await host.poll({ waitMs: 0 });
    assert.equal(presenceOf(res, ben.participantId).state, 'active', 'an open poll counts as contact');
    await open;
    await env.advance!(12_000);
    res = await host.poll({ waitMs: 0, fresh: true });
    assert.equal(presenceOf(res, ben.participantId).state, 'interrupted');
    assert.ok(holder(res, ben.participantId), 'still holds locks while interrupted');
    await env.advance!(19_000);
    res = await host.poll({ waitMs: 0 });
    assert.equal(presenceOf(res, ben.participantId).state, 'disconnected');
    assert.equal(holder(res, ben.participantId), undefined, 'locks freed after 30 s');
    const back = await ben.poll({ waitMs: 0 });
    assert.equal(back.ok, true, 'he is still a participant');
    await env.anna.ok('session/end', host.ids());
  });

  it('locks: the first gets the element, the second sees it denied with the holder', async () => {
    const host = await startLocal(env);
    const { m: ben } = await joinSession(env.ben, host.sessionId);
    const a = await host.lock(['m1x0c7r2ab-12', 'k3f9a0x2mq-7']);
    assert.deepEqual(a.granted.sort(), ['k3f9a0x2mq-7', 'm1x0c7r2ab-12']);
    const b = await ben.lock(['m1x0c7r2ab-12', 'b-free']);
    assert.deepEqual(b.granted, ['b-free']);
    assert.deepEqual(b.denied, [{ element: 'm1x0c7r2ab-12', holder: host.participantId }]);
    assert.ok(b.version > a.version);
    const res = await ben.poll({ waitMs: 0 });
    assert.deepEqual(holder(res, host.participantId).elements.sort(), ['k3f9a0x2mq-7', 'm1x0c7r2ab-12']);
    assert.equal(holder(res, host.participantId).lapsed, false);
    const released = await host.lock(['k3f9a0x2mq-7']);
    assert.deepEqual(released.granted, ['k3f9a0x2mq-7']);
    const now = await ben.lock(['m1x0c7r2ab-12']);
    assert.deepEqual(now.granted, ['m1x0c7r2ab-12'], 'what is missing from the new set is freed');
    await env.anna.ok('session/end', host.ids());
  });

  it('an older lockSeq that arrives late changes nothing and gets the state of the last applied one', async () => {
    const host = await startLocal(env);
    await env.anna.ok('locks/set', { ...host.ids(), lockSeq: 5, elements: ['a', 'b'] });
    const late = await env.anna.ok('locks/set', { ...host.ids(), lockSeq: 4, elements: ['c'] });
    assert.equal(late.lockSeq, 5);
    assert.deepEqual(late.granted.sort(), ['a', 'b']);
    const res = await host.poll({ waitMs: 0 });
    assert.deepEqual(holder(res, host.participantId).elements.sort(), ['a', 'b']);
    await env.anna.ok('session/end', host.ids());
  });

  it('a lapsed holder (idle ≥ 300 s) loses the element to another and gets locks_taken', async () => {
    const host = await startLocal(env);
    const { m: ben } = await joinSession(env.ben, host.sessionId);
    await host.lock(['kiste-1']);
    await host.poll({ waitMs: 0, presence: { position: [0, 0, 0], idleSeconds: 300 } });
    const res = await ben.lock(['kiste-1']);
    assert.deepEqual(res.granted, ['kiste-1']);
    const a = await host.poll({ waitMs: 0, presence: { position: [0, 0, 0], idleSeconds: 301 } });
    const notice = a.notices.list.find((n: Json) => n.kind === 'locks_taken');
    assert.equal(notice.by, 'Ben');
    assert.equal(notice.count, 1);
    await env.anna.ok('session/end', host.ids());
  });

  it('lapse timing: idleSeconds 290 in an 8 s poll → lapsed within 11 s; idleSeconds 0 → not lapsed again', async (t) => {
    if (!needsClock(env, t)) return;
    const host = await startLocal(env);
    const { m: ben } = await joinSession(env.ben, host.sessionId);
    await ben.lock(['fass-1']);
    await host.drain();
    const before = host.versions.locks;
    await ben.poll({ waitMs: 0, presence: { position: [0, 0, 0], idleSeconds: 290 } });
    await env.advance!(10_500);
    const res = await host.poll({ waitMs: 0 });
    assert.ok(res.locks.version > before);
    assert.equal(holder(res, ben.participantId).lapsed, true);
    await ben.poll({ waitMs: 0, presence: { position: [0, 0, 0], idleSeconds: 0 } });
    const back = await host.poll({ waitMs: 0 });
    assert.equal(holder(back, ben.participantId).lapsed, false);
    await env.anna.ok('session/end', host.ids());
  });

  it('a new element locked before the op that creates it is denied to the next who clicks it', async () => {
    const host = await startLocal(env);
    const { m: ben } = await joinSession(env.ben, host.sessionId);
    const barrel = `${host.key}-500`;
    await host.lock([barrel]);
    await host.sendOk([host.op(`{"v":1,"type":"core:create","id":"${barrel}"}`)]);
    await ben.drain();
    const res = await ben.lock([barrel]);
    assert.deepEqual(res.denied, [{ element: barrel, holder: host.participantId }]);
    await env.anna.ok('session/end', host.ids());
  });

  it('locks/release: only with canManage; a repeated release leaves the new set alone', async () => {
    const host = await startLocal(env);
    const { m: ben } = await joinSession(env.ben, host.sessionId);
    await ben.lock(['x1', 'x2']);
    await env.ben.err('locks/release', { requestId: rid(), ...ben.ids(), target: host.participantId }, 'not_allowed');
    const requestId = rid();
    await env.anna.ok('locks/release', { requestId, ...host.ids(), target: ben.participantId });
    let res = await ben.poll({ waitMs: 0 });
    assert.ok(res.notices.list.some((n: Json) => n.kind === 'locks_released' && n.by === 'Anna'));
    assert.equal(holder(res, ben.participantId), undefined);
    await ben.lock(['x3']);
    await env.anna.ok('locks/release', { requestId, ...host.ids(), target: ben.participantId });
    res = await host.poll({ waitMs: 0 });
    assert.deepEqual(holder(res, ben.participantId).elements, ['x3']);
    await env.anna.ok('session/end', host.ids());
  });

  it('live: at most 5 of 10 per second accepted; a repetition is accepted false', async () => {
    const host = await startLocal(env);
    let accepted = 0;
    let lastN = 0;
    const liveId = `${host.key}-L1`;
    for (let n = 1; n <= 10; n++) {
      const res = await env.anna.ok('live/put', {
        ...host.ids(),
        liveId,
        n,
        mode: 'values',
        values: [{ element: 'e1', position: [n, 2, 3], ypr: [0, 0, 0], scale: 1 }],
      });
      if (res.accepted) {
        accepted++;
        lastN = n;
      }
      await sleep(100);
    }
    assert.ok(accepted >= 1 && accepted <= 5, `${accepted} accepted`);
    await sleep(250);
    const again = await env.anna.ok('live/put', { ...host.ids(), liveId, n: lastN, mode: 'values', values: [] });
    assert.equal(again.accepted, false);
    await env.anna.ok('session/end', host.ids());
  });

  it('live: values and transform reach the others; commit + op with the liveId removes it, never before the op', async () => {
    const host = await startLocal(env);
    const { m: ben } = await joinSession(env.ben, host.sessionId);
    const liveId = `${ben.key}-L7`;
    await env.ben.ok('live/put', { ...ben.ids(), liveId, n: 1, mode: 'transform', transform: { pivot: [10, 0, 10], translate: [1.5, 0, 0], yaw: 45 } });
    let res = await host.poll({ waitMs: 0 });
    const st = res.live.states.find((s: Json) => s.participantId === ben.participantId);
    assert.equal(st.mode, 'transform');
    assert.deepEqual(st.transform, { pivot: [10, 0, 10], translate: [1.5, 0, 0], yaw: 45 });
    const end = await env.ben.ok('live/put', { ...ben.ids(), liveId, n: 2, mode: 'end', end: 'commit' });
    assert.equal(end.accepted, true);
    res = await host.poll({ waitMs: 0 });
    assert.equal(res.live.states.find((s: Json) => s.participantId === ben.participantId).committed, true);
    await ben.sendOk([ben.op(opText(1), { liveId })]);
    res = await host.poll({ waitMs: 0 });
    assert.ok(res.ops.some((o: Json) => o.liveId === liveId));
    assert.ok(!res.live.states.some((s: Json) => s.participantId === ben.participantId), 'gone in the same answer as the op');

    const cancelId = `${ben.key}-L8`;
    await sleep(LIVE_GAP);
    await env.ben.ok('live/put', { ...ben.ids(), liveId: cancelId, n: 1, mode: 'values', values: [{ element: 'e', position: [0, 0, 0], ypr: [90, 0, 0], scale: 1.5 }] });
    await host.drain();
    await env.ben.ok('live/put', { ...ben.ids(), liveId: cancelId, n: 2, mode: 'end', end: 'cancel' });
    res = await host.poll({ waitMs: 0 });
    assert.ok(!res.live.states.some((s: Json) => s.participantId === ben.participantId));
    await env.anna.ok('session/end', host.ids());
  });

  it('live: a commit without its op is dropped after 10 s', async (t) => {
    if (!needsClock(env, t)) return;
    const host = await startLocal(env);
    const { m: ben } = await joinSession(env.ben, host.sessionId);
    const liveId = `${ben.key}-L9`;
    await env.ben.ok('live/put', { ...ben.ids(), liveId, n: 1, mode: 'values', values: [{ element: 'e', position: [0, 0, 0], ypr: [0, 0, 0], scale: 1 }] });
    await env.ben.ok('live/put', { ...ben.ids(), liveId, n: 2, mode: 'end', end: 'commit' });
    await host.drain();
    await env.advance!(10_500);
    const res = await host.poll({ waitMs: 0 });
    assert.ok(!res.live.states.some((s: Json) => s.participantId === ben.participantId));
    await env.anna.ok('session/end', host.ids());
  });
});
