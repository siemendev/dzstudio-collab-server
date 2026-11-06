import assert from 'node:assert/strict';
import { CLIENT, instanceKey, Member, partsOf, pieces, rid, type Json, type User } from './client.js';
import type { Env } from './target.js';

export const STATE_TEXT = '{"v":1,"head":{"types":{"core:object":1}},"groups":[{"id":"k3f9a0x2mq-2","name":"Basis Nord"}]}';

export interface StartOptions {
  text?: string;
  projectName?: string;
  map?: string;
  requires?: Json[];
  firstPoll?: boolean;
}

export async function uploadSnapshot(m: Member, seq: number, text: string): Promise<void> {
  const parts = partsOf(pieces(text));
  for (let i = 0; i < parts.length; i++) {
    const res = await m.user.ok('snapshot/put', { ...m.ids(), seq, part: i, parts: parts.length, pieces: parts[i] });
    assert.equal(res.complete, i === parts.length - 1);
  }
}

export async function downloadSnapshot(m: Member, seq: number, parts: number): Promise<{ text: string; pieces: string[][] }> {
  const all: string[][] = [];
  for (let i = 0; i < parts; i++) {
    const res = await m.user.ok('snapshot/get', { ...m.ids(), seq, part: i });
    assert.equal(res.seq, seq);
    assert.equal(res.part, i);
    assert.equal(res.parts, parts);
    all.push(res.pieces);
  }
  return { text: all.flat().join(''), pieces: all };
}

export async function startLocal(env: Env, host: User = env.anna, opts: StartOptions = {}): Promise<Member> {
  const text = opts.text ?? STATE_TEXT;
  const parts = partsOf(pieces(text));
  const key = instanceKey();
  const res = await host.ok('session/create', {
    requestId: rid(),
    client: CLIENT,
    projectName: opts.projectName ?? 'Basis Nord',
    map: opts.map ?? 'chernarus',
    kind: 'local',
    requires: opts.requires,
    instanceKey: key,
    snapshotBytes: Buffer.byteLength(text),
    snapshotParts: parts.length,
  });
  assert.equal(res.role, 'host');
  assert.equal(res.canManage, true);
  assert.equal(res.canSave, false);
  const m = new Member(host, res.sessionId, res.participantId, key);
  await uploadSnapshot(m, 0, text);
  if (opts.firstPoll !== false) await m.drain();
  return m;
}

/**
 * On a target that is not spawned the hosted tests share one project: a session a failed test left behind would
 * make every later start already_running, so it is discarded first.
 */
async function discardLeftover(env: Env, user: User, ref: Json): Promise<void> {
  if (env.spawned) return;
  const found = await user.call('sessions/list', { hostedRef: ref });
  for (const s of (found.sessions ?? []) as Json[]) {
    const join = await user.call('session/join', { requestId: rid(), client: CLIENT, sessionId: s.sessionId, instanceKey: instanceKey() });
    if (join.ok) await user.call('session/end', { sessionId: s.sessionId, participantId: join.participantId });
  }
}

export async function startHosted(env: Env, user: User, ref: Json, opts: StartOptions = {}): Promise<Member> {
  await discardLeftover(env, user, ref);
  const text = opts.text ?? STATE_TEXT;
  const parts = partsOf(pieces(text));
  const key = instanceKey();
  const res = await user.ok('session/create', {
    requestId: rid(),
    client: CLIENT,
    projectName: opts.projectName ?? 'Server-Mod',
    map: opts.map ?? 'livonia',
    kind: 'hosted',
    hostedRef: ref,
    hostedRevision: await env.hosted!.revision(ref),
    instanceKey: key,
    snapshotBytes: Buffer.byteLength(text),
    snapshotParts: parts.length,
  });
  assert.equal(res.role, 'participant');
  assert.ok(res.capabilities.includes('hosted'));
  const m = new Member(user, res.sessionId, res.participantId, key);
  await uploadSnapshot(m, 0, text);
  if (opts.firstPoll !== false) await m.drain();
  return m;
}

export async function joinSession(user: User, sessionId: string, opts: { firstPoll?: boolean } = {}): Promise<{ m: Member; join: Json; snapshot: string }> {
  const key = instanceKey();
  const join = await user.ok('session/join', { requestId: rid(), client: CLIENT, sessionId, instanceKey: key });
  assert.equal(join.mode, 'snapshot');
  const m = new Member(user, sessionId, join.participantId, key);
  const snap = await downloadSnapshot(m, join.snapshot.seq, join.snapshot.parts);
  m.cursor = join.snapshot.seq;
  if (opts.firstPoll !== false) await m.drain();
  return { m, join, snapshot: snap.text };
}

export async function invite(host: Member, forUserId?: string): Promise<string> {
  const res = await host.user.ok('invite/create', { requestId: rid(), ...host.ids(), forUserId });
  return res.code;
}

export async function redeemAndJoin(user: User, code: string, sessionId: string): Promise<Member> {
  const r = await user.ok('invite/redeem', { requestId: rid(), code });
  assert.equal(r.sessionId, sessionId);
  return (await joinSession(user, sessionId)).m;
}

/** Text of one generated operation (opaque to the server). */
export function opText(n: number, filler = ''): string {
  return `{"v":1,"type":"core:set","n":${n}${filler ? `,"src":${JSON.stringify(filler)}` : ''}}`;
}

export function needsClock(env: Env, t: { skip(msg?: string): void }): boolean {
  if (env.advance) return true;
  t.skip('server has no clock control (only the reference server under test has)');
  return false;
}

/** The server's userId for a user: start a throwaway session and read the own participant entry. */
export async function whoAmI(env: Env, user: User): Promise<string> {
  const m = await startLocal(env, user, { firstPoll: false });
  const res = await m.poll({ waitMs: 0 });
  const me = res.participants.list.find((p: Json) => p.participantId === m.participantId);
  await user.ok('session/end', m.ids());
  return me.userId;
}

export function needsRestart(env: Env, t: { skip(msg?: string): void }): boolean {
  if (env.restart) return true;
  t.skip('cannot restart this target (only a server the suite spawns)');
  return false;
}

export function needsHosted(env: Env, t: { skip(msg?: string): void }): boolean {
  if (env.info.capabilities.includes('hosted') && env.hosted) return true;
  t.skip(env.info.capabilities.includes('hosted') ? 'no hosted project configured for this target' : 'no hosted capability');
  return false;
}

/** Splits text into k chain links of at most maxBytes each (counted as written), at piece boundaries. */
export function chainLinks(text: string, maxBytes = 3_670_016): string[][] {
  const links: string[][] = [];
  let cur: string[] = [];
  let bytes = 0;
  for (const p of pieces(text)) {
    const n = Buffer.byteLength(JSON.stringify(p));
    if (bytes + n > maxBytes && cur.length) {
      links.push(cur);
      cur = [];
      bytes = 0;
    }
    cur.push(p);
    bytes += n;
  }
  if (cur.length) links.push(cur);
  return links;
}

/** The ops/send items of one chain link: a single item, or its parts if it is over 1 MB. */
export function linkItems(clientSeq: number, opId: string, index: number, count: number, linkPieces: string[], extra: Json = {}): Json[] {
  const parts = partsOf(linkPieces);
  const chain = { index, count };
  if (parts.length === 1) return [{ clientSeq, opId, chain, pieces: parts[0], ...extra }];
  return parts.map((p, i) => ({ clientSeq, opId, chain, part: i, parts: parts.length, pieces: p, ...extra }));
}

/** ASCII text with some umlauts, `bytes` UTF-8 bytes long (opaque to the server). */
export function bigText(bytes: number, seed = 'x'): string {
  const unit = `${seed}-Größe-`;
  const unitBytes = Buffer.byteLength(unit);
  let text = unit.repeat(Math.floor(bytes / unitBytes));
  text += 'x'.repeat(bytes - Buffer.byteLength(text));
  return text;
}
