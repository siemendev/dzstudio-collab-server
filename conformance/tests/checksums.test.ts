import assert from 'node:assert/strict';
import { after, before, describe, it } from 'node:test';
import { checked, pieces, rid, sha256, type Json, type Member, type User } from '../support/client.js';
import { opText, startHosted } from '../support/flows.js';
import { startEnv, type Env } from '../support/target.js';

describe('project/save checksums (save checksums, capability hosted)', () => {
  let env: Env;
  let skip = '';
  before(async () => {
    env = await startEnv();
    if (!env.info.capabilities.includes('hosted')) skip = 'no hosted capability';
    else if (!env.hosted) skip = 'no hosted project configured for this target';
  });
  after(async () => env.close());
  const group = (): string => env.hosted?.groupFile ?? 'f-basis-nord';

  const BIG = JSON.stringify({ Objects: [{ name: 'Land_Barrel', pos: [6512.123456, 0.0, 9300.5] }], note: 'Grüße aus Чернарусь – '.repeat(70_000) });
  const files = (n: number) =>
    checked([
      { name: 'manifest', pieces: pieces(`{"format":"dzstudio-project","version":1,"n":${n}}`) },
      { name: group(), pieces: pieces(JSON.stringify({ Objects: [{ name: 'Land_Barrel', pos: [n, 0, 0] }], note: 'Größe' })) },
    ]);

  function body(m: Member, list: Json[], extra: Json = {}): Json {
    return { requestId: rid(), ...m.ids(), seq: m.cursor, files: list, deleted: [], ...extra };
  }

  async function advance(m: Member, n: number): Promise<void> {
    await m.sendOk([m.op(opText(n))]);
    await m.drain();
  }

  async function loadFile(user: User, ref: Json, name: string): Promise<string> {
    const head = await user.ok('project/load', { hostedRef: ref });
    const file = (head.files as Json[]).find((f) => f.name === name)!;
    const parts: string[] = [];
    for (let part = 0; part < file.parts; part++) {
      const res = await user.ok('project/load', { hostedRef: ref, hostedRevision: head.hostedRevision, file: name, part });
      parts.push(...res.pieces);
    }
    return parts.join('');
  }

  it('a save names per file revision and the SHA-256 of the stored bytes, merged per file; the repetition the same', async (t) => {
    if (skip) return t.skip(skip);
    const ref = await env.hosted!.ref();
    const anna = await startHosted(env, env.anna, ref);
    await advance(anna, 1);
    const list = checked([
      { name: 'manifest', pieces: pieces('{"format":"dzstudio-project","version":1}') },
      { name: group(), pieces: pieces(BIG) },
    ]);
    for (const f of list) assert.match(f.checksum, /^[0-9a-f]{64}$/);
    const b = body(anna, list);
    const saved = await env.anna.ok('project/save', b);
    env.hosted!.remember(ref, saved.hostedRevision);
    assert.equal(saved.superseded, false);
    assert.deepEqual(
      (saved.revisions as Json[]).map((r) => [r.name, r.checksum]).sort(),
      list.map((f) => [f.name, f.checksum]).sort(),
    );
    assert.ok((saved.revisions as Json[]).every((r) => typeof r.revision === 'string'));
    assert.equal(sha256(await loadFile(env.anna, ref, group())), list[1].checksum, 'what project/load gives has that checksum');
    if (env.spawned) {
      assert.deepEqual(
        (saved.merged as Json[]).map((m) => [m.target, m.checksum]).sort(),
        list.map((f) => [f.name, f.checksum]).sort(),
        'a store that composes nothing names target = name with the file checksum',
      );
    } else {
      assert.ok(Array.isArray(saved.merged));
    }
    assert.deepEqual(await env.anna.ok('project/save', b), saved, 'the repetition names the same checksums');
    const older = await env.anna.ok('project/save', body(anna, files(0), { seq: anna.cursor - 1 }));
    assert.equal(older.superseded, true);
    assert.equal(older.revisions, undefined);
    assert.equal(older.merged, undefined);
    await env.anna.ok('session/end', anna.ids());
  });

  it('a wrong checksum: checksum_mismatch names the file, nothing is written; the next save with the same seq writes', async (t) => {
    if (skip) return t.skip(skip);
    const ref = await env.hosted!.ref();
    const anna = await startHosted(env, env.anna, ref);
    await advance(anna, 1);
    const first = await env.anna.ok('project/save', body(anna, files(1)));
    env.hosted!.remember(ref, first.hostedRevision);
    await advance(anna, 2);
    const before = await env.anna.ok('project/revision', { hostedRef: ref });
    const savingBefore = (await anna.poll({ waitMs: 0, fresh: true })).saving;

    const list = files(2);
    const sent = list[1].pieces.join('');
    const forged = list.map((f, i) => (i === 1 ? { ...f, checksum: sha256(sent + 'x') } : f));
    const e = await env.anna.err('project/save', body(anna, forged), 'checksum_mismatch');
    assert.deepEqual(e.mismatches, [{ kind: 'file', name: group(), expected: sha256(sent + 'x'), actual: sha256(sent) }]);
    const corrupted = list.map((f, i) => (i === 1 ? { ...f, pieces: [f.pieces[0].replace('Barrel', 'Barrex'), ...f.pieces.slice(1)] } : f));
    const e2 = await env.anna.err('project/save', body(anna, corrupted), 'checksum_mismatch');
    assert.equal(e2.mismatches[0].actual, sha256(corrupted[1].pieces.join('')));
    assert.equal(e2.mismatches[0].expected, list[1].checksum);

    assert.equal((await env.anna.ok('project/revision', { hostedRef: ref })).hostedRevision, before.hostedRevision);
    assert.notEqual(sha256(await loadFile(env.anna, ref, group())), list[1].checksum);
    const savingAfter = (await anna.poll({ waitMs: 0, fresh: true })).saving;
    assert.equal(savingAfter.savedSeq, savingBefore.savedSeq);
    assert.deepEqual(savingAfter.savedBy, savingBefore.savedBy);

    const missing = list.map(({ checksum: _c, ...f }) => f);
    await env.anna.err('project/save', body(anna, missing), 'bad_request');
    assert.equal((await env.anna.ok('project/revision', { hostedRef: ref })).hostedRevision, before.hostedRevision);

    const ok = await env.anna.ok('project/save', body(anna, list));
    env.hosted!.remember(ref, ok.hostedRevision);
    assert.equal(ok.superseded, false);
    assert.notEqual(ok.hostedRevision, before.hostedRevision);
    assert.equal(sha256(await loadFile(env.anna, ref, group())), list[1].checksum);
    await env.anna.ok('session/end', anna.ids());
  });

  it('the file checksum comes before superseded and force; the same requestId with the right checksum then goes on', async (t) => {
    if (skip) return t.skip(skip);
    const ref = await env.hosted!.ref();
    const anna = await startHosted(env, env.anna, ref);
    await advance(anna, 1);
    const saved = await env.anna.ok('project/save', body(anna, files(1)));
    env.hosted!.remember(ref, saved.hostedRevision);
    const wrong = (list: Json[]) => list.map((f, i) => (i === 0 ? { ...f, checksum: '0'.repeat(64) } : f));

    const older = body(anna, wrong(files(0)), { seq: anna.cursor - 1 });
    await env.anna.err('project/save', older, 'checksum_mismatch');
    assert.equal((await env.anna.ok('project/save', { ...older, files: files(0) })).superseded, true);

    await advance(anna, 2);
    const forced = body(anna, wrong(files(2)), { force: true });
    await env.anna.err('project/save', forced, 'checksum_mismatch');
    const written = await env.anna.ok('project/save', { ...forced, files: files(2) });
    env.hosted!.remember(ref, written.hostedRevision);
    assert.equal(written.superseded, false);
    await env.anna.ok('session/end', anna.ids());
  });

  it('the file checksum comes before store_changed; with the right checksum the same request is store_changed', async (t) => {
    if (skip) return t.skip(skip);
    if (!env.hosted!.editOutside) return t.skip('cannot change the store from outside on this target');
    const ref = await env.hosted!.ref();
    const anna = await startHosted(env, env.anna, ref);
    await advance(anna, 1);
    await env.anna.ok('project/save', body(anna, files(1)));
    await env.hosted!.editOutside(ref, group(), '{"Objects":[],"by":"workbench"}');
    await advance(anna, 2);
    const list = files(2);
    const b = body(anna, list.map((f, i) => (i === 1 ? { ...f, checksum: sha256('nope') } : f)));
    const e = await env.anna.err('project/save', b, 'checksum_mismatch');
    assert.equal(e.mismatches[0].kind, 'file');
    const changed = await env.anna.err('project/save', { ...b, files: list }, 'store_changed');
    assert.deepEqual(changed.changed, [group()]);
    const forced = await env.anna.ok('project/save', { ...b, files: list, force: true });
    assert.equal(forced.superseded, false);
    await env.anna.ok('session/end', anna.ids());
  });

  it('a store that composes nothing passes over merged, also wrong or unknown targets', async (t) => {
    if (skip) return t.skip(skip);
    if (!env.spawned) return t.skip('only the reference server is known to compose nothing');
    const ref = await env.hosted!.ref();
    const anna = await startHosted(env, env.anna, ref);
    await advance(anna, 1);
    const list = files(1);
    const res = await env.anna.ok('project/save', body(anna, list, { merged: [{ target: 'missionfile.xml', checksum: 'abc' }] }));
    assert.deepEqual(
      (res.merged as Json[]).map((m) => m.target).sort(),
      list.map((f) => f.name).sort(),
    );
    await env.anna.err('project/save', body(anna, list, { merged: [{ target: 'a', checksum: 'x' }, { target: 'a', checksum: 'y' }] }), 'bad_request');
    await env.anna.ok('session/end', anna.ids());
  });
});
