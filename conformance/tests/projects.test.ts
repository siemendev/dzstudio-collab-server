import assert from 'node:assert/strict';
import { after, before, describe, it } from 'node:test';
import { CLIENT, instanceKey, Member, PART_BYTES, piecesBytes, rid, type Json, type User, checked } from '../support/client.js';
import { joinSession, needsHosted, opText, uploadSnapshot, STATE_TEXT } from '../support/flows.js';
import { startEnv, type Env } from '../support/target.js';

const MANIFEST = '{"format":"dzstudio-project","version":1,"project":{"groups":[{"file":"f-north-base"},{"file":"f-fences"}]}}';

describe('projects at the server: project/list, project/revision, project/load (capability hosted)', () => {
  let env: Env;
  let skip = '';
  before(async () => {
    env = await startEnv();
    if (!env.info.capabilities.includes('hosted')) skip = 'no hosted capability';
    else if (!env.hosted?.edit) skip = 'needs a spawned server with store controls to create projects';
  });
  after(async () => env.close());

  async function project(files: { name: string; content: string }[], projectName = 'North Base', map = 'chernarus'): Promise<Json> {
    const ref = await env.hosted!.ref();
    await env.hosted!.edit!(ref, { files, projectName, map });
    return ref;
  }

  async function loadAll(user: User, ref: Json, head: Json): Promise<Map<string, { text: string; pieces: string[][] }>> {
    const out = new Map<string, { text: string; pieces: string[][] }>();
    for (const f of head.files as Json[]) {
      const parts: string[][] = [];
      for (let part = 0; part < f.parts; part++) {
        const res = await user.ok('project/load', { hostedRef: ref, hostedRevision: head.hostedRevision, file: f.name, part });
        assert.equal(res.hostedRevision, head.hostedRevision);
        assert.equal(res.file, f.name);
        assert.equal(res.part, part);
        assert.equal(res.parts, f.parts);
        assert.ok(piecesBytes(res.pieces) <= PART_BYTES, 'a part holds at most 1 MB, counted as written');
        parts.push(res.pieces);
      }
      out.set(f.name, { text: parts.flat().join(''), pieces: parts });
    }
    return out;
  }

  it('project/list pages through 450 projects: 200, 200, … each one exactly once, next ≤ 200 bytes', async (t) => {
    if (skip) return t.skip(skip);
    const prefix = `list-${rid().slice(0, 6)}-`;
    for (let i = 0; i < 450; i++) {
      await env.hosted!.edit!({ provider: 'fs', key: `${prefix}${String(i).padStart(3, '0')}` }, {
        files: [{ name: 'manifest', content: MANIFEST }],
        projectName: `Project ${i}`,
        map: i % 2 ? 'livonia' : 'chernarus',
      });
    }
    const pages = async (user: User) => {
      const all: Json[][] = [];
      let after: string | undefined;
      for (let round = 0; round < 100; round++) {
        const res = await user.ok('project/list', after === undefined ? {} : { after });
        assert.ok(res.projects.length <= 200);
        all.push(res.projects);
        if (res.next === undefined) break;
        assert.equal(typeof res.next, 'string');
        assert.ok(Buffer.byteLength(res.next) <= 200);
        after = res.next;
      }
      return all;
    };
    const anna = await pages(env.anna);
    assert.ok(anna.length >= 3);
    for (let i = 0; i < anna.length - 1; i++) assert.equal(anna[i].length, 200, 'every page but the last is full');
    const mine = anna.flat().filter((p) => p.hostedRef.key.startsWith(prefix));
    assert.equal(mine.length, 450);
    assert.equal(new Set(mine.map((p) => p.hostedRef.key)).size, 450, 'each project exactly once');
    const p7 = mine.find((p) => p.hostedRef.key === `${prefix}007`)!;
    assert.deepEqual(p7.hostedRef, { provider: 'fs', key: `${prefix}007` });
    assert.equal(p7.projectName, 'Project 7');
    assert.equal(p7.map, 'livonia');
    assert.equal(typeof p7.hostedRevision, 'string');
    assert.equal(p7.canSave, true);
    assert.equal(p7.sessionId, undefined, 'no session runs');

    const dana = (await pages(env.dana)).flat().filter((p) => p.hostedRef.key.startsWith(prefix));
    assert.equal(dana.length, 450);
    assert.ok(dana.every((p) => p.canSave === false));
    const carla = (await pages(env.carla)).flat().filter((p) => p.hostedRef.key.startsWith(prefix));
    assert.deepEqual(carla, [], 'without rights nothing is listed');
    await env.anna.err('project/list', { after: 'no such mark %%%' }, 'bad_request');
  });

  it('project/revision names the state without files; not_allowed without rights, project_unknown for an unknown ref', async (t) => {
    if (skip) return t.skip(skip);
    const ref = await project([{ name: 'manifest', content: MANIFEST }], 'Café Fences', 'livonia');
    const r = await env.anna.ok('project/revision', { hostedRef: ref });
    assert.equal(typeof r.hostedRevision, 'string');
    assert.ok(Buffer.byteLength(r.hostedRevision) <= 200);
    assert.equal(r.projectName, 'Café Fences');
    assert.equal(r.map, 'livonia');
    assert.equal(r.canSave, true);
    assert.equal(r.sessionId, undefined);
    assert.equal(r.files, undefined, 'no files');
    assert.equal((await env.dana.ok('project/revision', { hostedRef: ref })).canSave, false);
    await env.carla.err('project/revision', { hostedRef: ref }, 'not_allowed');
    await env.anna.err('project/revision', { hostedRef: { provider: 'fs', key: `missing-${rid()}` } }, 'project_unknown');
    await env.anna.err('project/load', { hostedRef: { provider: 'fs', key: `missing-${rid()}` } }, 'project_unknown');
    await env.carla.err('project/load', { hostedRef: ref }, 'not_allowed');
    await env.anna.err('project/list', { hostedRef: ref, after: 7 }, 'bad_request');
    await env.hosted!.editOutside!(ref, 'f-fences', '{"Objects":[]}');
    const changed = await env.anna.ok('project/revision', { hostedRef: ref });
    assert.notEqual(changed.hostedRevision, r.hostedRevision, 'a change from outside is a new revision');
  });

  it('rights before existence: without a right, an unknown key, an unknown provider and a bad revision are all not_allowed', async (t) => {
    if (skip) return t.skip(skip);
    const ref = await project([{ name: 'manifest', content: MANIFEST }]);
    const missing = { provider: 'fs', key: `missing-${rid()}` };
    const noStore = { provider: `nostore-${rid().slice(0, 6)}`, key: 'x' };
    for (const endpoint of ['project/revision', 'project/load']) {
      await env.carla.err(endpoint, { hostedRef: missing }, 'not_allowed');
      await env.carla.err(endpoint, { hostedRef: noStore }, 'not_allowed');
      await env.anna.err(endpoint, { hostedRef: noStore }, 'not_allowed');
    }
    await env.carla.err('project/load', { hostedRef: ref, hostedRevision: '???' }, 'not_allowed');
    await env.carla.err('project/load', { hostedRef: ref, hostedRevision: '???', file: 'manifest' }, 'not_allowed');
    await env.anna.err('project/load', { hostedRef: ref, hostedRevision: '???' }, 'revision_unknown');
    const body = { requestId: rid(), client: CLIENT, projectName: 'x', map: 'chernarus', kind: 'hosted', hostedRef: noStore, hostedRevision: '',
      instanceKey: instanceKey(), snapshotBytes: 2_000_000_000, snapshotParts: 1 };
    await env.carla.err('session/create', body, 'not_allowed');
  });

  it('project/load: head with bytes and parts, files byte for byte in parts ≤ 1 MB, an empty file is one part without pieces', async (t) => {
    if (skip) return t.skip(skip);
    const big = JSON.stringify({ Objects: Array.from({ length: 20000 }, (_, i) => ({ name: 'Land_Barrel_Café', pos: [i, 0.5, 2], note: '"ä\\n' })) });
    assert.ok(Buffer.byteLength(big) > 1_048_576 * 1.2);
    const files = [
      { name: 'manifest', content: MANIFEST },
      { name: 'f-north-base', content: big },
      { name: 'f-fences', content: '' },
    ];
    const ref = await project(files);
    const head = await env.anna.ok('project/load', { hostedRef: ref });
    assert.equal(head.projectName, 'North Base');
    assert.equal(head.map, 'chernarus');
    assert.equal(head.canSave, true);
    assert.equal(head.sessionId, undefined);
    assert.equal(head.hostedRevision, (await env.anna.ok('project/revision', { hostedRef: ref })).hostedRevision);
    assert.deepEqual(head.files.map((f: Json) => f.name).sort(), ['f-fences', 'f-north-base', 'manifest']);
    for (const f of files) {
      const h = head.files.find((x: Json) => x.name === f.name);
      assert.equal(h.bytes, Buffer.byteLength(f.content), `${f.name}: bytes`);
      assert.ok(h.parts >= 1);
      if (h.revision !== undefined) assert.equal(typeof h.revision, 'string');
    }
    assert.ok(head.files.find((x: Json) => x.name === 'f-north-base').parts >= 2);
    const loaded = await loadAll(env.anna, ref, head);
    for (const f of files) assert.equal(loaded.get(f.name)!.text, f.content, `${f.name} byte for byte`);
    assert.deepEqual(loaded.get('f-fences')!.pieces, [[]], 'an empty file is one part without pieces');
    const stored = await env.hosted!.read!(ref);
    for (const f of stored!.files) assert.equal(loaded.get(f.name)!.text, f.pieces.join(''));

    await env.anna.err('project/load', { hostedRef: ref, hostedRevision: head.hostedRevision, file: 'nope', part: 0 }, 'bad_request');
    await env.anna.err('project/load', { hostedRef: ref, hostedRevision: head.hostedRevision, file: 'manifest', part: 1 }, 'bad_request');
    await env.anna.err('project/load', { hostedRef: ref, hostedRevision: 'r999999', file: 'manifest', part: 0 }, 'revision_unknown');
    await env.anna.err('project/load', { hostedRef: ref, hostedRevision: 'garbage', file: 'manifest', part: 0 }, 'revision_unknown');
    await env.dana.ok('project/load', { hostedRef: ref, hostedRevision: head.hostedRevision, file: 'manifest', part: 0 });
  });

  it('a project with more than 5000 files: the head is too_large', async (t) => {
    if (skip) return t.skip(skip);
    const files = Array.from({ length: 5001 }, (_, i) => ({ name: `f-${i}`, content: '{}' }));
    const ref = await project(files);
    const e = await env.anna.err('project/load', { hostedRef: ref }, 'too_large');
    assert.equal(e.maxCount, 5000);
    assert.equal(e.maxBytes, undefined);
  });

  it('saved while loading: the old hostedRevision keeps loading the old state; project/revision names the new one', async (t) => {
    if (skip) return t.skip(skip);
    const old = 'ö'.repeat(800_000);
    const ref = await project([
      { name: 'manifest', content: MANIFEST },
      { name: 'f-north-base', content: old },
    ]);
    const head = await env.anna.ok('project/load', { hostedRef: ref });
    const file = head.files.find((f: Json) => f.name === 'f-north-base');
    assert.ok(file.parts >= 2);
    const first = await env.anna.ok('project/load', { hostedRef: ref, hostedRevision: head.hostedRevision, file: 'f-north-base', part: 0 });

    const m = await startFromHead(env.anna, ref, head);
    await m.sendOk([m.op(opText(1))]);
    await m.drain();
    const saved = await env.anna.ok('project/save', {
      requestId: rid(),
      ...m.ids(),
      seq: m.cursor,
      files: checked([{ name: 'f-north-base', pieces: ['new'] }, { name: 'manifest', pieces: [MANIFEST] }]),
      deleted: [],
    });
    assert.notEqual(saved.hostedRevision, head.hostedRevision);
    const parts = [first.pieces];
    for (let part = 1; part < file.parts; part++) {
      const res = await env.anna.ok('project/load', { hostedRef: ref, hostedRevision: head.hostedRevision, file: 'f-north-base', part });
      assert.equal(res.hostedRevision, head.hostedRevision);
      parts.push(res.pieces);
    }
    assert.equal(parts.flat().join(''), old, 'the old revision still loads the old state');
    const now = await env.anna.ok('project/revision', { hostedRef: ref });
    assert.equal(now.hostedRevision, saved.hostedRevision);
    const fresh = await env.anna.ok('project/load', { hostedRef: ref });
    assert.equal(fresh.hostedRevision, saved.hostedRevision);
    const got = await env.anna.ok('project/load', { hostedRef: ref, hostedRevision: saved.hostedRevision, file: 'f-north-base', part: 0 });
    assert.deepEqual(got.pieces, ['new']);
    await env.anna.ok('session/end', m.ids());
  });

  async function startFromHead(user: User, ref: Json, head: Json): Promise<Member> {
    const key = instanceKey();
    const res = await user.ok('session/create', {
      requestId: rid(),
      client: CLIENT,
      projectName: head.projectName,
      map: head.map,
      kind: 'hosted',
      hostedRef: ref,
      hostedRevision: head.hostedRevision,
      instanceKey: key,
      snapshotBytes: Buffer.byteLength(STATE_TEXT),
      snapshotParts: 1,
    });
    const m = new Member(user, res.sessionId, res.participantId, key);
    await uploadSnapshot(m, 0, STATE_TEXT);
    await m.drain();
    return m;
  }

  it('without a provider plugin: load, start a session at once, save without store_changed; others join it', async (t) => {
    if (skip) return t.skip(skip);
    const ref = await project([
      { name: 'manifest', content: MANIFEST },
      { name: 'f-north-base', content: '{"Objects":[]}' },
    ]);
    const danaBefore = await env.dana.ok('project/revision', { hostedRef: ref });
    assert.equal(danaBefore.canSave, false);
    assert.equal(danaBefore.sessionId, undefined, 'Dana may not open it without a running session');

    const head = await env.anna.ok('project/load', { hostedRef: ref });
    await loadAll(env.anna, ref, head);
    const anna = await startFromHead(env.anna, ref, head);
    const benSees = await env.ben.ok('project/revision', { hostedRef: ref });
    assert.equal(benSees.sessionId, anna.sessionId);
    const benHead = await env.ben.ok('project/load', { hostedRef: ref });
    assert.equal(benHead.sessionId, anna.sessionId, 'the head names the running session');
    const { m: ben } = await joinSession(env.ben, anna.sessionId);
    const danaNow = await env.dana.ok('project/revision', { hostedRef: ref });
    assert.equal(danaNow.sessionId, anna.sessionId);
    const { m: dana } = await joinSession(env.dana, anna.sessionId);

    await ben.sendOk([ben.op(opText(1))]);
    await anna.drain();
    const save = await env.anna.call('project/save', {
      requestId: rid(),
      ...anna.ids(),
      seq: anna.cursor,
      files: checked([{ name: 'manifest', pieces: [MANIFEST] }, { name: 'f-north-base', pieces: ['{"Objects":[1]}'] }]),
      deleted: [],
    });
    assert.equal(save.ok, true, `first save of the session: ${JSON.stringify(save.error)}`);
    assert.equal(save.superseded, false);
    const after = await env.anna.ok('project/revision', { hostedRef: ref });
    assert.equal(after.hostedRevision, save.hostedRevision);

    await dana.leave();
    await env.anna.ok('session/end', anna.ids());
    const gone = await env.dana.ok('project/revision', { hostedRef: ref });
    assert.equal(gone.sessionId, undefined);
  });

  it('two users starting from the same head at once end up in one session', async (t) => {
    if (skip) return t.skip(skip);
    const ref = await project([{ name: 'manifest', content: MANIFEST }]);
    const head = await env.anna.ok('project/load', { hostedRef: ref });
    const body = (user: User) =>
      user.call('session/create', {
        requestId: rid(),
        client: CLIENT,
        projectName: head.projectName,
        map: head.map,
        kind: 'hosted',
        hostedRef: ref,
        hostedRevision: head.hostedRevision,
        instanceKey: instanceKey(),
        snapshotBytes: 10,
        snapshotParts: 1,
      });
    const [a, b] = await Promise.all([body(env.anna), body(env.ben)]);
    const created = [a, b].filter((r) => r.ok);
    assert.equal(created.length, 1);
    const other = [a, b].find((r) => !r.ok)!;
    assert.equal(other.error.code, 'already_running');
    assert.equal(other.error.sessionId, created[0].sessionId);
    const who = created[0] === a ? env.anna : env.ben;
    const m = new Member(who, created[0].sessionId, created[0].participantId);
    await uploadSnapshot(m, 0, '{"v":1,"s":1}');
    const res = await (created[0] === a ? env.ben : env.anna).ok('session/join', {
      requestId: rid(),
      client: CLIENT,
      sessionId: created[0].sessionId,
      instanceKey: instanceKey(),
    });
    assert.equal(res.mode, 'snapshot');
    await env.anna.ok('session/end', created[0] === a ? m.ids() : { sessionId: m.sessionId, participantId: res.participantId });
  });

  it('without capability hosted the project endpoints are capability_missing', async (t) => {
    if (env.info.capabilities.includes('hosted')) return t.skip('this server offers hosted');
    for (const ep of ['project/list', 'project/revision', 'project/load']) {
      await env.anna.err(ep, { hostedRef: { provider: 'x', key: 'y' } }, 'capability_missing');
    }
  });
});
