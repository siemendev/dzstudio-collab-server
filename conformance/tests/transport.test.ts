import assert from 'node:assert/strict';
import { after, before, describe, it } from 'node:test';
import { checkTransport, CLIENT, rid } from '../support/client.js';
import { startEnv, codeLogin, type Env } from '../support/target.js';

describe('transport, versions and login', () => {
  let env: Env;
  before(async () => {
    env = await startEnv();
  });
  after(async () => env.close());

  it('info answers GET and POST {} with the marker line and the required fields', async () => {
    const get = await env.t.call('info', {}, { method: 'GET' });
    const post = await env.t.call('info', {});
    for (const res of [get, post]) {
      assert.equal(res.ok, true);
      assert.ok(Buffer.byteLength(res.server.name) <= 64);
      assert.ok(res.protocols.some((p: any) => p.major === 1));
      assert.ok(Array.isArray(res.capabilities));
      assert.ok(res.auth.length >= 1, 'at least one login method');
      assert.ok(res.limits.maxRequestBytes >= 4_194_304);
      assert.ok(res.limits.maxWaitMs >= 8000);
      assert.ok(res.limits.maxParticipants >= 2);
      assert.equal(res.limits.liveIntervalMs, 200);
      assert.equal(res.limits.lockLapseSeconds, 300);
      assert.equal(res.limits.connectionGraceSeconds, 30);
      assert.equal(res.limits.hostGoneSeconds, 300);
      assert.equal(res.limits.maxOpBytes, 3_670_016);
      assert.equal(res.limits.maxChainOps, 16);
      for (const cap of ['auth.code', 'auth.name']) {
        if (res.capabilities.includes(cap)) assert.ok(res.auth.some((a: any) => a.method === cap.slice(5)));
      }
    }
  });

  it('every error is HTTP 200 with the marker line, also for unknown endpoints and bad bodies', async () => {
    const unknown = await env.t.call('no/such/endpoint', { protocol: '1.0' });
    assert.equal(unknown.error.code, 'bad_request');
    const notJson = await env.t.call('sessions/list', 'this is not json');
    assert.equal(notJson.error.code, 'bad_request');
    const getOther = await env.t.call('sessions/list', {}, { method: 'GET' });
    assert.equal(getOther.ok, false);
  });

  it('reads the body as JSON whatever the content type says', async () => {
    const res = await env.t.call('sessions/list', { protocol: '1.0', token: env.anna.token }, { contentType: 'text/plain' });
    assert.equal(res.ok, true);
    const none = await env.t.call('sessions/list', { protocol: '1.0', token: env.anna.token }, { contentType: 'application/x-www-form-urlencoded' });
    assert.equal(none.ok, true);
  });

  it('a newer minor version works; another major version is protocol_unsupported', async () => {
    assert.equal((await env.t.call('sessions/list', { protocol: '1.7', token: env.anna.token, futureField: { x: 1 } })).ok, true);
    const res = await env.t.call('sessions/list', { protocol: '2.0', token: env.anna.token });
    assert.equal(res.error.code, 'protocol_unsupported');
  });

  it('tokens: missing is auth_required, garbage is token_invalid', async () => {
    assert.equal((await env.t.call('sessions/list', { protocol: '1.0' })).error.code, 'auth_required');
    assert.equal((await env.t.call('sessions/list', { protocol: '1.0', token: 'nonsense' })).error.code, 'token_invalid');
  });

  it('a text over 1000 bytes in a request is refused', async () => {
    const res = await env.t.call('sessions/list', { protocol: '1.0', token: env.anna.token, note: 'ä'.repeat(501) });
    assert.equal(res.error.code, 'bad_request');
  });

  it('a request over maxRequestBytes gets too_large with maxBytes', async () => {
    const max = env.info.limits.maxRequestBytes as number;
    const body = JSON.stringify({ protocol: '1.0', token: env.anna.token, pad: 'x' }).padEnd(max + 10, ' ');
    const res = await env.t.call('sessions/list', body);
    assert.equal(res.error.code, 'too_large');
    assert.equal(res.error.maxBytes, max);
  });

  it('auth.name gives a token; the same requestId gives the same answer', async (t) => {
    if (!env.info.capabilities.includes('auth.name')) return t.skip('no auth.name');
    const requestId = rid();
    const a = await env.t.call('auth/name', { protocol: '1.0', requestId, name: 'Erik', client: CLIENT });
    const b = await env.t.call('auth/name', { protocol: '1.0', requestId, name: 'Erik', client: CLIENT });
    assert.equal(a.ok, true);
    assert.ok(/^[\x21-\x7e]+$/.test(a.token) && Buffer.byteLength(a.token) <= 1000, 'printable ASCII, at most 1000 bytes');
    assert.equal(b.token, a.token);
    assert.equal(a.name, 'Erik');
    const list = await env.t.call('sessions/list', { protocol: '1.0', token: a.token });
    assert.equal(list.ok, true);
  });

  it('auth.code: waiting until confirmed on the page, then the token; denied and expired', async (t) => {
    if (!env.info.capabilities.includes('auth.code') || !env.spawned || !env.advance) return t.skip('needs a spawned server with auth.code');
    const start = await env.t.call('auth/code/start', { protocol: '1.0', client: CLIENT });
    assert.match(start.loginCode, /^[ABCDEFGHJKLMNPQRSTUVWXYZ23456789]{8}$/);
    assert.ok(start.intervalSeconds >= 2);
    assert.equal((await env.t.call('auth/code/poll', { protocol: '1.0', loginCode: start.loginCode })).state, 'waiting');
    const page = await fetch(start.url);
    assert.equal(page.status, 200);
    const user = await codeLogin(env.t, 'carla', 'carla-password');
    assert.equal((await user.call('sessions/list')).ok, true);

    const denied = await env.t.call('auth/code/start', { protocol: '1.0', client: CLIENT });
    await fetch(denied.url, {
      method: 'POST',
      body: new URLSearchParams({ code: denied.loginCode, user: 'carla', password: 'carla-password', action: 'deny' }),
    });
    assert.equal((await env.t.call('auth/code/poll', { protocol: '1.0', loginCode: denied.loginCode })).state, 'denied');

    const late = await env.t.call('auth/code/start', { protocol: '1.0', client: CLIENT });
    await env.advance!(start.expiresInSeconds * 1000 + 1000);
    assert.equal((await env.t.call('auth/code/poll', { protocol: '1.0', loginCode: late.loginCode })).state, 'expired');
  });

  it('checkTransport rejects answers without the marker line or with cut JSON', () => {
    assert.throws(() => checkTransport(200, Buffer.from('<html>proxy error</html>')));
    assert.throws(() => checkTransport(200, Buffer.from('dzcollab 1\n{"ok":tr')));
    assert.throws(() => checkTransport(502, Buffer.from('dzcollab 1\n{"ok":true}')));
  });
});
