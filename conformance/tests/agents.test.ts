import assert from 'node:assert/strict';
import { after, before, describe, it } from 'node:test';
import { CLIENT, instanceKey, Member, pieces, rid, sleep, Transport, User, checked, type Json } from '../support/client.js';
import { downloadSnapshot, invite, joinSession, opText, startHosted, startLocal } from '../support/flows.js';
import { startEnv, type Env } from '../support/target.js';

const AGENT = { name: 'dzstudio-mcp', version: '0.0.1', kind: 'agent' };
const entryOf = (res: Json, pid: string) => res.participants?.list?.find((p: Json) => p.participantId === pid);
const presenceOf = (res: Json, pid: string) => res.presence.find((p: Json) => p.participantId === pid);

/** A participant that speaks for an AI agent: every poll carries phase and status. */
class AgentMember extends Member {
  phase = 'waiting';
  status = '';
  override async poll(opts: { waitMs?: number; maxBytes?: number; presence?: Json; saving?: Json; fresh?: boolean } = {}): Promise<Json> {
    return super.poll({ ...opts, presence: opts.presence ?? { idleSeconds: 0, phase: this.phase, status: this.status } });
  }
}

async function agentJoin(user: User, sessionId: string, client: Json = AGENT): Promise<{ a: AgentMember; join: Json }> {
  const key = instanceKey();
  const join = await user.ok('session/join', { requestId: rid(), client, sessionId, instanceKey: key });
  const a = new AgentMember(user, sessionId, join.participantId, key);
  await downloadSnapshot(a, join.snapshot.seq, join.snapshot.parts);
  a.cursor = join.snapshot.seq;
  await a.drain();
  return { a, join };
}

/** A code login, as the MCP does it; with agent the token is bound to an agent. */
async function codeLogin(t: Transport, user: string, password: string, client: Json): Promise<User> {
  const start = await t.call('auth/code/start', { protocol: '1.0', client });
  assert.equal(start.ok, true, JSON.stringify(start.error));
  const form = new URLSearchParams({ code: start.loginCode, user, password, action: 'confirm' });
  const page = await (await fetch(start.url, { method: 'POST', body: form, headers: { 'content-type': 'application/x-www-form-urlencoded' } })).text();
  assert.match(page, /Login confirmed/);
  if (client.kind === 'agent') assert.match(page, /AI agent/, 'the confirmation page says that an agent is logging in');
  if (client.agentName) assert.ok(page.includes(client.agentName), 'the confirmation page names the agent');
  const done = await t.call('auth/code/poll', { protocol: '1.0', loginCode: start.loginCode });
  assert.equal(done.state, 'confirmed');
  return new User(t, done.token, `${done.name} (agent)`);
}

describe('agents (capability agents)', () => {
  let env: Env;
  let skip = '';
  before(async () => {
    env = await startEnv();
    if (!env.info.capabilities.includes('agents')) skip = 'no agents capability';
  });
  after(async () => env.close());

  const hostedSkip = (): string => skip || (!env.info.capabilities.includes('hosted') || !env.hosted ? 'no hosted project for this target' : '');

  it('agent recognised: no rights, kind agent with its user name; it may not save, end, remove, release or invite', async (t) => {
    if (hostedSkip()) return t.skip(hostedSkip());
    const ref = await env.hosted!.ref();
    const anna = await startHosted(env, env.anna, ref);
    const before = await env.hosted!.revision(ref);
    await anna.lock(['k-1']);
    const { a, join } = await agentJoin(env.anna, anna.sessionId, { ...AGENT, agentName: 'Builder' });
    assert.equal(join.canSave, false);
    assert.equal(join.canManage, false);
    assert.equal(join.canInvite, false);
    const res = await anna.poll({ waitMs: 0 });
    const me = entryOf(res, anna.participantId);
    const agent = entryOf(res, a.participantId);
    assert.equal(me.kind, 'studio');
    assert.equal(agent.kind, 'agent');
    assert.equal(agent.name, me.name, "the agent carries its user's name");
    assert.equal(agent.userId, me.userId);
    assert.equal(agent.agentName, 'Builder');
    assert.equal(me.agentName, undefined);

    const denied: [string, Json][] = [
      ['project/save', { requestId: rid(), ...a.ids(), seq: a.cursor, files: checked([{ name: 'manifest', pieces: pieces('{"v":1}') }]), deleted: [] }],
      ['session/end', a.ids()],
      ['participant/remove', { ...a.ids(), target: anna.participantId }],
      ['locks/release', { requestId: rid(), ...a.ids(), target: anna.participantId }],
      ['invite/create', { requestId: rid(), ...a.ids() }],
    ];
    for (const [endpoint, body] of denied) await env.anna.err(endpoint, body, 'not_allowed');
    const after = await anna.poll({ waitMs: 0, fresh: true });
    assert.ok(entryOf(after, anna.participantId) && entryOf(after, a.participantId), 'nobody was removed');
    assert.deepEqual(after.locks.holders.find((h: Json) => h.participantId === anna.participantId)?.elements, ['k-1']);
    assert.equal(await env.hosted!.revision(ref), before, 'no new revision');
    await env.anna.err(
      'session/create',
      { requestId: rid(), client: AGENT, projectName: 'P', map: 'chernarus', kind: 'local', instanceKey: instanceKey(), snapshotBytes: 2, snapshotParts: 1 },
      'not_allowed',
    );
    await env.anna.ok('session/end', anna.ids());
  });

  it('token of an agent: a code login as agent makes every participant an agent; rights before existence', async (t) => {
    if (skip) return t.skip(skip);
    if (!env.spawned) return t.skip('needs the password of a configured user');
    for (const agentName of ['', 'two\nlines', 'x'.repeat(65)]) {
      const bad = await env.t.call('auth/code/start', { protocol: '1.0', client: { ...AGENT, agentName } });
      assert.equal(bad.error?.code, 'bad_request', `agentName ${JSON.stringify(agentName)}`);
    }
    const agentUser = await codeLogin(env.t, 'carla', 'carla-password', { ...AGENT, agentName: 'Scout' });
    const host = await startLocal(env);
    const code = await invite(host);
    await agentUser.ok('invite/redeem', { requestId: rid(), code });
    const { a, join } = await agentJoin(agentUser, host.sessionId, CLIENT);
    assert.equal(join.canSave, false);
    assert.equal(join.canInvite, false);
    assert.equal(entryOf(await host.poll({ waitMs: 0 }), a.participantId).kind, 'agent');
    for (const endpoint of ['project/save', 'session/end', 'participant/remove', 'locks/release', 'invite/create']) {
      await agentUser.err(endpoint, { requestId: rid(), sessionId: 'nosuchsession', participantId: 'p1', target: 'p2', seq: 0, files: [] }, 'not_allowed');
    }
    const person = await codeLogin(env.t, 'carla', 'carla-password', CLIENT);
    await person.ok('session/end', { sessionId: 'nosuchsession', participantId: 'p1' });
    await env.anna.ok('session/end', host.ids());
  });

  it('client kind: an unknown kind is bad_request, none is a person, resume keeps the kind, no version check for the MCP', async (t) => {
    if (skip) return t.skip(skip);
    const host = await startLocal(env);
    await env.ben.err('session/join', { requestId: rid(), client: { ...AGENT, kind: 'robot' }, sessionId: host.sessionId, instanceKey: instanceKey() }, 'bad_request');
    const { m: ben } = await joinSession(env.ben, host.sessionId);
    const { a } = await agentJoin(env.ben, host.sessionId);
    let res = await host.poll({ waitMs: 0 });
    assert.equal(entryOf(res, ben.participantId).kind, 'studio');
    assert.equal(entryOf(res, a.participantId).kind, 'agent');
    const resumed = await env.ben.ok('session/join', {
      requestId: rid(),
      client: { ...AGENT, kind: 'studio' },
      sessionId: host.sessionId,
      instanceKey: a.key,
      resume: { participantId: a.participantId, after: a.cursor },
    });
    assert.equal(resumed.canInvite, false, 'an agent stays an agent on returning');
    res = await host.poll({ waitMs: 0, fresh: true });
    assert.equal(entryOf(res, a.participantId).kind, 'agent');
    await env.anna.ok('session/end', host.ids());

    if (!env.spawnExtra) return;
    const server = await env.spawnExtra({ clientVersion: { min: '0.4.0' } });
    try {
      const login = await server.t.call('auth/name', { protocol: '1.0', requestId: rid(), name: 'Anna', client: AGENT });
      assert.equal(login.ok, true, 'the MCP is no dzStudio: no client_outdated');
      const created = await server.t.call('session/create', {
        protocol: '1.0',
        token: login.token,
        requestId: rid(),
        client: { name: 'dzstudio', version: '0.4.0' },
        projectName: 'P',
        map: 'chernarus',
        kind: 'local',
        instanceKey: instanceKey(),
        snapshotBytes: 2,
        snapshotParts: 1,
      });
      assert.equal(created.ok, true);
      await server.t.call('snapshot/put', { protocol: '1.0', token: login.token, sessionId: created.sessionId, participantId: created.participantId, seq: 0, part: 0, parts: 1, pieces: ['{}'] });
      const joined = await server.t.call('session/join', { protocol: '1.0', token: login.token, requestId: rid(), client: AGENT, sessionId: created.sessionId, instanceKey: instanceKey() });
      assert.equal(joined.ok, true, JSON.stringify(joined.error));
    } finally {
      await server.close();
    }
  });

  it('phase and status: passed on byte for byte, validated, waiting by default, never for a person, never waking a poll', async (t) => {
    if (skip) return t.skip(skip);
    const host = await startLocal(env);
    const { m: ben } = await joinSession(env.ben, host.sessionId);
    const key = instanceKey();
    const join = await env.anna.ok('session/join', { requestId: rid(), client: AGENT, sessionId: host.sessionId, instanceKey: key });
    const a = new AgentMember(env.anna, host.sessionId, join.participantId, key);
    a.cursor = join.snapshot.seq;
    await a.user.ok('poll', { ...a.ids(), after: a.cursor, waitMs: 0, versions: a.versions });
    let p = presenceOf(await host.poll({ waitMs: 0 }), a.participantId);
    assert.equal(p.phase, 'waiting');
    assert.equal(p.status, '');
    assert.equal(p.position, undefined, 'no position reported, none passed on');

    for (const bad of [
      { idleSeconds: 0 },
      { idleSeconds: 0, phase: 'busy' },
      { idleSeconds: 0, phase: 'working', status: 'x'.repeat(201) },
      { idleSeconds: 0, phase: 'working', status: 'line\nbreak' },
    ]) {
      const res = await a.poll({ presence: bad });
      assert.equal(res.error?.code, 'bad_request', JSON.stringify(bad));
    }

    await ben.drain();
    const waiting = ben.poll({ waitMs: 5000 });
    await sleep(300);
    const status = 'résumé'.repeat(24) + 'xxxxxxxx';
    assert.equal(Buffer.byteLength(status), 200);
    a.phase = 'working';
    a.status = status;
    await a.poll();
    const t0 = Date.now();
    const woke = await waiting;
    assert.ok(Date.now() - t0 > 4000, 'phase and status do not wake a waiting poll');
    for (const res of [woke, await ben.poll({ waitMs: 0 }), await host.poll({ waitMs: 0 })]) {
      const ap = presenceOf(res, a.participantId);
      assert.equal(ap.phase, 'working');
      assert.equal(ap.status, status);
    }

    await ben.poll({ presence: { position: [1, 2, 3], idleSeconds: 0, phase: 'working', status: 'person' } });
    const bp = presenceOf(await host.poll({ waitMs: 0 }), ben.participantId);
    assert.equal(bp.phase, undefined);
    assert.equal(bp.status, undefined);
    await env.anna.ok('session/end', host.ids());
  });

  it('idleness of an agent: reported idleSeconds 300 lapses its locks for anyone else', async (t) => {
    if (skip) return t.skip(skip);
    const host = await startLocal(env);
    const { a } = await agentJoin(env.anna, host.sessionId);
    await a.lock(['a-1', 'a-2', 'a-3']);
    await a.poll({ presence: { idleSeconds: 300, phase: 'working', status: '' } });
    const res = await host.poll({ waitMs: 0 });
    assert.equal(res.locks.holders.find((h: Json) => h.participantId === a.participantId).lapsed, true);
    const taken = await host.lock(['a-1', 'a-2', 'a-3']);
    assert.deepEqual([...taken.granted].sort(), ['a-1', 'a-2', 'a-3']);
    await env.anna.ok('session/end', host.ids());
  });

  it("own host: the agent joins its user's local session directly; an agent without rights does not", async (t) => {
    if (skip) return t.skip(skip);
    const host = await startLocal(env);
    const listed = await env.anna.ok('sessions/list', {});
    assert.equal(listed.sessions.find((s: Json) => s.sessionId === host.sessionId)?.access, 'direct');
    await agentJoin(env.anna, host.sessionId);
    await env.carla.err('session/join', { requestId: rid(), client: AGENT, sessionId: host.sessionId, instanceKey: instanceKey() }, 'not_allowed');
    await env.anna.ok('session/end', host.ids());
  });

  it('several sessions: one token, two participants, each with its own phase and status', async (t) => {
    if (skip) return t.skip(skip);
    const one = await startLocal(env);
    const two = await startLocal(env);
    const { a: a1 } = await agentJoin(env.anna, one.sessionId);
    const { a: a2 } = await agentJoin(env.anna, two.sessionId);
    assert.notEqual(a1.participantId + one.sessionId, a2.participantId + two.sessionId);
    a1.phase = 'working';
    a1.status = 'building at Krasno';
    await a1.poll();
    a2.phase = 'review';
    await a2.poll();
    const p1 = presenceOf(await one.poll({ waitMs: 0 }), a1.participantId);
    const p2 = presenceOf(await two.poll({ waitMs: 0 }), a2.participantId);
    assert.deepEqual([p1.phase, p1.status], ['working', 'building at Krasno']);
    assert.deepEqual([p2.phase, p2.status], ['review', '']);
    await env.anna.ok('session/end', one.ids());
    await env.anna.ok('session/end', two.ids());
  });

  it('no project by an agent; it starts a session of an existing project only with its user’s right to save, and never saves', async (t) => {
    if (hostedSkip()) return t.skip(hostedSkip());
    const create = (ref: Json, revision: string): Json => ({
      requestId: rid(),
      client: AGENT,
      projectName: 'Agent-Mod',
      map: 'livonia',
      kind: 'hosted',
      hostedRef: ref,
      hostedRevision: revision,
      instanceKey: instanceKey(),
      snapshotBytes: 7,
      snapshotParts: 1,
    });
    if (env.spawned) {
      const fresh = await env.hosted!.ref();
      await env.anna.err('session/create', create(fresh, ''), 'not_allowed');
      assert.equal(await env.hosted!.revision(fresh), '', 'no project came about');
      assert.equal((await env.anna.ok('sessions/list', { hostedRef: fresh })).sessions.length, 0);
      await env.anna.err('project/revision', { hostedRef: fresh }, 'project_unknown');
    }
    const ref = await env.hosted!.ref();
    if ((await env.hosted!.revision(ref)) === '') {
      if (!env.hosted!.edit) return t.skip('cannot create a project on this target');
      await env.hosted!.edit(ref, { files: [{ name: 'manifest', content: '{"v":1}' }], projectName: 'Agent-Mod', map: 'livonia' });
    }
    const revision = await env.hosted!.revision(ref);
    if (env.hosted!.joinOnly) await env.dana.err('session/create', create(ref, revision), 'not_allowed');

    const created = await env.anna.ok('session/create', create(ref, revision));
    assert.equal(created.role, 'participant');
    assert.equal(created.canSave, false);
    assert.equal(created.canManage, false);
    const a = new AgentMember(env.anna, created.sessionId, created.participantId);
    await env.anna.ok('snapshot/put', { ...a.ids(), seq: 0, part: 0, parts: 1, pieces: ['{"v":1}'] });
    await a.drain();
    await a.sendOk([a.op(opText(1))]);
    a.phase = 'review';
    await a.drain();
    await env.anna.err('project/save', { requestId: rid(), ...a.ids(), seq: a.cursor, files: checked([{ name: 'manifest', pieces: ['{"v":2}'] }]), deleted: [] }, 'not_allowed');
    await a.leave();

    const found = await env.anna.ok('sessions/list', { hostedRef: ref });
    assert.equal(found.sessions.length, 1, 'the work waits in the session');
    assert.equal(found.sessions[0].unsavedCount, 1);
    assert.equal(found.sessions[0].participants, 0);
    assert.equal(await env.hosted!.revision(ref), revision);
    const { m: anna, join } = await joinSession(env.anna, created.sessionId);
    assert.equal(join.canSave, true);
    const saved = await env.anna.ok('project/save', {
      requestId: rid(),
      ...anna.ids(),
      seq: anna.cursor,
      files: checked([{ name: 'manifest', pieces: ['{"v":2}'] }]),
      deleted: [],
    });
    env.hosted!.remember(ref, saved.hostedRevision);
    assert.notEqual(await env.hosted!.revision(ref), revision, 'the person saved');
    await env.anna.ok('session/end', anna.ids());
  });

  it('a user may always remove their own agent, also as a guest, without losing their invitation', async (t) => {
    if (skip) return t.skip(skip);
    const host = await startLocal(env);
    const code = await invite(host);
    await env.carla.ok('invite/redeem', { requestId: rid(), code });
    const { m: carla } = await joinSession(env.carla, host.sessionId);
    const { a: carlasAgent } = await agentJoin(env.carla, host.sessionId);
    const { a: annasAgent } = await agentJoin(env.anna, host.sessionId);
    await carla.drain();
    await host.drain();
    await env.carla.err('participant/remove', { ...carla.ids(), target: annasAgent.participantId }, 'not_allowed');
    await env.carla.err('participant/remove', { ...carla.ids(), target: host.participantId }, 'not_allowed');
    await env.carla.ok('participant/remove', { ...carla.ids(), target: carlasAgent.participantId });
    const gone = await carlasAgent.poll();
    assert.equal(gone.error.code, 'not_participant');
    assert.equal(gone.error.reason, 'removed');
    const res = await host.poll({ waitMs: 0 });
    const notice = res.notices.list.find((n: Json) => n.kind === 'participant_removed');
    assert.equal(notice.participantId, carlasAgent.participantId);
    assert.equal(entryOf(res, carlasAgent.participantId), undefined);
    await carla.leave();
    await joinSession(env.carla, host.sessionId);

    const { a: second } = await agentJoin(env.carla, host.sessionId);
    await env.anna.ok('participant/remove', { ...host.ids(), target: second.participantId });
    await env.carla.err('invite/redeem', { requestId: rid(), code }, 'invite_used');
    await env.anna.ok('session/end', host.ids());
  });

  it('server without the capability: info does not name it, kind is ignored, there are no agents', async (t) => {
    if (!env.spawnExtra) return t.skip('needs a server the suite starts with agents turned off');
    const server = await env.spawnExtra({ agents: { enabled: false } });
    try {
      const info = await server.t.call('info', {}, { method: 'GET' });
      assert.ok(!info.capabilities.includes('agents'));
      const login = async (name: string) => new User(server.t, (await server.t.call('auth/name', { protocol: '1.0', requestId: rid(), name, client: CLIENT })).token, name);
      const [anna, ben] = [await login('Anna'), await login('Ben')];
      const host = await startLocal(env, anna);
      const join = await ben.ok('session/join', { requestId: rid(), client: { ...AGENT, kind: 'robot' }, sessionId: host.sessionId, instanceKey: instanceKey() });
      assert.equal(join.canInvite, true, 'treated as a person');
      await ben.ok('poll', { sessionId: host.sessionId, participantId: join.participantId, after: 0, waitMs: 0, presence: { idleSeconds: 0, phase: 'busy' } });
      const res = await host.poll({ waitMs: 0 });
      for (const p of res.participants.list) assert.equal(p.kind, undefined);
      assert.equal(presenceOf(res, join.participantId).phase, undefined);
      await anna.ok('session/end', host.ids());
    } finally {
      await server.close();
    }
  });
});
