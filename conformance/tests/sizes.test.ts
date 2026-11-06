import assert from 'node:assert/strict';
import { after, before, describe, it } from 'node:test';
import { MAX_OP_BYTES, PART_BYTES, partsOf, pieces, piecesBytes, type Json } from '../support/client.js';
import { joinSession, opText, startLocal } from '../support/flows.js';
import { startEnv, type Env } from '../support/target.js';

/** ASCII text whose pieces, as written, are exactly `written` bytes (every piece adds its two quotes). */
function textWritten(written: number): string {
  const text = 'x'.repeat(Math.floor((written * 1000) / 1002));
  const extra = written - piecesBytes(pieces(text));
  return text + 'y'.repeat(Math.max(0, extra));
}

describe('sizes: maxOpBytes, parts counted as written, poll answers under 4 MB', () => {
  let env: Env;
  before(async () => {
    env = await startEnv();
  });
  after(async () => env.close());

  it('info names maxOpBytes 3 670 016 and maxChainOps 16', () => {
    assert.equal(env.info.limits.maxOpBytes, MAX_OP_BYTES);
    assert.equal(env.info.limits.maxChainOps, 16);
  });

  it('parts are counted as written: a part of 1 MB raw text plus quotes is too_large', async () => {
    const host = await startLocal(env);
    const raw = pieces('z'.repeat(PART_BYTES));
    assert.ok(piecesBytes(raw) > PART_BYTES);
    const clientSeq = host.clientSeq;
    const e = await host.user.err('ops/send', { ...host.ids(), ops: [{ clientSeq, opId: host.newOpId(), part: 0, parts: 2, pieces: raw }] }, 'too_large');
    assert.equal(typeof e.maxBytes, 'number');
    await host.sendOk([host.op(opText(1))]);
    await env.anna.ok('session/end', host.ids());
  });

  it('an operation of exactly maxOpBytes is accepted and arrives whole in one answer, also with maxBytes 1 MB', async () => {
    const host = await startLocal(env);
    const { m: ben } = await joinSession(env.ben, host.sessionId);
    const text = textWritten(MAX_OP_BYTES);
    const parts = partsOf(pieces(text));
    assert.equal(piecesBytes(parts.flat()), MAX_OP_BYTES);
    assert.equal(parts.length, 4);
    const clientSeq = host.clientSeq++;
    const opId = host.newOpId();
    const res = await host.sendOk(parts.map((p, i) => ({ clientSeq, opId, part: i, parts: parts.length, pieces: p })));
    assert.equal(res.results.length, 1);
    const seq = res.results[0].seq;
    const got = await ben.poll({ waitMs: 0, maxBytes: PART_BYTES });
    const mine = got.ops.filter((o: Json) => o.seq === seq);
    assert.equal(mine.length, 4, 'all parts in the same answer');
    assert.equal(mine.map((o: Json) => o.pieces).flat().join(''), text);
    await env.anna.ok('session/end', host.ids());
  });

  it('an operation over maxOpBytes is too_large; nothing is appended and the next operation goes through', async () => {
    const host = await startLocal(env);
    const { m: ben } = await joinSession(env.ben, host.sessionId);
    const text = textWritten(3_774_874);
    const parts = partsOf(pieces(text));
    assert.ok(parts.length >= 4);
    const clientSeq = host.clientSeq;
    const opId = host.newOpId();
    const e = await host.user.err(
      'ops/send',
      { ...host.ids(), ops: parts.map((p, i) => ({ clientSeq, opId, part: i, parts: parts.length, pieces: p })) },
      'too_large',
    );
    assert.equal(e.maxBytes, MAX_OP_BYTES);
    const next = await host.sendOk([host.op(opText(1))]);
    assert.equal(next.results[0].clientSeq, clientSeq, 'the refused operation took no clientSeq');
    await ben.drain();
    assert.equal(ben.log.size, 1, 'nothing of the refused operation is in the log');
    assert.equal(ben.text(next.results[0].seq), opText(1));
    await env.anna.ok('session/end', host.ids());
  });

  it('first operation plus the other fields over 4 MB: participants, locks, live, notices, saving left out, more: true', async () => {
    const host = await startLocal(env);
    const { m: ben } = await joinSession(env.ben, host.sessionId);
    const id = (who: string, i: number) => `${who}-${i}-`.padEnd(64, 'q');
    const a = await host.lock(Array.from({ length: 20_000 }, (_, i) => id('a', i)));
    assert.equal(a.granted.length, 20_000);
    const b = await ben.lock(Array.from({ length: 5_000 }, (_, i) => id('b', i)));
    assert.equal(b.granted.length, 5_000);
    const text = textWritten(MAX_OP_BYTES);
    const parts = partsOf(pieces(text));
    const clientSeq = host.clientSeq++;
    const opId = host.newOpId();
    const sent = await host.sendOk(parts.map((p, i) => ({ clientSeq, opId, part: i, parts: parts.length, pieces: p })));
    const seq = sent.results[0].seq;
    const before = { ...ben.versions };
    const res = await ben.poll({ waitMs: 0, maxBytes: 4_194_304 });
    assert.equal(res.ok, true);
    assert.equal(res.ops.filter((o: Json) => o.seq === seq).length, parts.length, 'the operation comes whole');
    for (const k of ['participants', 'locks', 'live', 'notices', 'saving']) assert.equal(res[k], undefined, `${k} left out`);
    assert.equal(res.more, true);
    assert.ok(Array.isArray(res.presence), 'presence stands in every answer');
    assert.deepEqual(ben.versions, before, 'versions unchanged');
    const next = await ben.poll({ waitMs: 0 });
    const held = (next.locks.holders as Json[]).reduce((n, h) => n + h.elements.length, 0);
    assert.equal(held, 25_000, 'the next answer brings the locks');
    await env.anna.ok('session/end', host.ids());
  });

  it('the session holds at most 25 000 locked elements; more are denied with holder null', async () => {
    const host = await startLocal(env);
    const { m: ben } = await joinSession(env.ben, host.sessionId);
    await host.lock(Array.from({ length: 20_000 }, (_, i) => `h-${i}`));
    const res = await ben.lock(Array.from({ length: 5_001 }, (_, i) => `b-${i}`));
    assert.equal(res.granted.length, 5_000);
    assert.deepEqual(res.denied, [{ element: 'b-5000', holder: null }]);
    await env.anna.ok('session/end', host.ids());
  });
});
