import assert from 'node:assert/strict';
import { after, before, describe, it } from 'node:test';
import { CLIENT, instanceKey, rid } from '../support/client.js';
import { startEnv, type Env } from '../support/target.js';

describe('token_expired', () => {
  let env: Env;
  before(async () => {
    env = await startEnv();
  });
  after(async () => env.close());

  it('a new token and the same requestId repeat the request without a second effect', async (t) => {
    if (!env.spawnExtra) return t.skip('needs a server the suite starts (short token lifetime)');
    const server = await env.spawnExtra({ auth: { tokenTtlSeconds: 120 } });
    try {
      const login = async () => (await server.t.call('auth/name', { protocol: '1.0', requestId: rid(), name: 'Anna' })).token as string;
      const token = await login();
      const body = {
        protocol: '1.0',
        requestId: rid(),
        client: CLIENT,
        projectName: 'P',
        map: 'chernarus',
        kind: 'local',
        instanceKey: instanceKey(),
        snapshotBytes: 2,
        snapshotParts: 1,
      };
      const first = await server.t.call('session/create', { ...body, token });
      assert.equal(first.ok, true);
      await server.advance(121_000);
      const expired = await server.t.call('session/create', { ...body, token });
      assert.equal(expired.error.code, 'token_expired');
      const again = await server.t.call('session/create', { ...body, token: await login() });
      assert.equal(again.sessionId, first.sessionId);
      assert.equal(again.participantId, first.participantId);
      const list = await server.t.call('sessions/list', { protocol: '1.0', token: await login() });
      assert.equal(list.ok, true);
    } finally {
      await server.close();
    }
  });
});
