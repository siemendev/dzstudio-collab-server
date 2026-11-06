import assert from 'node:assert/strict';
import { after, before, describe, it } from 'node:test';
import { partsOf, pieces, type Json } from '../support/client.js';
import { invite, joinSession, opText, redeemAndJoin, startLocal } from '../support/flows.js';
import { startEnv, type Env } from '../support/target.js';

describe('operations: order, retries, batches, large operations', () => {
  let env: Env;
  before(async () => {
    env = await startEnv();
  });
  after(async () => env.close());

  it('ops/send numbers operations; a lost answer is repeated and every operation is in the log once', async () => {
    const host = await startLocal(env);
    const { m: ben } = await joinSession(env.ben, host.sessionId);
    const ops = [host.op(opText(1)), host.op(opText(2)), host.op(opText(3))];
    await env.t.fireAndDrop('ops/send', { protocol: '1.0', token: env.anna.token, ...host.ids(), ops });
    const first = await host.sendOk(ops);
    const again = await host.sendOk(ops);
    assert.deepEqual(again.results, first.results, 'a repetition names the same seqs');
    assert.equal(first.nextClientSeq, 4);
    assert.deepEqual(first.results.map((r: Json) => r.clientSeq), [1, 2, 3]);
    const seqs = first.results.map((r: Json) => r.seq);
    assert.deepEqual(seqs, [seqs[0], seqs[0] + 1, seqs[0] + 2]);
    await ben.drain();
    await host.drain();
    for (const m of [ben, host]) {
      const texts = [...m.log.values()].map((e) => e.parts.flat().join(''));
      assert.deepEqual(texts, [opText(1), opText(2), opText(3)], 'each operation exactly once, also for the sender');
      assert.equal(m.log.get(seqs[0])!.participantId, host.participantId);
      assert.equal(m.log.get(seqs[0])!.opId, ops[0].opId);
    }
    await env.anna.ok('session/end', host.ids());
  });

  it('a gap is client_seq_gap with expectedClientSeq; sending from there puts 8 before 9', async () => {
    const host = await startLocal(env);
    for (let i = 1; i <= 7; i++) await host.sendOk([host.op(opText(i))]);
    const op8 = host.op(opText(8));
    const op9 = host.op(opText(9));
    const gap = await host.user.err('ops/send', { ...host.ids(), ops: [op9] }, 'client_seq_gap');
    assert.equal(gap.expectedClientSeq, 8);
    const res = await host.sendOk([op8, op9]);
    assert.ok(res.results[0].seq < res.results[1].seq);
    await env.anna.ok('session/end', host.ids());
  });

  it('what came before an error in the same request still counts', async () => {
    const host = await startLocal(env);
    const a = host.op(opText(1));
    host.clientSeq++;
    const c = host.op(opText(3));
    const err = await host.user.err('ops/send', { ...host.ids(), ops: [a, c] }, 'client_seq_gap');
    assert.equal(err.expectedClientSeq, 2);
    await host.drain();
    assert.equal(host.log.size, 1);
    await env.anna.ok('session/end', host.ids());
  });

  it('an opId already in the log is not appended again under a new clientSeq', async () => {
    const host = await startLocal(env);
    const op = host.op(opText(1));
    const first = await host.sendOk([op]);
    const again = await host.sendOk([{ ...op, clientSeq: host.clientSeq++ }]);
    assert.equal(again.results[0].seq, first.results[0].seq);
    await host.drain();
    assert.equal(host.log.size, 1);
    await env.anna.ok('session/end', host.ids());
  });

  it('two clients sending 50 operations each at once: each batch stands unbroken in the log', async () => {
    const host = await startLocal(env);
    const { m: ben } = await joinSession(env.ben, host.sessionId);
    const a = Array.from({ length: 50 }, (_, i) => host.op(opText(i, 'A')));
    const b = Array.from({ length: 50 }, (_, i) => ben.op(opText(i, 'B')));
    const [ra, rb] = await Promise.all([host.sendOk(a), ben.sendOk(b)]);
    const sa = ra.results.map((r: Json) => r.seq);
    const sb = rb.results.map((r: Json) => r.seq);
    assert.equal(sa[49] - sa[0], 49);
    assert.equal(sb[49] - sb[0], 49);
    assert.ok(sa[49] < sb[0] || sb[49] < sa[0]);
    await host.drain();
    const order = [...host.log.entries()].sort((x, y) => x[0] - y[0]).map(([, e]) => e.participantId);
    const switches = order.filter((p, i) => i > 0 && p !== order[i - 1]).length;
    assert.equal(switches, 1);
    await env.anna.ok('session/end', host.ids());
  });

  it('an operation with 10 KB source and multi-byte characters arrives byte for byte with the same piece boundaries', async () => {
    const host = await startLocal(env);
    const { m: ben } = await joinSession(env.ben, host.sessionId);
    const src = 'class Café { void Naïve() { Print("éèç ñ € 😀 \\" quoted \\" "); } }\n'.repeat(160);
    const op = host.op(JSON.stringify({ v: 1, type: 'core:set', src }));
    assert.ok(op.pieces.length > 10);
    await host.sendOk([op]);
    await ben.drain();
    const got = [...ben.log.values()].find((e) => e.opId === op.opId)!;
    assert.deepEqual(got.parts[0], op.pieces);
    await env.anna.ok('session/end', host.ids());
  });

  it('an operation over 1 MB travels in parts, repeated parts change nothing, other ops wait (batch_pending)', async () => {
    const host = await startLocal(env);
    const { m: ben } = await joinSession(env.ben, host.sessionId);
    const objects = Array.from({ length: 10_000 }, (_, i) => ({ id: `${host.key}-${i + 10}`, type: 'Land_Barrel_Blue', pos: [i * 0.5, 12.25, 300.125] }));
    const text = JSON.stringify({ v: 1, type: 'core:import', objects, pad: 'ö'.repeat(400_000) });
    const parts = partsOf(pieces(text));
    assert.ok(parts.length >= 2 && parts.length <= 3, `${parts.length} parts`);
    const clientSeq = host.clientSeq++;
    const opId = host.newOpId();
    const item = (i: number) => ({ clientSeq, opId, part: i, parts: parts.length, pieces: parts[i] });
    const first = await host.sendOk([item(0)]);
    assert.deepEqual(first.results, [], 'parts without the last part do not appear');
    assert.equal(first.nextClientSeq, clientSeq);
    await host.sendOk([item(0)]);
    await host.user.err('ops/send', { ...host.ids(), ops: [host.op(opText(99))] }, 'batch_pending');
    host.clientSeq--;
    host.opCounter--;
    await ben.sendOk([ben.op(opText(5))]);
    let done: Json | undefined;
    for (let i = 1; i < parts.length; i++) done = await host.sendOk([item(i)]);
    assert.equal(done!.results.length, 1);
    const seq = done!.results[0].seq;
    const retry = await host.sendOk([item(parts.length - 1)]);
    assert.deepEqual(retry.results, [{ clientSeq, seq }]);

    await ben.drain();
    const got = ben.log.get(seq)!;
    assert.equal(got.parts.length, parts.length);
    assert.equal(got.parts.flat().join(''), text);
    assert.deepEqual(got.parts, parts);
    assert.equal([...ben.log.values()].filter((e) => e.opId === opId).length, 1, 'applied exactly once');
    await env.anna.ok('session/end', host.ids());
  });

  it('poll fills up to maxBytes and says more; the client asks again and gets the rest', async () => {
    const host = await startLocal(env);
    const big = 'x'.repeat(900);
    for (let round = 0; round < 3; round++) {
      await host.sendOk(Array.from({ length: 100 }, (_, i) => host.op(opText(round * 100 + i, big))));
    }
    const { m: ben } = await joinSession(env.ben, host.sessionId, { firstPoll: false });
    const res = await ben.poll({ waitMs: 0, maxBytes: 50_000 });
    assert.equal(res.more, true);
    assert.ok(res.ops.length > 0 && res.ops.length < 300);
    await ben.drain();
    assert.equal(ben.log.size, 300);
    const tiny = await joinSession(env.ben, host.sessionId, { firstPoll: false });
    const one = await tiny.m.poll({ waitMs: 0, maxBytes: 1 });
    assert.equal(one.ops.length, 1, 'always at least one operation');
    await env.anna.ok('session/end', host.ids());
  });

  it('operations of a participant without locks are passed on; system and liveId travel along', async () => {
    const host = await startLocal(env);
    const { m: ben } = await joinSession(env.ben, host.sessionId);
    const op = ben.op(opText(1), { system: true, liveId: `${ben.key}-L1` });
    await ben.sendOk([op]);
    await host.drain();
    const e = [...host.log.values()][0];
    assert.equal(e.system, true);
    assert.equal(e.liveId, `${ben.key}-L1`);
    await env.anna.ok('session/end', host.ids());
  });

  it('three participants, 300 mixed changes with random delays: everyone ends with the same log', async () => {
    const host = await startLocal(env);
    const { m: ben } = await joinSession(env.ben, host.sessionId);
    const carla = await redeemAndJoin(env.carla, await invite(host), host.sessionId);
    const members = [host, ben, carla];
    await Promise.all(
      members.map(async (m, k) => {
        for (let i = 0; i < 10; i++) {
          await new Promise((r) => setTimeout(r, Math.random() * 30));
          await m.sendOk(Array.from({ length: 10 }, (_, j) => m.op(opText(k * 1000 + i * 10 + j))));
          await m.poll({ waitMs: 0 });
        }
      }),
    );
    for (const m of members) await m.drain();
    const logs = members.map((m) => JSON.stringify([...m.log.entries()].sort((a, b) => a[0] - b[0])));
    assert.equal(host.log.size, 300);
    assert.equal(logs[0], logs[1]);
    assert.equal(logs[1], logs[2]);
    await env.anna.ok('session/end', host.ids());
  });

  it('a cursor beyond the highest seq is refused', async () => {
    const host = await startLocal(env);
    await host.user.err('poll', { ...host.ids(), after: 999, waitMs: 0 }, 'bad_request');
    await env.anna.ok('session/end', host.ids());
  });
});
