import assert from 'node:assert/strict';
import { after, before, describe, it } from 'node:test';
import { CLIENT, instanceKey, Member, partsOf, pieces, rid, type Json } from '../support/client.js';
import { downloadSnapshot, invite, joinSession, redeemAndJoin, startLocal, STATE_TEXT, uploadSnapshot } from '../support/flows.js';
import { startEnv, type Env } from '../support/target.js';

describe('sessions: create, snapshot, join, lists, colours', () => {
  let env: Env;
  before(async () => {
    env = await startEnv();
  });
  after(async () => env.close());

  it('the same session/create twice (answer lost) makes one session', async () => {
    const requestId = rid();
    const key = instanceKey();
    const body = {
      requestId,
      client: CLIENT,
      projectName: 'Doppelt',
      map: 'chernarus',
      kind: 'local',
      instanceKey: key,
      snapshotBytes: STATE_TEXT.length,
      snapshotParts: 1,
    };
    await env.t.fireAndDrop('session/create', { protocol: '1.0', token: env.anna.token, ...body });
    const a = await env.anna.ok('session/create', body);
    const b = await env.anna.ok('session/create', body);
    assert.equal(a.sessionId, b.sessionId);
    assert.equal(a.participantId, b.participantId);
    await env.anna.ok('session/end', { sessionId: a.sessionId, participantId: a.participantId });
  });

  it('until snapshot 0 is complete the session is in no list and join says snapshot_not_ready', async () => {
    const text = 'ü'.repeat(600_000);
    const parts = partsOf(pieces(text));
    assert.ok(parts.length >= 2);
    const key = instanceKey();
    const res = await env.anna.ok('session/create', {
      requestId: rid(),
      client: CLIENT,
      projectName: 'Big',
      map: 'chernarus',
      kind: 'local',
      instanceKey: key,
      snapshotBytes: Buffer.byteLength(text),
      snapshotParts: parts.length,
    });
    const host = new Member(env.anna, res.sessionId, res.participantId, key);
    await env.anna.ok('snapshot/put', { ...host.ids(), seq: 0, part: 0, parts: parts.length, pieces: parts[0] });
    const list = await env.ben.ok('sessions/list');
    assert.ok(!list.sessions.some((s: Json) => s.sessionId === host.sessionId));
    await env.ben.err('session/join', { requestId: rid(), client: CLIENT, sessionId: host.sessionId, instanceKey: instanceKey() }, 'snapshot_not_ready');
    const again = await env.anna.ok('snapshot/put', { ...host.ids(), seq: 0, part: 0, parts: parts.length, pieces: parts[0] });
    assert.equal(again.complete, false, 'the same part twice changes nothing');
    await uploadSnapshotRest(host, parts);
    const listed = await env.ben.ok('sessions/list');
    assert.ok(listed.sessions.some((s: Json) => s.sessionId === host.sessionId));
    await env.anna.ok('session/end', host.ids());
  });

  async function uploadSnapshotRest(host: Member, parts: string[][]) {
    for (let i = 1; i < parts.length; i++) {
      await host.user.ok('snapshot/put', { ...host.ids(), seq: 0, part: i, parts: parts.length, pieces: parts[i] });
    }
  }

  it('a 5 MB snapshot with multi-byte characters goes up and down in parts and comes back identical, piece for piece', async () => {
    const text = JSON.stringify({ v: 1, src: 'résumé naïve éèç ñ € 𝄞 '.repeat(230_000) }).slice(0, 5_300_000);
    const host = await startLocal(env, env.anna, { text });
    const { m, join } = await joinSession(env.ben, host.sessionId, { firstPoll: false });
    assert.ok(join.snapshot.bytes >= 5_000_000);
    const down = await downloadSnapshot(m, join.snapshot.seq, join.snapshot.parts);
    assert.equal(down.text, text);
    const expected = partsOf(pieces(text));
    assert.deepEqual(down.pieces, expected, 'same pieces, same boundaries');
    await env.anna.ok('session/end', host.ids());
  });

  it('who may join: Ben directly (listed as direct), Carla not listed and not allowed', async () => {
    const host = await startLocal(env, env.anna, { projectName: 'Café South', map: 'chernarus', requires: [{ type: 'core:object', version: 1 }] });
    const ben = await env.ben.ok('sessions/list');
    const entry = ben.sessions.find((s: Json) => s.sessionId === host.sessionId);
    assert.ok(entry, 'Ben sees the session');
    assert.equal(entry.access, 'direct');
    assert.equal(entry.kind, 'local');
    assert.equal(entry.projectName, 'Café South');
    assert.equal(entry.map, 'chernarus');
    assert.equal(entry.participants, 1);
    assert.deepEqual(entry.requires, [{ type: 'core:object', version: 1 }]);
    assert.equal(typeof entry.host.userId, 'string');
    const carla = await env.carla.ok('sessions/list');
    assert.ok(!carla.sessions.some((s: Json) => s.sessionId === host.sessionId));
    await env.carla.err('session/join', { requestId: rid(), client: CLIENT, sessionId: host.sessionId, instanceKey: instanceKey() }, 'not_allowed');

    const { m, join, snapshot } = await joinSession(env.ben, host.sessionId);
    assert.equal(join.role, 'guest');
    assert.equal(join.kind, 'local');
    assert.equal(join.canInvite, true);
    assert.equal(join.canSave, false);
    assert.equal(join.canManage, false);
    assert.equal(snapshot, STATE_TEXT);
    assert.notEqual(join.colour, 1);
    await m.leave();
    await env.anna.ok('session/end', host.ids());
  });

  it('the map key travels byte for byte, also one nobody knows', async () => {
    const host = await startLocal(env, env.anna, { map: 'deer_isle_Ünknown' });
    const list = await env.ben.ok('sessions/list');
    assert.equal(list.sessions.find((s: Json) => s.sessionId === host.sessionId).map, 'deer_isle_Ünknown');
    const code = await invite(host);
    const r = await env.carla.ok('invite/redeem', { requestId: rid(), code });
    assert.equal(r.map, 'deer_isle_Ünknown');
    await env.anna.ok('session/end', host.ids());
  });

  it('instance keys are unique within a session', async () => {
    const host = await startLocal(env);
    await env.ben.err('session/join', { requestId: rid(), client: CLIENT, sessionId: host.sessionId, instanceKey: host.key }, 'instance_key_taken');
    await env.anna.ok('session/end', host.ids());
  });

  it('the same session/join twice gives the same participant; a joiner who never polls is never seen', async () => {
    const host = await startLocal(env);
    const body = { requestId: rid(), client: CLIENT, sessionId: host.sessionId, instanceKey: instanceKey() };
    const a = await env.ben.ok('session/join', body);
    const b = await env.ben.ok('session/join', body);
    assert.equal(a.participantId, b.participantId);
    const seen = await host.poll({ waitMs: 0 });
    assert.ok(!(seen.participants?.list ?? []).some((p: Json) => p.participantId === a.participantId));
    assert.ok(!seen.presence.some((p: Json) => p.participantId === a.participantId));
    const list = await env.ben.ok('sessions/list');
    assert.equal(list.sessions.find((s: Json) => s.sessionId === host.sessionId).participants, 1);
    await env.ben.ok('session/leave', { sessionId: host.sessionId, participantId: a.participantId });
    const after = await host.poll({ waitMs: 0 });
    assert.ok(!(after.participants?.list ?? []).some((p: Json) => p.participantId === a.participantId));
    await env.anna.ok('session/end', host.ids());
  });

  it('a joiner appears in the participant list with its first poll, in join order', async () => {
    const host = await startLocal(env);
    const { m } = await joinSession(env.ben, host.sessionId);
    const res = await host.poll({ waitMs: 0 });
    assert.deepEqual(
      res.participants.list.map((p: Json) => p.participantId),
      [host.participantId, m.participantId],
    );
    const anna = res.participants.list[0];
    assert.equal(anna.role, 'host');
    assert.equal(anna.canManage, true);
    assert.equal(anna.name, 'Anna');
    await env.anna.ok('session/end', host.ids());
  });

  it('colours: nine participants get 1–8 and then 1; after the one with 3 leaves, the next gets 3', async (t) => {
    if (env.info.limits.maxParticipants < 10) return t.skip('server allows fewer than 10 participants');
    const guests = await env.extra(9);
    if (guests.length < 9) return t.skip('not enough users for this test');
    const host = await startLocal(env);
    const members: Member[] = [host];
    const colours = [1];
    for (let i = 0; i < 8; i++) {
      const code = await invite(host);
      await guests[i].ok('invite/redeem', { requestId: rid(), code });
      const { m, join } = await joinSession(guests[i], host.sessionId);
      members.push(m);
      colours.push(join.colour);
    }
    assert.deepEqual(colours, [1, 2, 3, 4, 5, 6, 7, 8, 1]);
    await members[2].leave();
    const code = await invite(host);
    const last = await redeemAndJoin(guests[8], code, host.sessionId);
    const res = await host.poll({ waitMs: 0 });
    assert.equal(res.participants.list.find((p: Json) => p.participantId === last.participantId).colour, 3);
    await env.anna.ok('session/end', host.ids());
  });

  it('session_full when maxParticipants are in', async (t) => {
    const max = env.info.limits.maxParticipants as number;
    const guests = await env.extra(max);
    if (guests.length < max) return t.skip('not enough users for this test');
    const host = await startLocal(env);
    for (let i = 0; i < max - 1; i++) {
      const code = await invite(host);
      await redeemAndJoin(guests[i], code, host.sessionId);
    }
    const code = await invite(host);
    await guests[max - 1].ok('invite/redeem', { requestId: rid(), code });
    await guests[max - 1].err('session/join', { requestId: rid(), client: CLIENT, sessionId: host.sessionId, instanceKey: instanceKey() }, 'session_full');
    await env.anna.ok('session/end', host.ids());
  });

  it('unknown sessions are session_unknown', async () => {
    await env.ben.err('session/join', { requestId: rid(), client: CLIENT, sessionId: 'nope', instanceKey: instanceKey() }, 'session_unknown');
    const poll = await env.ben.call('poll', { sessionId: 'nope', participantId: 'p1', after: 0, waitMs: 0 });
    assert.ok(['session_unknown', 'not_allowed'].includes(poll.error?.code), 'poll may answer not_allowed (rights before existence)');
  });

  it('users/list names available users with a state, if offered', async (t) => {
    if (!env.info.capabilities.includes('users')) return t.skip('no users capability');
    const res = await env.anna.ok('users/list');
    assert.ok(res.users.length <= 200);
    for (const u of res.users) {
      assert.equal(typeof u.userId, 'string');
      assert.ok(['online', 'offline', 'in_session'].includes(u.state));
    }
  });
});
