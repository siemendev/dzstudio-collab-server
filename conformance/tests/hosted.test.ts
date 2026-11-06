import assert from 'node:assert/strict';
import { after, before, describe, it } from 'node:test';
import { CLIENT, instanceKey, pieces, rid, sleep, type Json, checked } from '../support/client.js';
import { invite, joinSession, opText, redeemAndJoin, startHosted, startLocal, STATE_TEXT } from '../support/flows.js';
import { startEnv, type Env } from '../support/target.js';

describe('hosted sessions (capability hosted)', () => {
  let env: Env;
  let skip = '';
  before(async () => {
    env = await startEnv();
    if (!env.info.capabilities.includes('hosted')) skip = 'no hosted capability';
    else if (!env.hosted) skip = 'no hosted project configured for this target';
  });
  after(async () => env.close());
  const group = (): string => env.hosted?.groupFile ?? 'f-north-base';

  const files = (n: number) =>
    checked([
      { name: group(), pieces: pieces(JSON.stringify({ Objects: [{ name: 'Land_Barrel', pos: [n, 0, 0] }], note: 'Café' })) },
      { name: 'manifest', pieces: pieces(`{"v":1,"n":${n}}`) },
    ]);

  async function save(m: { user: any; ids(): Json; cursor: number }, extra: Json = {}): Promise<Json> {
    return m.user.call('project/save', { requestId: rid(), ...m.ids(), seq: m.cursor, files: files(m.cursor), deleted: [], ...extra });
  }

  it('only who may save starts a session; a second start for the same project is already_running', async (t) => {
    if (skip) return t.skip(skip);
    const ref = await env.hosted!.ref();
    const body = (user: string) => ({
      requestId: rid(),
      client: CLIENT,
      projectName: 'Server-Mod',
      map: 'livonia',
      kind: 'hosted',
      hostedRef: ref,
      hostedRevision: '',
      instanceKey: instanceKey(),
      snapshotBytes: 10,
      snapshotParts: 1,
      note: `from ${user}`,
    });
    await env.carla.err('session/create', body('carla'), 'not_allowed');
    await env.dana.err('session/create', body('dana'), 'not_allowed');
    const anna = await startHosted(env, env.anna, ref);
    const e = await env.ben.err('session/create', { ...body('ben'), hostedRevision: await env.hosted!.revision(ref) }, 'already_running');
    assert.equal(e.sessionId, anna.sessionId);
    const found = await env.ben.ok('sessions/list', { hostedRef: ref });
    assert.deepEqual(found.sessions.map((s: Json) => s.sessionId), [anna.sessionId]);
    assert.equal(found.sessions[0].kind, 'hosted');
    assert.deepEqual(found.sessions[0].hostedRef, ref);
    assert.equal(found.sessions[0].host, undefined);
    await env.anna.ok('session/end', anna.ids());
  });

  it('rights: Ben may save, Dana may join and invite but not save, Carla by code is a guest; nobody is host', async (t) => {
    if (skip) return t.skip(skip);
    const ref = await env.hosted!.ref();
    const anna = await startHosted(env, env.anna, ref);
    const ben = await joinSession(env.ben, anna.sessionId);
    assert.equal(ben.join.role, 'participant');
    assert.equal(ben.join.canSave, true);
    assert.equal(ben.join.canManage, false);
    assert.equal(ben.join.canInvite, true);
    const dana = env.hosted!.joinOnly ? await joinSession(env.dana, anna.sessionId) : undefined;
    if (dana) {
      assert.equal(dana.join.canSave, false);
      assert.equal(dana.join.canInvite, true);
    }
    const carla = await redeemAndJoin(env.carla, await invite(dana?.m ?? ben.m), anna.sessionId);
    const list = (await carla.poll({ waitMs: 0, fresh: true })).participants.list;
    assert.ok(list.every((p: Json) => p.role === 'participant'));
    const c = list.find((p: Json) => p.participantId === carla.participantId);
    assert.deepEqual([c.canInvite, c.canSave, c.canManage], [false, false, false]);
    const e = await save(dana?.m ?? carla);
    assert.equal(e.error.code, 'not_allowed');
    await env.anna.ok('session/end', anna.ids());
    if (!dana) t.diagnostic('the target has no user who may only join; Dana was left out');
  });

  it('the creator leaves: the session goes on without a host, and the others keep working', async (t) => {
    if (skip) return t.skip(skip);
    const ref = await env.hosted!.ref();
    const anna = await startHosted(env, env.anna, ref);
    const ben = (await joinSession(env.ben, anna.sessionId)).m;
    const dana = env.hosted!.joinOnly
      ? (await joinSession(env.dana, anna.sessionId)).m
      : await redeemAndJoin(env.carla, await invite(ben), anna.sessionId);
    await anna.leave();
    await ben.sendOk([ben.op(opText(1))]);
    const res = await dana.drain();
    assert.equal(res.ok, true);
    assert.equal(dana.log.size, 1);
    assert.ok(!res.participants || res.participants.list.every((p: Json) => p.role !== 'host'));
    await env.anna.err('session/end', { sessionId: anna.sessionId, participantId: ben.participantId }, 'not_participant');
    await env.ben.err('session/end', ben.ids(), 'not_allowed');
    const anna2 = (await joinSession(env.anna, anna.sessionId)).m;
    await env.anna.ok('session/end', anna2.ids());
  });

  it('save: files land in one step, savedSeq and unsavedCount follow; older is superseded; repeated save writes once', async (t) => {
    if (skip) return t.skip(skip);
    const ref = await env.hosted!.ref();
    const anna = await startHosted(env, env.anna, ref);
    const ben = (await joinSession(env.ben, anna.sessionId)).m;
    await ben.sendOk([ben.op(opText(1)), ben.op(opText(2))]);
    await anna.sendOk([anna.op(opText(3), { system: true })]);
    await ben.drain();
    await anna.drain();

    if (env.hosted!.read) assert.equal(await env.hosted!.read(ref), null, 'nothing written before the first save');
    const requestId = rid();
    const body = { requestId, ...ben.ids(), seq: ben.cursor, files: files(ben.cursor), deleted: [] };
    const first = await env.ben.ok('project/save', body);
    const second = await env.ben.ok('project/save', body);
    assert.deepEqual(second, first, 'the repetition gets the stored answer');
    assert.equal(first.superseded, false);
    assert.equal(first.savedSeq, ben.cursor);
    assert.equal(typeof first.hostedRevision, 'string');
    env.hosted!.remember(ref, first.hostedRevision);
    if (env.hosted!.read) {
      const stored = await env.hosted!.read(ref);
      assert.equal(stored!.revision, first.hostedRevision);
      assert.deepEqual(stored!.files.map((f) => f.name).sort(), [group(), 'manifest'].sort());
      assert.equal(stored!.files.find((f) => f.name === group())!.pieces.join(''), files(ben.cursor)[0].pieces.join(''));
    }
    let s = (await anna.poll({ waitMs: 0 })).saving;
    assert.equal(s.state, 'saved');
    assert.equal(s.savedSeq, ben.cursor);
    assert.equal(s.unsavedCount, 0);
    assert.equal(s.savedBy.name, 'Ben');

    const savedAt = ben.cursor;
    await anna.sendOk([anna.op(opText(4)), anna.op(opText(5), { system: true }), anna.op(opText(6))]);
    await anna.drain();
    s = anna.last.saving ?? (await anna.poll({ waitMs: 0 })).saving;
    assert.equal(s.unsavedCount, 2, 'only operations after savedSeq without system');
    assert.equal(s.state, 'unsaved');

    const older = await env.ben.ok('project/save', { requestId: rid(), ...ben.ids(), seq: savedAt - 1, files: files(1), deleted: [] });
    assert.equal(older.superseded, true);
    assert.equal(older.savedSeq, savedAt);
    const tooNew = await env.ben.call('project/save', { requestId: rid(), ...ben.ids(), seq: anna.cursor + 50, files: files(1) });
    assert.equal(tooNew.error.code, 'not_confirmed');
    await env.anna.ok('session/end', anna.ids());
  });

  it('new project: project/list names the stores with canCreate; the first save at seq 0 writes the project', async (t) => {
    if (skip) return t.skip(skip);
    if (!env.spawned) return t.skip('needs a store that creates projects under a new key');
    const listed = await env.anna.ok('project/list', {});
    const store = listed.stores.find((s: Json) => s.provider === 'fs');
    assert.equal(store?.canCreate, true);
    assert.equal((await env.dana.ok('project/list', {})).stores.find((s: Json) => s.provider === 'fs')?.canCreate, false);
    const ref = await env.hosted!.ref();
    assert.equal(await env.hosted!.revision(ref), '');
    const anna = await startHosted(env, env.anna, ref, { projectName: 'Server Base' });
    assert.equal(anna.cursor, 0);
    const first = await env.anna.ok('project/save', { requestId: rid(), ...anna.ids(), seq: 0, files: files(0), deleted: [] });
    assert.equal(first.superseded, false);
    assert.equal(first.savedSeq, 0);
    assert.notEqual(first.hostedRevision, '');
    const head = await env.anna.ok('project/revision', { hostedRef: ref });
    assert.equal(head.hostedRevision, first.hostedRevision);
    assert.equal(head.projectName, 'Server Base');
    assert.equal((await anna.poll({ waitMs: 0 })).saving.savedBy.name, 'Anna');
    const again = await env.anna.ok('project/save', { requestId: rid(), ...anna.ids(), seq: 0, files: files(0), deleted: [] });
    assert.equal(again.superseded, true);
    assert.equal(again.hostedRevision, first.hostedRevision);
    await env.anna.ok('session/end', anna.ids());
  });

  it('a save wakes every waiting poll', async (t) => {
    if (skip) return t.skip(skip);
    const ref = await env.hosted!.ref();
    const anna = await startHosted(env, env.anna, ref);
    const ben = (await joinSession(env.ben, anna.sessionId)).m;
    await ben.sendOk([ben.op(opText(1))]);
    await ben.drain();
    await anna.drain();
    const waiting = anna.poll({ waitMs: 8000 });
    await sleep(200);
    const t0 = Date.now();
    const res = await save(ben);
    assert.equal(res.ok, true);
    env.hosted!.remember(ref, res.hostedRevision);
    const w = await waiting;
    assert.ok(Date.now() - t0 < 1000);
    assert.ok(w.saving, 'woken by the save');
    const s = (await anna.poll({ waitMs: 0 })).saving ?? w.saving;
    assert.equal(s.savedBy.name, 'Ben');
    await env.anna.ok('session/end', anna.ids());
  });

  it('changed from outside: store_changed names the file and writes nothing; force writes', async (t) => {
    if (skip) return t.skip(skip);
    if (!env.hosted!.editOutside) return t.skip('cannot change the store from outside on this target');
    const ref = await env.hosted!.ref();
    const anna = await startHosted(env, env.anna, ref);
    await anna.sendOk([anna.op(opText(1))]);
    await anna.drain();
    const first = await save(anna);
    assert.equal(first.ok, true);
    await env.hosted!.editOutside(ref, group(), '{"Objects":[],"by":"workbench"}');
    await anna.sendOk([anna.op(opText(2))]);
    await anna.drain();
    const res = await save(anna);
    assert.equal(res.error.code, 'store_changed');
    assert.deepEqual(res.error.changed, [group()]);
    const stored = await env.hosted!.read!(ref);
    assert.equal(stored!.files.find((f) => f.name === group())!.pieces.join(''), '{"Objects":[],"by":"workbench"}');
    const forced = await save(anna, { force: true });
    assert.equal(forced.ok, true);
    const after = await env.hosted!.read!(ref);
    assert.equal(after!.files.find((f) => f.name === group())!.pieces.join(''), files(anna.cursor)[0].pieces.join(''));
    await env.anna.ok('session/end', anna.ids());
  });

  it('save leaves files outside both lists alone and deletes what deleted names', async (t) => {
    if (skip) return t.skip(skip);
    if (!env.hosted!.editOutside) return t.skip('cannot look into the store on this target');
    const ref = await env.hosted!.ref();
    await env.hosted!.editOutside(ref, 'unreadable.xml', '<not readable/>');
    await env.hosted!.editOutside(ref, 'alt', 'old');
    const anna = await startHosted(env, env.anna, ref);
    await anna.sendOk([anna.op(opText(1))]);
    await anna.drain();
    const res = await save(anna, { deleted: ['alt'] });
    assert.equal(res.ok, true, JSON.stringify(res.error));
    const stored = await env.hosted!.read!(ref);
    const names = stored!.files.map((f) => f.name).sort();
    assert.deepEqual(names, [group(), 'manifest', 'unreadable.xml'].sort());
    assert.equal(stored!.files.find((f) => f.name === 'unreadable.xml')!.pieces.join(''), '<not readable/>');
    assert.ok(res.revisions.every((r: Json) => typeof r.revision === 'string'));
    await env.anna.ok('session/end', anna.ids());
  });

  it('everyone leaves with unsaved changes: the session stays findable with 0 participants; save and leave ends it', async (t) => {
    if (skip) return t.skip(skip);
    const ref = await env.hosted!.ref();
    const anna = await startHosted(env, env.anna, ref);
    await anna.sendOk([anna.op(opText(1)), anna.op(opText(2)), anna.op(opText(3))]);
    await anna.leave();
    const found = await env.ben.ok('sessions/list', { hostedRef: ref });
    assert.equal(found.sessions.length, 1);
    assert.equal(found.sessions[0].participants, 0);
    assert.equal(found.sessions[0].unsavedCount, 3);
    const ben = (await joinSession(env.ben, anna.sessionId)).m;
    assert.equal(ben.log.size, 3, 'the unsaved changes are there');
    const res = await save(ben);
    assert.equal(res.ok, true);
    env.hosted!.remember(ref, res.hostedRevision);
    await ben.leave();
    const gone = await env.ben.ok('sessions/list', { hostedRef: ref });
    assert.deepEqual(gone.sessions, []);
  });

  it('discard: session/end with canManage ends it for all with discarded and by; the store keeps the last save', async (t) => {
    if (skip) return t.skip(skip);
    const ref = await env.hosted!.ref();
    const anna = await startHosted(env, env.anna, ref);
    const ben = (await joinSession(env.ben, anna.sessionId)).m;
    await ben.sendOk([ben.op(opText(1))]);
    const before = env.hosted!.read ? await env.hosted!.read(ref) : undefined;
    await ben.drain();
    const waiting = ben.poll({ waitMs: 8000 });
    await sleep(200);
    await env.anna.ok('session/end', anna.ids());
    const res = await waiting;
    assert.equal(res.error.code, 'session_ended');
    assert.equal(res.error.reason, 'discarded');
    assert.equal(res.error.by, 'Anna');
    if (env.hosted!.read) assert.deepEqual(await env.hosted!.read(ref), before);
    const gone = await env.ben.ok('sessions/list', { hostedRef: ref });
    assert.deepEqual(gone.sessions, []);
  });

  it('a hosted session with unsaved changes survives a restart of the server', async (t) => {
    if (skip) return t.skip(skip);
    if (!env.restart) return t.skip('cannot restart this target');
    const ref = await env.hosted!.ref();
    const anna = await startHosted(env, env.anna, ref, { text: STATE_TEXT });
    await anna.sendOk([anna.op(opText(1)), anna.op(opText(2)), anna.op(opText(3))]);
    await anna.drain();
    await anna.leave();
    await env.restart();
    const found = await env.ben.ok('sessions/list', { hostedRef: ref });
    assert.equal(found.sessions.length, 1);
    assert.equal(found.sessions[0].participants, 0);
    assert.equal(found.sessions[0].unsavedCount, 3);
    const ben = await joinSession(env.ben, anna.sessionId);
    assert.equal(ben.snapshot, STATE_TEXT);
    assert.deepEqual([...ben.m.log.values()].map((e) => e.parts.flat().join('')), [opText(1), opText(2), opText(3)]);
    const res = await save(ben.m);
    assert.equal(res.ok, true, JSON.stringify(res.error));
    await ben.m.leave();
    assert.deepEqual((await env.ben.ok('sessions/list', { hostedRef: ref })).sessions, []);
  });

  it('a local session can be resumed after a restart', async (t) => {
    if (!env.restart) return t.skip('cannot restart this target');
    const host = await startLocal(env);
    const ben = (await joinSession(env.ben, host.sessionId)).m;
    await ben.sendOk([ben.op(opText(1))]);
    await ben.drain();
    await env.restart();
    const res = await env.ben.ok('session/join', {
      requestId: rid(),
      client: CLIENT,
      sessionId: host.sessionId,
      instanceKey: ben.key,
      resume: { participantId: ben.participantId, after: ben.cursor },
    });
    assert.equal(res.mode, 'resume');
    const next = await ben.sendOk([ben.op(opText(2))]);
    assert.equal(next.results[0].clientSeq, 2);
    await host.drain();
    assert.equal(host.log.size, 2);
    await env.anna.ok('session/end', host.ids());
  });
});
