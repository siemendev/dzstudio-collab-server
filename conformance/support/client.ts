import assert from 'node:assert/strict';
import { createHash, randomInt } from 'node:crypto';

const MARKER = 'dzcollab 1\n';
export const RESPONSE_BYTES = 4_194_304;
export const MAX_OP_BYTES = 3_670_016;
export const PART_BYTES = 1_048_576;
const decoder = new TextDecoder('utf-8', { fatal: true });

export type Json = Record<string, any>;

export function rid(): string {
  const a = '0123456789abcdefghijklmnopqrstuvwxyz';
  let s = '';
  for (let i = 0; i < 16; i++) s += a[randomInt(a.length)];
  return s;
}

export function instanceKey(): string {
  return rid().slice(0, 10);
}

/** SHA-256 of a text in UTF-8, as 64 lower-case hex digits (the checksum of project/save). */
export function sha256(text: string): string {
  return createHash('sha256').update(text, 'utf8').digest('hex');
}

/** Files for project/save with the checksum over the concatenation of their pieces. */
export function checked<T extends { name: string; pieces: string[] }>(files: T[]): (T & { checksum: string })[] {
  return files.map((f) => ({ ...f, checksum: sha256(f.pieces.join('')) }));
}

/** Splits a text into pieces of at most 1000 UTF-8 bytes, only at character boundaries. */
export function pieces(text: string, max = 1000): string[] {
  const out: string[] = [];
  let cur = '';
  let bytes = 0;
  for (const ch of text) {
    const n = Buffer.byteLength(ch);
    if (bytes + n > max) {
      out.push(cur);
      cur = '';
      bytes = 0;
    }
    cur += ch;
    bytes += n;
  }
  if (cur || !out.length) out.push(cur);
  return out;
}

/** Size of a piece as the server writes it: with quotes, escaped as tightly as JSON allows. */
export function pieceBytes(p: string): number {
  return Buffer.byteLength(JSON.stringify(p));
}

export function piecesBytes(list: string[]): number {
  return list.reduce((n, p) => n + pieceBytes(p), 0);
}

/** Groups pieces into parts of at most maxBytes, counted as written. */
export function partsOf(list: string[], maxBytes = 1_048_576): string[][] {
  const parts: string[][] = [];
  let cur: string[] = [];
  let bytes = 0;
  for (const p of list) {
    const n = pieceBytes(p);
    if (bytes + n > maxBytes && cur.length) {
      parts.push(cur);
      cur = [];
      bytes = 0;
    }
    cur.push(p);
    bytes += n;
  }
  if (cur.length || !parts.length) parts.push(cur);
  return parts;
}

/** Checks the transport rules on every answer: HTTP 200, marker line, one JSON object, no text over 1000 bytes. */
export function checkTransport(status: number, raw: Buffer): Json {
  assert.equal(status, 200, 'every answer is HTTP 200');
  const text = decoder.decode(raw);
  assert.ok(text.startsWith(MARKER), `answer starts with the marker line, got ${JSON.stringify(text.slice(0, 40))}`);
  assert.ok(raw.length - Buffer.byteLength(MARKER) <= RESPONSE_BYTES, `answer is ${raw.length} bytes, over 4 MB`);
  const body = JSON.parse(text.slice(MARKER.length));
  assert.ok(body && typeof body === 'object' && !Array.isArray(body), 'answer is one JSON object');
  assert.equal(typeof body.ok, 'boolean', 'answer has ok');
  if (!body.ok) assert.equal(typeof body.error?.code, 'string', 'failed answer has error.code');
  walkTexts(body, '');
  return body;
}

function walkTexts(v: unknown, path: string): void {
  if (typeof v === 'string') {
    assert.ok(Buffer.byteLength(v) <= 1000, `text at ${path} is ${Buffer.byteLength(v)} bytes`);
    assert.ok(!/[\ud800-\udbff]$/.test(v), `text at ${path} ends inside a character`);
  } else if (Array.isArray(v)) v.forEach((x, i) => walkTexts(x, `${path}[${i}]`));
  else if (v && typeof v === 'object') for (const [k, x] of Object.entries(v)) walkTexts(x, `${path}.${k}`);
}

export class Transport {
  requests = 0;
  constructor(readonly base: string) {
    if (!this.base.endsWith('/')) this.base += '/';
  }

  async raw(endpoint: string, body: unknown, init: { method?: string; timeoutMs?: number; contentType?: string } = {}): Promise<{ status: number; raw: Buffer }> {
    this.requests++;
    const method = init.method ?? 'POST';
    const res = await fetch(this.base + endpoint, {
      method,
      body: method === 'GET' ? undefined : typeof body === 'string' ? body : JSON.stringify(body),
      headers: method === 'GET' ? undefined : { 'content-type': init.contentType ?? 'application/json' },
      signal: AbortSignal.timeout(init.timeoutMs ?? 10_000),
      redirect: 'manual',
    });
    return { status: res.status, raw: Buffer.from(await res.arrayBuffer()) };
  }

  async call(endpoint: string, body: unknown, init?: { method?: string; timeoutMs?: number; contentType?: string }): Promise<Json> {
    const { status, raw } = await this.raw(endpoint, body, init);
    return checkTransport(status, raw);
  }

  /** Sends a request and gives up before the answer arrives, as the game does after its timeout. */
  async fireAndDrop(endpoint: string, body: unknown, afterMs = 0): Promise<void> {
    const ctrl = new AbortController();
    const p = fetch(this.base + endpoint, { method: 'POST', body: JSON.stringify(body), signal: ctrl.signal }).catch(() => undefined);
    await new Promise((r) => setTimeout(r, afterMs));
    ctrl.abort();
    await p;
  }
}

export interface Versions {
  participants: number;
  locks: number;
  live: number;
  notices: number;
  saving: number;
}

/** One logged-in user talking to the server like DZSTUDIO does. */
export class User {
  constructor(
    readonly t: Transport,
    public token: string,
    readonly label: string,
  ) {}

  async call(endpoint: string, body: Json = {}): Promise<Json> {
    return this.t.call(endpoint, { protocol: '1.0', token: this.token, ...body });
  }

  async ok(endpoint: string, body: Json = {}): Promise<Json> {
    const res = await this.call(endpoint, body);
    assert.equal(res.ok, true, `${this.label} ${endpoint}: ${JSON.stringify(res.error)}`);
    return res;
  }

  async err(endpoint: string, body: Json, code: string): Promise<Json> {
    const res = await this.call(endpoint, body);
    assert.equal(res.ok, false, `${this.label} ${endpoint} should fail with ${code}`);
    assert.equal(res.error.code, code, `${this.label} ${endpoint}: ${JSON.stringify(res.error)}`);
    return res.error;
  }
}

/** The highest version there is, so the suite passes a server's minimum client version. */
export const CLIENT = { name: 'dzstudio', version: '999.999.999' };

/** A participant of one session with its own cursor, versions and running numbers. */
export class Member {
  cursor = 0;
  versions: Versions = { participants: 0, locks: 0, live: 0, notices: 0, saving: 0 };
  clientSeq = 1;
  lockSeq = 1;
  opCounter = 0;
  /** Everything received through poll, by seq: the pieces of each part. */
  log = new Map<
    number,
    { participantId: string; opId: string; system?: boolean; liveId?: string; chain?: { index: number; count: number }; count: number; parts: string[][] }
  >();
  last: Json = {};
  readonly key: string;

  constructor(
    readonly user: User,
    readonly sessionId: string,
    public participantId: string,
    key = instanceKey(),
  ) {
    this.key = key;
  }

  ids(): Json {
    return { sessionId: this.sessionId, participantId: this.participantId };
  }

  newOpId(): string {
    return `${this.key}-${++this.opCounter}`;
  }

  async poll(opts: { waitMs?: number; maxBytes?: number; presence?: Json; saving?: Json; fresh?: boolean } = {}): Promise<Json> {
    const body: Json = {
      ...this.ids(),
      after: this.cursor,
      waitMs: opts.waitMs ?? 0,
      versions: opts.fresh ? { participants: 0, locks: 0, live: 0, notices: 0, saving: 0 } : this.versions,
      presence: opts.presence ?? { position: [100.5, 20, 300.25], idleSeconds: 0 },
    };
    if (opts.maxBytes !== undefined) body.maxBytes = opts.maxBytes;
    if (opts.saving) body.saving = opts.saving;
    const res = await this.user.call('poll', body);
    if (res.ok) this.absorb(res);
    this.last = res;
    return res;
  }

  absorb(res: Json): void {
    for (const op of res.ops as Json[]) {
      assert.ok(op.seq > this.cursor || this.log.has(op.seq), 'ops come after the cursor');
      let entry = this.log.get(op.seq);
      if (!entry) {
        entry = {
          participantId: op.participantId,
          opId: op.opId,
          system: op.system,
          liveId: op.liveId,
          chain: op.chain,
          count: op.parts ?? 1,
          parts: [],
        };
        this.log.set(op.seq, entry);
      }
      entry.parts[op.part ?? 0] = op.pieces;
    }
    const seqs = [...this.log.keys()].sort((a, b) => a - b);
    for (const s of seqs) {
      const e = this.log.get(s)!;
      const complete = e.parts.filter((x) => Array.isArray(x)).length === e.count;
      if (s === this.cursor + 1 && complete) this.cursor = s;
    }
    for (const k of ['participants', 'locks', 'live', 'notices', 'saving'] as const) {
      if (res[k]?.version !== undefined) this.versions[k] = res[k].version;
    }
  }

  /** Polls with waitMs 0 until nothing new is left. */
  async drain(): Promise<Json> {
    let res: Json;
    let rounds = 0;
    do {
      res = await this.poll({ waitMs: 0 });
      assert.equal(res.ok, true, JSON.stringify(res.error));
    } while ((res.more || this.cursor < res.seq) && ++rounds < 500);
    return res;
  }

  op(text: string, extra: Json = {}): Json {
    return { clientSeq: this.clientSeq++, opId: this.newOpId(), pieces: pieces(text), ...extra };
  }

  async send(ops: Json[]): Promise<Json> {
    return this.user.call('ops/send', { ...this.ids(), ops });
  }

  async sendOk(ops: Json[]): Promise<Json> {
    const res = await this.send(ops);
    assert.equal(res.ok, true, JSON.stringify(res.error));
    return res;
  }

  async lock(elements: string[]): Promise<Json> {
    return this.user.ok('locks/set', { ...this.ids(), lockSeq: this.lockSeq++, elements });
  }

  async leave(): Promise<Json> {
    return this.user.ok('session/leave', this.ids());
  }

  text(seq: number): string {
    return this.log.get(seq)!.parts.flat().join('');
  }
}

export const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
