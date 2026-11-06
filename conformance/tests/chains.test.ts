import assert from 'node:assert/strict';
import { after, before, describe, it } from 'node:test';
import { Member, PART_BYTES, pieces, rid, type Json, checked } from '../support/client.js';
import { chainLinks, joinSession, linkItems, needsHosted, opText, startHosted, startLocal, uploadSnapshot } from '../support/flows.js';
import { startEnv, type Env } from '../support/target.js';

/** Sends the links of one chain; returns the clientSeq of each link. */
function chainOf(m: Member, links: string[][], extra: Json = {}) {
  const opId = m.newOpId();
  const first = m.clientSeq;
  m.clientSeq += links.length;
  const items = links.map((l, i) => linkItems(first + i, opId, i, links.length, l, extra));
  return { opId, items, clientSeqs: links.map((_, i) => first + i) };
}

function smallLinks(n: number, tag = 'k'): string[][] {
  return Array.from({ length: n }, (_, i) => pieces(`{"v":1,"type":"core:import","${tag}":${i},"part":"Größe"}`));
}

describe('chains: large composite steps', () => {
  let env: Env;
  before(async () => {
    env = await startEnv();
  });
  after(async () => env.close());

  it('a 12 MB import travels as 4 links: appended together after the last, consecutive while another client sends', async () => {
    const host = await startLocal(env);
    const { m: ben } = await joinSession(env.ben, host.sessionId);
    const text = JSON.stringify({ v: 1, type: 'core:import', src: 'Größe Übung '.repeat(900_000) }).slice(0, 12_000_000);
    const links = chainLinks(text);
    assert.equal(links.length, 4);
    const c = chainOf(host, links);
    const before = (await host.poll({ waitMs: 0 })).seq;

    const r0 = await host.sendOk(c.items[0]);
    assert.deepEqual(r0.results, [], 'no link appears before the last');
    assert.equal(r0.nextClientSeq, c.clientSeqs[0] + 1, 'nextClientSeq counts on');
    const benOps = ben.sendOk(Array.from({ length: 20 }, (_, i) => ben.op(opText(i, 'B'))));
    const stray = { clientSeq: host.clientSeq, opId: host.newOpId(), pieces: pieces(opText(99)) };
    await host.user.err('ops/send', { ...host.ids(), ops: [stray] }, 'batch_pending');
    host.opCounter--;
    const r1 = await host.sendOk(c.items[1]);
    assert.deepEqual(r1.results, [], 'the next link in a later request is no new batch');
    const again = await host.sendOk(c.items[1]);
    assert.deepEqual(again.results, [], 'a repeated link not yet appended changes nothing');
    await benOps;
    await host.sendOk(c.items[2]);
    const r3 = await host.sendOk(c.items[3]);
    assert.deepEqual(
      r3.results.map((r: Json) => r.clientSeq),
      c.clientSeqs,
      'the answer with the last link names every link',
    );
    const seqs: number[] = r3.results.map((r: Json) => r.seq as number);
    assert.deepEqual(seqs, [seqs[0], seqs[0] + 1, seqs[0] + 2, seqs[0] + 3], 'consecutive, nothing foreign between');
    assert.equal(r3.nextClientSeq, c.clientSeqs[3] + 1);
    const retry = await host.sendOk(c.items[3]);
    assert.deepEqual(retry.results, [{ clientSeq: c.clientSeqs[3], seq: seqs[3] }]);

    const late = (await joinSession(env.ben, host.sessionId, { firstPoll: false })).m;
    late.cursor = before;
    for (let round = 0; round < 50; round++) {
      const res = await late.poll({ waitMs: 0, maxBytes: PART_BYTES });
      assert.equal(res.ok, true);
      const linkSeqs = new Set(res.ops.filter((o: Json) => o.chain).map((o: Json) => o.seq));
      assert.ok(linkSeqs.size <= 1, 'with maxBytes 1 MB every link comes in an answer of its own');
      if (!res.more && late.cursor >= res.seq) break;
    }
    for (let i = 0; i < 4; i++) {
      const e = late.log.get(seqs[i])!;
      assert.deepEqual(e.chain, { index: i, count: 4 });
      assert.equal(e.opId, c.opId);
      assert.equal(e.participantId, host.participantId);
    }
    assert.equal(seqs.map((s: number) => late.text(s)).join(''), text, 'the links joined are the operation');
    await env.anna.ok('session/end', host.ids());
  });

  it('chain errors: count < 2, count > 16, wrong first index, skipped index, differing opId/count/system/liveId', async () => {
    const host = await startLocal(env);
    const cs = host.clientSeq;
    const opId = host.newOpId();
    const p = pieces('{"v":1}');
    const send = (chain: Json, extra: Json = {}) => host.user.call('ops/send', { ...host.ids(), ops: [{ clientSeq: cs, opId, chain, pieces: p, ...extra }] });
    assert.equal((await send({ index: 0, count: 1 })).error.code, 'bad_request');
    const big = await send({ index: 0, count: 17 });
    assert.equal(big.error.code, 'too_large');
    assert.equal(big.error.maxCount, 16);
    assert.equal(big.error.maxBytes, undefined);
    assert.equal((await send({ index: 1, count: 3 })).error.code, 'bad_request');
    const open = await host.sendOk([{ clientSeq: cs, opId, chain: { index: 0, count: 3 }, pieces: p, liveId: 'lv-1' }]);
    assert.deepEqual(open.results, []);
    const link = (chain: Json, extra: Json = {}) =>
      host.user.call('ops/send', { ...host.ids(), ops: [{ clientSeq: cs + 1, opId, chain, pieces: p, liveId: 'lv-1', ...extra }] });
    assert.equal((await link({ index: 2, count: 3 })).error.code, 'bad_request', 'skipped index');
    assert.equal((await link({ index: 1, count: 4 })).error.code, 'bad_request', 'other count');
    assert.equal((await link({ index: 1, count: 3 }, { opId: `${opId}x` })).error.code, 'bad_request', 'other opId');
    assert.equal((await link({ index: 1, count: 3 }, { system: true })).error.code, 'bad_request', 'other system');
    assert.equal((await link({ index: 1, count: 3 }, { liveId: 'lv-2' })).error.code, 'bad_request', 'other liveId');
    await host.sendOk([{ clientSeq: cs + 1, opId, chain: { index: 1, count: 3 }, pieces: p, liveId: 'lv-1' }]);
    const done = await host.sendOk([{ clientSeq: cs + 2, opId, chain: { index: 2, count: 3 }, pieces: p, liveId: 'lv-1' }]);
    assert.deepEqual(done.results.map((r: Json) => r.clientSeq), [cs, cs + 1, cs + 2]);
    host.clientSeq = cs + 3;
    await host.drain();
    const links = [...host.log.values()].filter((e) => e.opId === opId);
    assert.equal(links.length, 3);
    assert.ok(links.every((e) => e.liveId === 'lv-1'));
    await env.anna.ok('session/end', host.ids());
  });

  it('a chain and further operations in one request are appended as one batch; links with parts work', async () => {
    const host = await startLocal(env);
    const big = pieces('Ü'.repeat(700_000));
    assert.ok(big.length > 1000);
    const links = [big, pieces('{"tail":1}')];
    const c = chainOf(host, links);
    const after = host.op(opText(7));
    const res = await host.sendOk([...c.items.flat(), after]);
    assert.deepEqual(res.results.map((r: Json) => r.clientSeq), [...c.clientSeqs, after.clientSeq]);
    const s: number[] = res.results.map((r: Json) => r.seq as number);
    assert.deepEqual(s, [s[0], s[0] + 1, s[0] + 2]);
    assert.ok(c.items[0].length >= 2, 'the first link travels in parts');
    await host.drain();
    assert.equal(host.text(s[0]), big.join(''));
    await env.anna.ok('session/end', host.ids());
  });

  it('a client back with the cursor before the first link gets the whole chain again', async () => {
    const host = await startLocal(env);
    const { m: ben } = await joinSession(env.ben, host.sessionId);
    const start = ben.cursor;
    const c = chainOf(host, smallLinks(3));
    for (const items of c.items) await host.sendOk(items);
    await ben.drain();
    const again = new Member(env.ben, host.sessionId, ben.participantId, ben.key);
    again.cursor = start;
    again.versions = { ...ben.versions };
    await again.drain();
    assert.deepEqual(
      [...again.log.values()].filter((e) => e.opId === c.opId).map((e) => e.chain!.index),
      [0, 1, 2],
    );
    await env.anna.ok('session/end', host.ids());
  });

  it('a checkpoint with the seq of a link before the last is bad_request', async (t) => {
    const host = await startLocal(env);
    let asked = false;
    for (let i = 0; i < 3 && !asked; i++) {
      await host.sendOk(Array.from({ length: 30 }, (_, j) => host.op(opText(i * 30 + j))));
      const c = chainOf(host, smallLinks(3, `c${i}`));
      for (const items of c.items) await host.sendOk(items);
      const res = await host.drain();
      asked = res.checkpointWanted === true || host.last.checkpointWanted === true;
    }
    if (!asked) {
      await env.anna.ok('session/end', host.ids());
      return t.skip('server did not ask for a checkpoint');
    }
    const inner = [...host.log.entries()].find(([, e]) => e.chain && e.chain.index === 1)![0];
    await host.user.err('snapshot/put', { ...host.ids(), seq: inner, part: 0, parts: 1, pieces: ['{}'] }, 'bad_request');
    await uploadSnapshot(host, host.cursor, '{"v":1,"checkpoint":true}');
    await env.anna.ok('session/end', host.ids());
  });

  it('hosted: a chain counts once in unsavedCount; project/save with the seq of an inner link is bad_request', async (t) => {
    if (!needsHosted(env, t)) return;
    const ref = await env.hosted!.ref();
    const anna = await startHosted(env, env.anna, ref);
    const c = chainOf(anna, smallLinks(3));
    for (const items of c.items) await anna.sendOk(items);
    await anna.sendOk([anna.op(opText(1))]);
    await anna.drain();
    const saving = anna.last.saving ?? (await anna.poll({ waitMs: 0, fresh: true })).saving;
    assert.equal(saving.unsavedCount, 2, 'the chain is one operation, plus one single');
    const inner = [...anna.log.entries()].find(([, e]) => e.opId === c.opId && e.chain!.index === 1)![0];
    const files = checked([{ name: 'manifest', pieces: ['{"v":1}'] }]);
    const bad = await env.anna.call('project/save', { requestId: rid(), ...anna.ids(), seq: inner, files, deleted: [] });
    assert.equal(bad.error.code, 'bad_request');
    const last = [...anna.log.entries()].find(([, e]) => e.opId === c.opId && e.chain!.index === 2)![0];
    const ok = await env.anna.ok('project/save', { requestId: rid(), ...anna.ids(), seq: last, files, deleted: [] });
    env.hosted!.remember(ref, ok.hostedRevision);
    const after = (await anna.poll({ waitMs: 0 })).saving;
    assert.equal(after.unsavedCount, 1);
    await env.anna.ok('session/end', anna.ids());
  });
});
