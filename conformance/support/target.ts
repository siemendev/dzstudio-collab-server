import assert from 'node:assert/strict';
import { spawn, type ChildProcess } from 'node:child_process';
import { openSync, closeSync } from 'node:fs';
import { mkdtemp, rm, writeFile } from 'node:fs/promises';
import { createServer } from 'node:net';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { CLIENT, rid, Transport, User, type Json } from './client.js';

/**
 * The server under test.
 *
 * - DZCOLLAB_SERVER=/path/to/binary: the suite starts the server itself with a config of its own (see
 *   referenceConfig) and can restart it. Every check runs.
 * - DZCOLLAB_TARGET='{"url": …, "tokens": …}': a server running elsewhere (see README); restart checks are skipped,
 *   clock and store checks run only if it offers the test controls.
 */
export interface Env {
  t: Transport;
  info: Json;
  /** Anna: starts sessions; on hosted projects may save and manage. */
  anna: User;
  /** Ben: may always join Anna's local sessions; on hosted projects may save. */
  ben: User;
  /** Carla: no rights anywhere; only comes in with invitations. */
  carla: User;
  /** Dana: on hosted projects may join (not save, not manage); no rights on Anna's local sessions. */
  dana: User;
  /** Further users without rights (for the colour test); may be fewer than asked for. */
  extra(n: number): Promise<User[]>;
  /** A user only for the throttling test (it is locked out of invite/redeem for a minute afterwards). */
  throwaway(): Promise<User | undefined>;
  /** Moves the server's clock forward and runs its time rules; missing on servers without test controls. */
  advance?: (ms: number) => Promise<void>;
  hosted?: {
    /** Name of the group file the hosted tests save (with the manifest); a remote store may want one it knows. */
    groupFile: string;
    /** Whether Dana may join the hosted project without saving it (not every operator has such a right). */
    joinOnly: boolean;
    /** A fresh project reference for a test (a spawned server gives each test its own project). */
    ref(): Promise<Json>;
    revision(ref: Json): Promise<string>;
    remember(ref: Json, revision: string): void;
    read?: (ref: Json) => Promise<{ revision: string; files: { name: string; pieces: string[] }[] } | null>;
    editOutside?: (ref: Json, name: string, content: string) => Promise<void>;
    /** Writes several files in place and sets name and map, creating the project if needed. */
    edit?: (ref: Json, change: { files?: { name: string; content: string }[]; projectName?: string; map?: string }) => Promise<void>;
  };
  /** Ends a session from the operator side; missing on servers without test controls. */
  endByServer?: (sessionId: string) => Promise<void>;
  /** Makes the server lose one complete attachment; missing on servers without test controls. */
  forgetAttachment?: (sessionId: string, checksum: string) => Promise<void>;
  /** True when the suite started the server itself (configured users and passwords are known). */
  spawned: boolean;
  /** Stops the spawned server (SIGTERM, or SIGKILL with hard) and starts it again on the same data. */
  restart?: (opts?: { hard?: boolean }) => Promise<void>;
  /** Starts a second, independent server with a changed config. */
  spawnExtra?: (patch: Json) => Promise<{ t: Transport; advance: (ms: number) => Promise<void>; close(): Promise<void> }>;
  close(): Promise<void>;
}

export const BASE_PATH = '/dzcollab/';

export function referenceConfig(dir: string, port: number): Json {
  return {
    server: { name: 'Conformance reference' },
    listen: { host: '127.0.0.1', port, basePath: BASE_PATH },
    dataDir: join(dir, 'data'),
    storage: { type: 'file', fsync: false },
    auth: {
      secret: 'conformance-secret-conformance-secret-0123',
      tokenTtlSeconds: 90 * 24 * 3600,
      name: { enabled: true },
      code: { enabled: true, expiresInSeconds: 600, intervalSeconds: 2 },
    },
    users: [{ id: 'carla', name: 'Carla', password: 'carla-password' }],
    permissions: {
      hostLocal: ['*'],
      localAlwaysJoin: { 'name:Anna': ['name:Ben'] },
      hosted: [{ provider: 'fs', key: '*', save: ['name:Anna', 'name:Ben'], manage: ['name:Anna'], join: ['name:Dana'] }],
    },
    directory: { mode: 'all' },
    inviteUser: true,
    hosted: { enabled: true, stores: [{ provider: 'fs', root: join(dir, 'projects'), map: 'chernarus' }] },
    agents: { enabled: true },
    limits: { maxParticipants: 10, checkpointOps: 40 },
    testing: { enabled: true },
  };
}

function merge(base: Json, patch: Json): Json {
  const out: Json = { ...base };
  for (const [k, v] of Object.entries(patch)) {
    out[k] = v && typeof v === 'object' && !Array.isArray(v) && base[k] && typeof base[k] === 'object' ? merge(base[k], v) : v;
  }
  return out;
}

async function freePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const srv = createServer();
    srv.once('error', reject);
    srv.listen(0, '127.0.0.1', () => {
      const addr = srv.address();
      const port = typeof addr === 'object' && addr ? addr.port : 0;
      srv.close(() => resolve(port));
    });
  });
}

async function nameLogin(t: Transport, name: string): Promise<User> {
  const res = await t.call('auth/name', { protocol: '1.0', requestId: rid(), name, client: CLIENT });
  assert.equal(res.ok, true, JSON.stringify(res.error));
  return new User(t, res.token, name);
}

export async function codeLogin(t: Transport, user: string, password: string): Promise<User> {
  const start = await t.call('auth/code/start', { protocol: '1.0', client: CLIENT });
  assert.equal(start.ok, true, JSON.stringify(start.error));
  const form = new URLSearchParams({ code: start.loginCode, user, password, action: 'confirm' });
  const page = await fetch(start.url, { method: 'POST', body: form, headers: { 'content-type': 'application/x-www-form-urlencoded' } });
  assert.match(await page.text(), /Login confirmed/);
  const done = await t.call('auth/code/poll', { protocol: '1.0', loginCode: start.loginCode });
  assert.equal(done.state, 'confirmed');
  return new User(t, done.token, done.name);
}

async function control(t: Transport, path: string, body: Json): Promise<Json> {
  const res = await t.call(path, body);
  assert.equal(res.ok, true, `${path}: ${JSON.stringify(res)}`);
  return res;
}

/** One running instance of the server binary. */
class Spawned {
  proc?: ChildProcess;
  readonly url: string;
  constructor(
    readonly binary: string,
    readonly dir: string,
    readonly configPath: string,
    readonly port: number,
  ) {
    this.url = `http://127.0.0.1:${port}${BASE_PATH}`;
  }

  async start(): Promise<void> {
    const fd = openSync(join(this.dir, 'server.log'), 'a');
    try {
      this.proc = spawn(this.binary, [this.configPath], { stdio: ['ignore', fd, fd] });
    } finally {
      closeSync(fd);
    }
    const proc = this.proc;
    let exited = false;
    proc.once('exit', () => (exited = true));
    const deadline = Date.now() + 10_000;
    for (;;) {
      if (exited) throw new Error(`server exited at start; see ${join(this.dir, 'server.log')}`);
      try {
        const res = await fetch(this.url + 'info', { signal: AbortSignal.timeout(500) });
        if (res.status === 200) return;
      } catch {
        // not up yet
      }
      if (Date.now() > deadline) throw new Error(`server did not answer info within 10 s; see ${join(this.dir, 'server.log')}`);
      await new Promise((r) => setTimeout(r, 50));
    }
  }

  async stop(hard = false): Promise<void> {
    const proc = this.proc;
    if (!proc || proc.exitCode !== null || proc.signalCode !== null) return;
    const done = new Promise<void>((resolve) => proc.once('exit', () => resolve()));
    proc.kill(hard ? 'SIGKILL' : 'SIGTERM');
    const timer = setTimeout(() => proc.kill('SIGKILL'), 10_000);
    await done;
    clearTimeout(timer);
  }
}

async function spawnServer(binary: string, patch: Json = {}): Promise<Spawned> {
  const dir = await mkdtemp(join(tmpdir(), 'dzcollab-conf-'));
  const port = await freePort();
  const configPath = join(dir, 'config.json');
  await writeFile(configPath, JSON.stringify(merge(referenceConfig(dir, port), patch), null, 2));
  const s = new Spawned(binary, dir, configPath, port);
  await s.start();
  return s;
}

export async function startEnv(): Promise<Env> {
  const binary = process.env.DZCOLLAB_SERVER;
  const remote = process.env.DZCOLLAB_TARGET;
  if (binary) return spawnedEnv(binary);
  if (remote) return remoteEnv(JSON.parse(remote));
  throw new Error(
    'Set DZCOLLAB_SERVER=/path/to/server-binary (the suite starts and restarts it) or ' +
      "DZCOLLAB_TARGET='{\"url\":…,\"tokens\":{…}}' (a server running elsewhere); see the README.",
  );
}

async function spawnedEnv(binary: string): Promise<Env> {
  const server = await spawnServer(binary);
  const t = new Transport(server.url);
  const info = await t.call('info', {}, { method: 'GET' });
  const [anna, ben, dana] = await Promise.all(['Anna', 'Ben', 'Dana'].map((n) => nameLogin(t, n)));
  const carla = await codeLogin(t, 'carla', 'carla-password');
  let extras = 0;
  const extraServers: Spawned[] = [];
  const env: Env = {
    t,
    info,
    anna,
    ben,
    carla,
    dana,
    spawned: true,
    async extra(n) {
      const out: User[] = [];
      for (let i = 0; i < n; i++) out.push(await nameLogin(t, `Gast ${++extras}`));
      return out;
    },
    async throwaway() {
      return nameLogin(t, `Wegwerf ${rid()}`);
    },
    advance: async (ms) => {
      await control(t, '_test/clock', { advanceMs: ms });
    },
    hosted: {
      groupFile: 'f-basis-nord',
      joinOnly: true,
      async ref() {
        return { provider: 'fs', key: `proj-${rid()}` };
      },
      async revision(ref) {
        const res = await control(t, '_test/store/revision', ref);
        return res.revision ?? '';
      },
      remember() {},
      async read(ref) {
        return (await control(t, '_test/store/read', ref)).project;
      },
      async editOutside(ref, name, content) {
        await control(t, '_test/store/edit', { ...ref, name, content });
      },
      async edit(ref, change) {
        await control(t, '_test/store/edit', { ...ref, ...change });
      },
    },
    endByServer: async (sessionId) => {
      await control(t, '_test/session/end', { sessionId });
    },
    forgetAttachment: async (sessionId, checksum) => {
      await control(t, '_test/attachment/forget', { sessionId, checksum });
    },
    async restart(opts = {}) {
      await server.stop(opts.hard);
      await server.start();
    },
    async spawnExtra(patch) {
      const s = await spawnServer(binary, patch);
      extraServers.push(s);
      const et = new Transport(s.url);
      return {
        t: et,
        advance: async (ms: number) => {
          await control(et, '_test/clock', { advanceMs: ms });
        },
        async close() {
          await s.stop();
          await rm(s.dir, { recursive: true, force: true });
        },
      };
    },
    async close() {
      await server.stop();
      for (const s of extraServers) await s.stop();
      if (!process.env.DZCOLLAB_KEEP) await rm(server.dir, { recursive: true, force: true });
    },
  };
  return env;
}

interface RemoteTarget {
  url: string;
  tokens: { anna: string; ben: string; carla: string; dana: string; throwaway?: string; extra?: string[] };
  hosted?: { ref: Json; revision: string; groupFile?: string; joinOnly?: boolean };
}

/**
 * A server elsewhere. If it offers the same test controls as the reference server (_test/clock, _test/session/end,
 * _test/store/*), the clock- and store-dependent checks run too; otherwise they are skipped.
 */
async function remoteEnv(target: RemoteTarget): Promise<Env> {
  const t = new Transport(target.url);
  const info = await t.call('info', {}, { method: 'GET' });
  const probe = await t.call('_test/clock', { advanceMs: 0 }).catch(() => ({ ok: false }));
  const controls = probe.ok === true;
  const storeControls =
    controls && target.hosted ? (await t.call('_test/store/revision', target.hosted.ref).catch(() => ({ ok: false }))).ok === true : false;
  let revision = target.hosted?.revision ?? '';
  const extra = target.tokens.extra ?? [];
  const env: Env = {
    t,
    info,
    anna: new User(t, target.tokens.anna, 'anna'),
    ben: new User(t, target.tokens.ben, 'ben'),
    carla: new User(t, target.tokens.carla, 'carla'),
    dana: new User(t, target.tokens.dana, 'dana'),
    spawned: false,
    async extra(n) {
      return extra.slice(0, n).map((tok, i) => new User(t, tok, `extra${i}`));
    },
    async throwaway() {
      return target.tokens.throwaway ? new User(t, target.tokens.throwaway, 'throwaway') : undefined;
    },
    hosted: target.hosted
      ? {
          groupFile: target.hosted.groupFile ?? 'f-basis-nord',
          joinOnly: target.hosted.joinOnly ?? true,
          async ref() {
            return target.hosted!.ref;
          },
          async revision(ref) {
            if (storeControls) return (await control(t, '_test/store/revision', ref)).revision ?? '';
            const asked: Json = await t.call('project/revision', { protocol: '1.0', token: target.tokens.anna, hostedRef: ref }).catch(() => ({ ok: false }));
            return asked.ok === true && typeof asked.hostedRevision === 'string' ? asked.hostedRevision : revision;
          },
          remember(_ref, rev) {
            revision = rev;
          },
        }
      : undefined,
    async close() {},
  };
  if (controls) {
    env.advance = async (ms) => {
      await control(t, '_test/clock', { advanceMs: ms });
    };
    env.endByServer = async (sessionId) => {
      await control(t, '_test/session/end', { sessionId });
    };
    const forget = await t.call('_test/attachment/forget', { sessionId: 'probe', checksum: 'probe' }).catch(() => ({ ok: false }));
    if (forget.ok === true) {
      env.forgetAttachment = async (sessionId, checksum) => {
        await control(t, '_test/attachment/forget', { sessionId, checksum });
      };
    }
  }
  if (storeControls && env.hosted) {
    env.hosted.read = async (ref) => (await control(t, '_test/store/read', ref)).project;
    env.hosted.editOutside = async (ref, name, content) => {
      await control(t, '_test/store/edit', { ...ref, name, content });
    };
  }
  return env;
}
