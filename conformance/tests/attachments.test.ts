import assert from 'node:assert/strict';
import { after, before, describe, it } from 'node:test';
import { partsOf, pieces, rid, sha256, sleep, type Json, type Member } from '../support/client.js';
import { joinSession, opText, startHosted, startLocal } from '../support/flows.js';
import { startEnv, type Env } from '../support/target.js';

const MB16 = 16_777_216;

describe('attachments: attachment/put, attachment/get, attachment/want', () => {
  let env: Env;
  before(async () => {
    env = await startEnv();
  });
  after(async () => env.close());

  const LOOT = '\t\t<group name="Land_Garage_Big"><container name="lootFloor"><point pos="1.5 0 -2.25" range="0.5"/></container></group>\n'.repeat(18_000) + 'Café ✓';

  /** Uploads a text as an attachment, part by part; returns the checksum and the answers. */
  async function put(m: Member, text: string, opts: { seq?: number; checksum?: string } = {}): Promise<{ checksum: string; answers: Json[]; parts: string[][] }> {
    const checksum = opts.checksum ?? `sha256:${sha256(text)}`;
    const parts = text === '' ? [[]] : partsOf(pieces(text));
    const answers: Json[] = [];
    for (let i = 0; i < parts.length; i++) {
      answers.push(
        await m.user.ok('attachment/put', { ...m.ids(), checksum, seq: opts.seq ?? m.cursor, bytes: Buffer.byteLength(text), part: i, parts: parts.length, pieces: parts[i] }),
      );
    }
    return { checksum, answers, parts };
  }

  async function get(m: Member, checksum: string): Promise<{ text: string; parts: string[][]; first: Json }> {
    const first = await m.user.ok('attachment/get', { ...m.ids(), checksum });
    const parts: string[][] = [first.pieces];
    for (let part = 1; part < first.parts; part++) {
      const res = await m.user.ok('attachment/get', { ...m.ids(), checksum, part });
      assert.equal(res.part, part);
      assert.equal(res.parts, first.parts);
      parts.push(res.pieces);
    }
    return { text: parts.flat().join(''), parts, first };
  }

  async function waitForWanted(m: Member, checksum: string, withinMs: number): Promise<boolean> {
    const deadline = Date.now() + withinMs;
    while (Date.now() < deadline) {
      const res = await m.poll({ waitMs: Math.max(0, Math.min(8000, deadline - Date.now())) });
      assert.equal(res.ok, true, JSON.stringify(res.error));
      if ((res.notices?.list ?? []).some((n: Json) => n.kind === 'attachment_wanted' && n.checksum === checksum)) return true;
    }
    return false;
  }

  it('a 2 MB attachment with multi-byte characters goes up in parts and comes down byte for byte with the same piece boundaries', async () => {
    const host = await startLocal(env);
    const ben = (await joinSession(env.ben, host.sessionId)).m;
    await host.sendOk([host.op(opText(1))]);
    await host.drain();
    const up = await put(host, LOOT);
    assert.ok(up.parts.length >= 2);
    assert.deepEqual(up.answers.map((a) => a.complete), [...up.parts.slice(1).map(() => false), true]);
    const got = await get(ben, up.checksum);
    assert.equal(got.text, LOOT);
    assert.deepEqual(got.parts, up.parts);
    assert.equal(got.first.checksum, up.checksum);
    assert.equal(got.first.bytes, Buffer.byteLength(LOOT));
    const again = await ben.user.ok('attachment/put', { ...ben.ids(), checksum: up.checksum, seq: ben.cursor, bytes: Buffer.byteLength(LOOT), part: 0, parts: up.parts.length, pieces: up.parts[0] });
    assert.equal(again.complete, true, 'a second upload of the same checksum is complete at once');
    await env.anna.ok('session/end', host.ids());
  });

  it('unknown or incomplete: attachment_unknown; part out of range, differing parts or bytes: bad_request; an empty file is one part', async () => {
    const host = await startLocal(env);
    await host.user.err('attachment/get', { ...host.ids(), checksum: 'never' }, 'attachment_unknown');
    const base = { ...host.ids(), checksum: 'half', seq: 0, bytes: 10 };
    assert.equal((await host.user.ok('attachment/put', { ...base, part: 0, parts: 2, pieces: ['12345'] })).complete, false);
    await host.user.err('attachment/get', { ...host.ids(), checksum: 'half' }, 'attachment_unknown');
    await host.user.err('attachment/put', { ...base, part: 1, parts: 3, pieces: ['67890'] }, 'bad_request');
    await host.user.err('attachment/put', { ...base, bytes: 11, part: 1, parts: 2, pieces: ['67890'] }, 'bad_request');
    assert.equal((await host.user.ok('attachment/put', { ...base, part: 1, parts: 2, pieces: ['67890'] })).complete, true);
    await host.user.err('attachment/get', { ...host.ids(), checksum: 'half', part: 2 }, 'bad_request');
    const empty = await put(host, '', { checksum: 'empty' });
    assert.equal(empty.answers[0].complete, true);
    const got = await host.user.ok('attachment/get', { ...host.ids(), checksum: 'empty' });
    assert.deepEqual([got.parts, got.pieces, got.bytes], [1, [], 0]);
    await env.anna.ok('session/end', host.ids());
  });

  it('over 16 MB is too_large with maxBytes 16777216; a part over 1 MB is too_large', async () => {
    const host = await startLocal(env);
    const e = await host.user.err('attachment/put', { ...host.ids(), checksum: 'big', seq: 0, bytes: MB16 + 1, part: 0, parts: 17, pieces: ['x'] }, 'too_large');
    assert.equal(e.maxBytes, MB16);
    const tooBig = pieces('y'.repeat(1_100_000));
    const e2 = await host.user.err('attachment/put', { ...host.ids(), checksum: 'part', seq: 0, bytes: 1_100_000, part: 0, parts: 1, pieces: tooBig }, 'too_large');
    assert.equal(typeof e2.maxBytes, 'number');
    await env.anna.ok('session/end', host.ids());
  });

  it('attachment/want asks the uploader again (within 1 s); after the uploader left, everybody else; one notice per 10 s', async (t) => {
    if (!env.forgetAttachment) return t.skip('server has no control to lose an attachment');
    const ref = env.hosted ? await env.hosted.ref() : undefined;
    const anna = ref ? await startHosted(env, env.anna, ref) : await startLocal(env);
    const ben = (await joinSession(env.ben, anna.sessionId)).m;
    const code = (await anna.user.ok('invite/create', { requestId: rid(), ...anna.ids() })).code;
    await env.carla.ok('invite/redeem', { requestId: rid(), code });
    const carla = (await joinSession(env.carla, anna.sessionId)).m;
    await ben.sendOk([ben.op(opText(1))]);
    await ben.drain();
    await anna.drain();
    await carla.drain();
    const up = await put(ben, LOOT);
    const got = await get(carla, up.checksum);
    assert.equal(got.text, LOOT);

    await env.forgetAttachment(anna.sessionId, up.checksum);
    await anna.user.err('attachment/get', { ...anna.ids(), checksum: up.checksum }, 'attachment_unknown');
    const waiting = waitForWanted(ben, up.checksum, 1500);
    await sleep(100);
    const t0 = Date.now();
    await anna.user.ok('attachment/want', { ...anna.ids(), checksum: up.checksum });
    assert.equal(await waiting, true, 'the uploader gets attachment_wanted');
    assert.ok(Date.now() - t0 < 1000);
    await anna.user.ok('attachment/want', { ...anna.ids(), checksum: up.checksum });
    assert.equal(await waitForWanted(ben, up.checksum, 300), false, 'a second wish within 10 s gives no second notice');
    const carlaPoll = await carla.poll({ waitMs: 0 });
    assert.ok(!(carlaPoll.notices?.list ?? []).some((n: Json) => n.kind === 'attachment_wanted'), 'only the uploader is asked');

    await put(ben, LOOT);
    assert.equal((await get(anna, up.checksum)).text, LOOT);
    await anna.user.ok('attachment/want', { ...anna.ids(), checksum: up.checksum });
    assert.equal(await waitForWanted(ben, up.checksum, 300), false, 'no notice for an attachment that is there');

    await env.forgetAttachment(anna.sessionId, up.checksum);
    await ben.leave();
    await anna.user.ok('attachment/want', { ...anna.ids(), checksum: up.checksum });
    assert.equal(await waitForWanted(carla, up.checksum, 1500), true, 'with the uploader gone, everybody else is asked');
    const own = await anna.poll({ waitMs: 0 });
    assert.ok(!(own.notices?.list ?? []).some((n: Json) => n.kind === 'attachment_wanted'), 'not the one who asked');
    await put(carla, got.text);
    assert.equal((await get(anna, up.checksum)).text, LOOT);
    await env.anna.ok('session/end', anna.ids());
  });

  it('incomplete parts are dropped when the uploader leaves; the session end drops every attachment', async () => {
    const host = await startLocal(env);
    const ben = (await joinSession(env.ben, host.sessionId)).m;
    await ben.user.ok('attachment/put', { ...ben.ids(), checksum: 'c1', seq: 0, bytes: 4, part: 0, parts: 2, pieces: ['ab'] });
    await ben.leave();
    const res = await host.user.ok('attachment/put', { ...host.ids(), checksum: 'c1', seq: 0, bytes: 7, part: 0, parts: 1, pieces: ['abcdefg'] });
    assert.equal(res.complete, true, 'the half upload of who left is gone, a new one with other parts begins');
    await env.anna.ok('session/end', host.ids());
    await env.anna.err('attachment/get', { ...host.ids(), checksum: 'c1' }, 'session_ended');
  });

  it('a complete attachment survives a restart of the server', async (t) => {
    if (!env.restart) return t.skip('cannot restart this target');
    const host = await startLocal(env);
    const up = await put(host, LOOT);
    await env.restart({ hard: true });
    const got = await get(host, up.checksum);
    assert.equal(got.text, LOOT);
    assert.deepEqual(got.parts, up.parts);
    await env.anna.ok('session/end', host.ids());
  });
});
