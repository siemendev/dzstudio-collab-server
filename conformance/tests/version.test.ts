import assert from 'node:assert/strict';
import { after, before, describe, it } from 'node:test';
import { instanceKey, rid, User, type Json } from '../support/client.js';
import { joinSession, opText, startLocal } from '../support/flows.js';
import { startEnv, type Env } from '../support/target.js';

const WORKSHOP = 'https://steamcommunity.com/sharedfiles/filedetails/?id=1';
const dz = (version?: unknown): Json => (version === undefined ? { name: 'dzstudio' } : { name: 'dzstudio', version });

function outdated(res: Json, yourVersion: string, minVersion = '0.4.0'): void {
  assert.equal(res.ok, false, `client_outdated for ${JSON.stringify(yourVersion)}`);
  assert.equal(res.error.code, 'client_outdated', JSON.stringify(res.error));
  assert.equal(res.error.minVersion, minVersion);
  assert.equal(res.error.yourVersion, yourVersion);
  assert.equal(res.error.updateUrl, WORKSHOP);
  assert.equal(res.token, undefined, 'no token');
}

describe('client version', () => {
  let env: Env;
  before(async () => {
    env = await startEnv();
  });
  after(async () => env.close());

  const createBody = (client?: Json): Json => ({
    protocol: '1.0',
    requestId: rid(),
    ...(client ? { client } : {}),
    projectName: 'P',
    map: 'chernarus',
    kind: 'local',
    instanceKey: instanceKey(),
    snapshotBytes: 2,
    snapshotParts: 1,
  });

  it('a server without a minimum names none of the fields and accepts any version', async (t) => {
    if (env.info.minClientVersion !== undefined) return t.skip('the server has a minimum client version');
    for (const k of ['minClientVersion', 'recommendedClientVersion', 'updateUrl']) assert.equal(env.info[k], undefined, k);
    const res = await env.anna.ok('session/create', createBody(dz('0.0.1')));
    await env.anna.ok('session/end', { sessionId: res.sessionId, participantId: res.participantId });
  });

  it('with minimum 0.4.0: older, malformed or missing versions are client_outdated before login, after the protocol', async (t) => {
    if (!env.spawnExtra) return t.skip('needs a server the suite starts (its config sets a minimum)');
    const server = await env.spawnExtra({ clientVersion: { min: '0.4.0', updateUrl: WORKSHOP } });
    try {
      const info = await server.t.call('info', {}, { method: 'GET' });
      assert.equal(info.minClientVersion, '0.4.0');
      assert.equal(info.updateUrl, WORKSHOP);
      assert.equal(info.recommendedClientVersion, undefined);

      outdated(await server.t.call('auth/name', { protocol: '1.0', requestId: rid(), name: 'Anna', client: dz('0.3.9') }), '0.3.9');
      outdated(await server.t.call('auth/name', { protocol: '1.0', requestId: rid(), name: 'Anna' }), '');
      outdated(await server.t.call('auth/code/start', { protocol: '1.0', client: dz('0.3.9') }), '0.3.9');
      const login = await server.t.call('auth/name', { protocol: '1.0', requestId: rid(), name: 'Anna', client: dz('0.4.0') });
      assert.equal(login.ok, true, JSON.stringify(login.error));
      const anna = new User(server.t, login.token, 'Anna');
      const benLogin = await server.t.call('auth/name', { protocol: '1.0', requestId: rid(), name: 'Ben', client: dz('0.10.0') });
      assert.equal(benLogin.ok, true, JSON.stringify(benLogin.error));
      const ben = new User(server.t, benLogin.token, 'Ben');

      for (const v of ['0.3.9', '0.4', '0.4.0-beta', '00.4.0']) outdated(await anna.call('session/create', createBody(dz(v))), v);
      outdated(await anna.call('session/create', createBody(dz())), '');
      outdated(await anna.call('session/create', createBody()), '');
      const listed = await anna.ok('sessions/list', {});
      assert.equal(listed.sessions.length, 0, 'no session came about');

      outdated(await server.t.call('session/create', { ...createBody(dz('0.3.9')), token: 'not-a-token' }), '0.3.9');
      const proto = await server.t.call('session/create', { ...createBody(dz('0.3.9')), protocol: '2.0', token: 'not-a-token' });
      assert.equal(proto.error.code, 'protocol_unsupported');

      const host = await startLocal(env, anna);
      outdated(await ben.call('session/join', { requestId: rid(), client: dz('0.3.9'), sessionId: host.sessionId, instanceKey: instanceKey() }), '0.3.9');
      const joined = await ben.ok('session/join', { requestId: rid(), client: dz('0.10.0'), sessionId: host.sessionId, instanceKey: instanceKey() });
      assert.equal(joined.mode, 'snapshot');
      const second = await anna.ok('session/create', createBody(dz('0.4.0')));
      await anna.ok('session/end', { sessionId: second.sessionId, participantId: second.participantId });
      await anna.ok('session/end', host.ids());
    } finally {
      await server.close();
    }
  });

  it('a minimum raised during a session never ends it; only a new join is client_outdated', async (t) => {
    if (!env.spawnExtra) return t.skip('needs a server the suite starts (test control _test/client-version)');
    const server = await env.spawnExtra({ clientVersion: { min: '0.4.0', recommended: '0.5.0', updateUrl: WORKSHOP } });
    try {
      const info = await server.t.call('info', {}, { method: 'GET' });
      assert.equal(info.recommendedClientVersion, '0.5.0');
      const login = async (name: string) => {
        const res = await server.t.call('auth/name', { protocol: '1.0', requestId: rid(), name, client: dz('0.4.0') });
        assert.equal(res.ok, true, JSON.stringify(res.error));
        return new User(server.t, res.token, name);
      };
      const [anna, ben] = [await login('Anna'), await login('Ben')];
      const host = await startLocal(env, anna);
      const { m: b } = await joinSession(ben, host.sessionId);

      const raised = await server.t.call('_test/client-version', { min: '0.5.0', updateUrl: WORKSHOP });
      assert.equal(raised.ok, true, JSON.stringify(raised));
      await b.sendOk([b.op(opText(1))]);
      await host.drain();
      const resumed = await ben.ok('session/join', {
        requestId: rid(),
        client: dz('0.4.0'),
        sessionId: host.sessionId,
        instanceKey: b.key,
        resume: { participantId: b.participantId, after: b.cursor },
      });
      assert.equal(resumed.mode, 'resume');
      await b.drain();
      outdated(await ben.call('session/join', { requestId: rid(), client: dz('0.4.0'), sessionId: host.sessionId, instanceKey: instanceKey() }), '0.4.0', '0.5.0');

      const off = await server.t.call('_test/client-version', {});
      assert.equal(off.ok, true);
      const plain = await server.t.call('info', {}, { method: 'GET' });
      assert.equal(plain.minClientVersion, undefined);
      await anna.ok('session/end', host.ids());
    } finally {
      await server.close();
    }
  });
});
