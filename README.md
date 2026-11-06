# dzstudio-collab-server

The reference server for collaborative editing in **DZSTUDIO**, the in-game object editor for DayZ. It speaks the
open protocol **dzcollab 1** ([PROTOCOL.md](PROTOCOL.md)): DZSTUDIO's "Edit together" talks to it so that several
people edit one project at the same time.

The server orders the operations of a session and hands them to every participant byte for byte; it never reads an
operation and never checks a precondition — every client applies the log in seq order and reaches the same state.
Around that it holds who is in a session, presence, locks, live states while dragging, invitations, checkpoints and,
for projects that live on the server, listing, loading and saving them.

- Go, standard library only, one static binary; a 10 MB container image.
- Everything the protocol requires, plus every optional part behind its capability: `users`, `invite.user`,
  `hosted` (with `project/list`, `project/revision`, `project/load`), `auth.code`, `auth.name`, `agents`.
- AI agents as participants (`agents`): an agent marks itself with `client.kind: "agent"` (or logs in as one with the
  code login, which binds its token), shows phase and status to everyone, and never saves, manages, invites, hosts or
  creates a project; its user may always remove it.
- Attachments (`attachment/put`, `attachment/get`, `attachment/want`): the text of loot files travels in parts up to
  16 MB, survives a restart and is asked again from its uploader (or everybody) with the notice `attachment_wanted`.
- Save checksums: every file of `project/save` carries its SHA-256; the server checks it over the bytes
  that arrived and again over the bytes it stores, writes nothing on a difference (`checksum_mismatch`) and names
  revision and checksum per file in the answer.
- Built for the game's limits: every answer is HTTP 200 with the line `dzcollab 1` and one JSON object, errors in the
  body, no text over 1000 bytes, no answer over 4 MB, operations up to 3.5 MiB and larger steps as chains, long poll
  answering within its wait time, idempotent retries (requestId, clientSeq, lockSeq, liveId + n, seq + part) — also
  across a restart.
- The core knows no operator. Accounts, rights, the log store and the project store are ports with default adapters
  for self-hosting; another operator (dzbot) plugs in its own without touching the core.
- A conformance suite (`conformance/`, TypeScript) that drives any server over HTTP exactly like a client.

Open points in the protocol and how this server reads them: [QUESTIONS.md](QUESTIONS.md).

## Run it

```bash
go install github.com/siemendev/dzstudio-collab-server/cmd/dzcollab@latest   # or, from a checkout:
go build -o bin/dzcollab ./cmd/dzcollab      # or: make build
bin/dzcollab                                 # defaults: port 8080, base address /dzcollab/, login by name, data in ./data
bin/dzcollab config.json                     # or DZCOLLAB_CONFIG=config.json bin/dzcollab
```

Check it: `curl http://127.0.0.1:8080/dzcollab/info` (or `bin/dzcollab health [config.json]`, exit code 0 if it
answers).

In DZSTUDIO, add the server under Settings › Edit together › "Add server …" with its base address,
e.g. `https://collab.example.org/dzcollab/`. With `hosted.enabled` its projects appear in the project browser under a
tab with the server's name; opening one always opens it in a session.

### Docker

```bash
docker build -t dzstudio-collab-server .     # or: make docker
docker run -d -p 8080:8080 -v dzcollab-data:/data dzstudio-collab-server
# with a config file:
docker run -d -p 8080:8080 -v dzcollab-data:/data -v $PWD/config.json:/config.json:ro \
  -e DZCOLLAB_CONFIG=/config.json dzstudio-collab-server
```

The image is distroless (static, no shell), runs as user `nonroot`, keeps everything under `/data` and has a health
check (`/dzcollab health`, which reads the same `DZCOLLAB_CONFIG`). Put hosted projects under `/data/projects` (see
the example config).

### Tests

```bash
make test        # go vet, Go unit tests with -race, then the conformance suite against a freshly built binary
make unit        # only the Go tests
make conformance # only the suite (needs Node.js 24 or newer)
```

The suite starts the binary itself in a temporary directory, restarts it (also with SIGKILL) for the restart checks
and takes about 20 seconds.

## Configuration

A JSON file on top of the defaults; relative paths are relative to the file. See
[config.example.json](config.example.json). Environment variables `DZCOLLAB_PORT`, `DZCOLLAB_DATA_DIR` and
`DZCOLLAB_PUBLIC_URL` override the file.

| Key | Default | Meaning |
| --- | --- | --- |
| `server.name` | `DZSTUDIO collaboration server` | Name shown in DZSTUDIO's server list (≤ 64 bytes) |
| `server.notice` | — | Operator notice shown in DZSTUDIO's settings (≤ 1000 bytes) |
| `listen.host`, `listen.port` | `0.0.0.0`, `8080` | Where to listen |
| `listen.basePath` | `/dzcollab/` | Path of the base address; endpoints are below it |
| `publicUrl` | derived from the request | Public base address, used for the login page link |
| `trustProxy` | `false` | Trust `X-Forwarded-*` from a reverse proxy |
| `dataDir` | `./data` | Session log, retry window, snapshots, token secret |
| `storage.type` | `file` | `file` (survives restarts) or `memory` |
| `storage.fsync` | `false` | fsync every write (slower, safe against power loss; without it a crash of the process loses nothing, a crash of the machine may) |
| `auth.name.enabled` | `true` | Login with a display name only (`auth.name`) — for servers in your own network |
| `auth.name.allowedNames` | any | Restrict name logins to these names |
| `auth.code.enabled` | `false` | Login with a code confirmed on the server's page with user + password (`auth.code`) |
| `auth.code.expiresInSeconds`, `auth.code.intervalSeconds` | 600, 3 | Lifetime of a login code; poll interval (≥ 2) |
| `auth.tokenTtlSeconds` | 30 days | Lifetime of tokens |
| `auth.secret` | generated into `dataDir/token-secret` | Secret for signing tokens (≥ 32 characters) |
| `users` | `[]` | `{id, name, password}`; ids `A–Z a–z 0–9 _ -`, not starting with `n-` |
| `permissions.hostLocal` | `["*"]` | Who may start local sessions |
| `permissions.localAlwaysJoin` | `{}` | Host → users who may always join that host's local sessions without an invitation |
| `permissions.hosted` | `[]` | Rules `{provider, key ("*" = all), join, save, manage, invite}` for hosted projects |
| `directory.mode` | `all` | `users/list`: `all` (configured users and everyone seen in the last day), `friends` (`directory.friends`), `none` (capability off) |
| `inviteUser` | `true` | Capability `invite.user` |
| `hosted.enabled` | `false` | Capability `hosted` |
| `agents.enabled` | `true` | Capability `agents`: AI agents may take part as marked participants without rights to save, manage or invite |
| `hosted.stores` | `[{provider: "fs", root: "./projects"}]` | Filesystem project stores, by `hostedRef.provider`; `map` is the map key of projects whose map is not recorded |
| `limits.maxRequestBytes` | 8 MiB (min 4 MiB) | Largest request, also the largest `project/save` |
| `limits.maxParticipants` | 8 (min 2) | Per session |
| `limits.maxSnapshotBytes` | 64 MiB | Largest project state |
| `limits.checkpointOps`, `limits.checkpointBytes` | 1000, 8 MiB | Ask for a checkpoint when the log after the snapshot is larger |
| `limits.maxLocalSessionsPerUser`, `limits.maxHostedSessionsPerUser` | no limit | Sessions one user may have started and not ended, per kind (`not_allowed` above) |
| `allowedClients` | any | Only accept these `client.name` values |
| `clientVersion.min` | — (no check) | Lowest DZSTUDIO version (`0.4.0`) for `auth/name`, `auth/code/start`, `session/create` and `session/join` without `resume`; older, missing or malformed answers `client_outdated`. Running sessions never end on it |
| `clientVersion.recommended` | — | Recommended version, named in `info` only when above `min` |
| `clientVersion.updateUrl` | — | DZSTUDIO's Workshop page (≤ 1000 bytes), in `info` and `client_outdated` |
| `testing.enabled` | `false` | Test controls below `_test/` — **never on a server people use** |

`maxOpBytes` (3 670 016) and `maxChainOps` (16) are fixed by the protocol and not configurable.

Principals in permission lists: `"*"` (every logged-in user), a `userId`, or `"name:<display name>"`. A name login
gets the userId `n-` + 20 hex digits of the name's SHA-256 (or the id of a configured user without password who has
that name), so permissions for name logins are usually written as `name:Anna`. Who has `join` on a hosted project sees
it in `project/list` and may load it; only who has `save` opens it without a running session. Who has `save` in a
rule with `"key": "*"` may also create new projects in that store (`project/list` › `stores` › `canCreate`).

### Hosted projects in the filesystem store

```
<root>/<key>                       link to the current version, one file per project file
<root>/.versions/<key>/r<N>/       the files of version N
<root>/.versions/<key>/r<N>.json   per file its SHA-256, size and revision
<root>/.meta/<key>.json            {"projectName": "…", "map": "chernarus"} — name and map of the project
```

A save builds a complete new version and switches the link in one step. A plain directory you put at `<root>/<key>`
becomes version `r0`; a file changed in place there is a change from outside — the next look at the project takes it
over as a new version, and the session's next save answers `store_changed`. A version replaced by a save stays
loadable for at least 10 minutes (`project/load` of a file is checked against the SHA-256 of its version, so content
changed in place answers `revision_unknown`). Without `.meta/<key>.json` the project is listed with its key as name and
the store's `map`; the first save of a new project from a session records the session's name and map.

## Architecture: ports and adapters

```
          httpapi                       200 + marker line, body limit, routes, extensions (login page, test controls)
             │
         core.Service                   every protocol rule; knows only the ports below
   ┌──────────┬────┴──────┬──────────────┬───────────────┬───────────────┬───────┐
 Identity  Permission  UserDirectory  SessionStorage  AttachmentStorage  ProjectStore  Clock
 Provider    Policy                                                     (+ SessionHooks)
```

| Package | Content |
| --- | --- |
| `protocol` | JSON writer (raw non-ASCII, tight escapes, float32 text, 1000-byte texts), request reader, error codes |
| `core` | the ports (`ports.go`) and `Service`: sessions, operations and chains, poll, locks, live states, invitations, hosted save and projects, time rules, persistence |
| `adapters/memstore`, `adapters/filestore` | `SessionStorage` in memory, or in memory plus append-only files; `filestore.Attachments` keeps attachment parts in the session directories |
| `adapters/fsprojects` | `ProjectStore` on disk (above) |
| `adapters/localidentity` | users from the config, signed tokens, `auth.name`, `auth.code` with its confirmation page |
| `adapters/configperms` | `PermissionPolicy` and `UserDirectory` from the config |
| `httpapi`, `config`, `reference`, `cmd/dzcollab` | HTTP layer, config file, wiring with the test controls, the command |

| Port | What the core asks | Default adapter |
| --- | --- | --- |
| `IdentityProvider` (+ optional `NameLogin`, `CodeLogin`, `AgentTokens`) | login methods for `info`; token → user, invalid or expired; `auth/name`, `auth/code/*`; whether a token is bound to an agent | `localidentity` |
| `PermissionPolicy` | may host local sessions; rights `{Join, Save, Manage, Invite}` of a user on a local project (by host) or a hosted one (by `hostedRef`) | `configperms` |
| `UserDirectory` | who is available for `users/list` (friends, people with standing rights) | `configperms` |
| `SessionStorage` | records, log append/read/truncate, snapshot parts, staged parts and chain links, small values (last lock answer), the retry window | `memstore`, `filestore` |
| `AttachmentStorage` | put, get and drop the parts of an attachment by session and checksum | `filestore.Attachments` (in memory without it) |
| `ProjectStore` (+ optional `UserProjectLister`, `UserScopedStore`) | current revision, changes since a revision, save all-or-nothing with checksums, list (or list for one user with rights), head of a revision, read one file of a revision | `fsprojects` |
| `SessionHooks` (optional) | a hosted session is starting (may refuse), a hosted session ended | — |
| `Clock` | milliseconds | system clock with a test offset |

What stays in the core and is not up to an adapter: seq numbering, batches and chains, retries and the 10-minute
retry window, cursors, locks and their lapse, live states, presence, invitations and their codes, colours,
checkpoints, the time rules (30 s, 300 s, 10 minutes), paging of `project/list`, splitting files into pieces and parts
for `project/load`, which attachments are complete and who uploaded them, `attachment_wanted` and its 10 s rule, the
checksum of every file of `project/save` over the bytes that arrived (before `not_confirmed`, `superseded` and
`store_changed`, also with `force`), the rights the protocol fixes (only the host manages a local session; whoever came by invitation
never invites; who may start a hosted session is who may save it).

### Plugging in another operator (dzbot)

Build a `core.Service` with your own adapters and serve it with `httpapi.Handler` — nothing in `core` changes:

```go
svc, err := core.New(core.Options{
	ServerName:   "dzbot",
	Capabilities: core.Capabilities{Users: true, InviteUser: true, Hosted: true, Agents: true},
}, core.Deps{
	Identity:      dzbotIdentity,    // AuthMethods: {provider "dzbot"}; token → dzbot account
	Permissions:   dzbotPermissions, // mod/placement access, "may always join", managers
	Directory:     dzbotFriends,     // friends for users/list
	Storage:       brokerStorage,    // the session log in a broker or database
	Attachments:   objectStorage,    // attachment parts in a directory or a bucket
	ProjectStores: []core.ProjectStore{dzbotMods}, // provider "dzbot": atomic write of files + manifest revision
	Hooks:         dzbotHolds,       // hold the project in dzbot while a hosted session runs
	Clock:         systemClock,
})
if err == nil {
	err = svc.Start()
}
http.Handle("/dzcollab/", httpapi.Handler(httpapi.Options{Service: svc, BasePath: "/dzcollab/"}))
```

Contracts an adapter must keep:

- `AgentTokens.IsAgentToken` (optional, capability `agents`) answers whether a token came from a code login whose
  `LoginContext.Agent` was set; the core then treats every participant with it as an agent and refuses the five
  requests an agent may never make before it looks at the session. `localidentity` keeps the flag in the signed token.
- `IdentityProvider.Authenticate` returns a `UserID` of `A–Z a–z 0–9 _ -` (1–64) and a display name; names over 64
  bytes are cut by the core. Login methods come in the order the server prefers. Refuse with a `*protocol.Error`
  (`not_allowed`, `throttled`) where the protocol has a code for it; any other error is answered as `internal`.
- `PermissionPolicy.Rights` is asked on every list, join and project request; keep it fast or cache. `Invite` is
  only used for participants with standing rights. `Join` also means "may list and load" a hosted project.
- `SessionStorage`: the core never overlaps calls; every call returns only once its data survives a restart
  (the core answers the client right after). `Read(after, limit)` returns entries with `seq > after` in order. Pieces
  come back byte for byte with the same boundaries. `LoadAll` and `LoadRequests` give back everything kept.
- A `SessionStorage` that also implements `DeferredStorage` may return from a write once it is queued: the core then
  waits, outside its lock, until every write queued before an answer was built is durable before it sends that
  answer (`busy` if that fails). A lost write must fail every later wait; restart and replay the log.
- `UserScopedStore` (optional) gets the user with every head and file read; the core has asked `Rights` before.
  The core asks `Rights` before it asks the store anything about a project.
- `Service.EndIdle(ms)` ends sessions without any request for that long (an operator's rule); `HostedSessions()`
  names running hosted sessions with creator and last revision.
- `AttachmentStorage` returns pieces byte for byte with the same boundaries and keeps them over a restart; the
  checksum is the client's opaque text (≤ 200 bytes), so encode it before using it as a file or object name.
- `ProjectStore.Save(SaveRequest)` writes all files and deletions in one step or nothing; files in neither list stay
  untouched. It forms the SHA-256 of every file over the bytes it stores before the write takes effect and returns
  `*core.ChecksumMismatchError` (nothing written) on a difference; a store that composes merged states (dzbot: a
  mission file from all deltas of a mod) checks them against `SaveRequest.Merged` unless `Force`, and names its
  values in `SaveResult.Merged` (nil: the core names target = file name). It may return a `*protocol.Error`, e.g.
  `store_changed` when it sees the change from outside itself. `FileRevision.Checksum` is the stored bytes' SHA-256.
  `ChangedSince(revision)` names files changed outside since that revision (an unknown revision: every file; `""` for
  a project that does not exist yet: nothing). `Head` and `ReadFile` describe one revision consistently, keep a
  revision replaced by a save readable for at least 10 minutes, and answer `ErrProjectUnknown`,
  `ErrRevisionUnknown`, `ErrFileUnknown`. `FileInfo.Hash` lets the core cache how a file splits into parts. `List`
  must answer within a few seconds for the whole store. The core serializes saves per project.

## Conformance suite

`conformance/` checks a server only over HTTP, like DZSTUDIO would: transport rules on every answer (HTTP 200,
marker line, one JSON object, no text over 1000 bytes, no answer over 4 MB, no broken characters), retries after lost
answers for every idempotent request — also across a clean and a hard restart —, two and three participants,
batches, large operations, chains, snapshots in parts, the long poll's timing, presence, locks and their lapse, live
states, invitations and throttling, removing, leaving, ending, coming back after 60 s and after 15 minutes,
checkpoints, hosted sessions, saving and save conflicts, `project/list` paging over 450 projects, `project/revision`,
`project/load` with revisions replaced while loading, opening a project without a provider plugin, save checksums
(save checksums) and their place in the error order, attachments in parts (also over a restart), `attachment/want`,
rights before existence, and agents (marking, token binding, rights, phase and status, removing one's own agent, a
server without the capability).

Against the reference server (or any server binary that reads the same config file):

```bash
cd conformance && npm ci && DZCOLLAB_SERVER=/abs/path/to/bin/dzcollab npm test
```

The suite writes its own config into a temporary directory (`DZCOLLAB_KEEP=1` keeps it, with `server.log`).

Against a server running elsewhere:

```bash
DZCOLLAB_TARGET='{
  "url": "https://collab.example.org/dzcollab/",
  "tokens": {
    "anna": "…", "ben": "…", "carla": "…", "dana": "…",
    "throwaway": "…", "extra": ["…", "…", "…", "…", "…", "…", "…", "…", "…", "…"]
  },
  "hosted": { "ref": { "provider": "dzbot", "key": "…" }, "revision": "…", "groupFile": "…", "joinOnly": false }
}' npm test
```

`hosted.groupFile` names the group file the hosted tests save next to the manifest (default `f-north-base`; a store
that only takes file names it knows, like dzbot's file keys, names one of the project). `hosted.joinOnly: false` says
the target has no right "may join, not save": Dana is left out of the hosted tests and an invited Carla stands in.
The current revision is asked with `project/revision`, so `revision` is only the fallback. A hosted session a failed
test left behind is discarded before the next hosted test starts.

The users need these rights on the target:

| Token | Local sessions | Hosted project `hosted.ref` |
| --- | --- | --- |
| `anna` | may start sessions | may save and manage |
| `ben` | may always join Anna's sessions | may save |
| `carla` | no rights | no rights |
| `dana` | no rights on Anna's sessions | may join only |
| `throwaway` | any; locked out of invite/redeem for a minute by the test | — |
| `extra` | no rights; 10 users for the colour and session-full tests | — |

Without `hosted`, `throwaway` or enough `extra` tokens the tests that need them are skipped. `revision` is the
project's current revision when the run starts; the suite follows it through its own saves. Hosted tests reuse the one
project, so it must have no running session when the run starts. Restart checks only run in spawn mode.

Checks that need time to pass (30 s, 300 s, 10 minutes, 24 hours), operator-ended sessions and changes to the project
store from outside run when the target offers the same test controls as the reference server with
`testing.enabled`; otherwise they are skipped and listed as such:

| Control (POST, below the base address) | Body | Effect |
| --- | --- | --- |
| `_test/clock` | `{advanceMs}` | Moves the server's clock forward and runs its time rules (the offset survives restarts) |
| `_test/session/end` | `{sessionId}` | Ends a session for reason `server` |
| `_test/attachment/forget` | `{sessionId, checksum}` | The server loses one complete attachment (for `attachment/want`) |
| `_test/client-version` | `{min?, recommended?, updateUrl?}` | Replaces `clientVersion` while the server runs (as an operator changing it); `{}` turns the check off |
| `_test/store/revision` | `{provider, key}` | `{revision}` of a hosted project (`null` if none) |
| `_test/store/read` | `{provider, key}` | `{project: {revision, files: [{name, pieces}]}}` |
| `_test/store/edit` | `{provider, key, name?, content?, files?: [{name, content}], projectName?, map?}` | Changes files as an edit from outside would (creates the project if missing), sets name and map |
| `_test/net` | `{userId?, delayMs?, dropResponses?, refuse?, untilMs?}` | Simulates a bad connection for requests of `userId` (a userId or `name:<display name>`; missing: every request): `delayMs` (≤ 600 000) holds each answer back after the server processed the request, `dropResponses` processes the request and then closes the connection without an answer (the connection looks lost, the server applied it), `refuse` closes the connection without processing (the server looks unreachable). Holds until `untilMs` (ms since 1970 on the server's clock, which `_test/clock` moves; the answer names `nowMs`) or until the next `_test/net`; `{}` resets. Requests below `_test/` are never affected |

## Security notes

- **Whoever runs a server sees the projects.** The server stores every operation, every snapshot and, for hosted
  projects, the files — in plain text. Only use a server whose operator you trust with your project; the operator
  notice (`server.notice`) is the place to say so.
- Use HTTPS (a reverse proxy with `trustProxy: true` and `publicUrl`). Tokens travel only in request bodies, never in
  addresses, but plain HTTP shows them to anyone on the network. HTTP is fine only inside your own network.
- `auth.name` trusts any name: everyone can be "Anna". Use it only on a LAN, or restrict it with `allowedNames`; for
  anything reachable from the internet use `auth.code` with passwords (or your own identity adapter).
- Passwords in the config file are plain text; keep the file readable only by the server. The token secret in
  `dataDir/token-secret` signs every token — whoever has it can log in as anyone. Changing it logs everybody out.
- Invitation codes have 40 bits from the system's secure random source, are valid for one person and 24 hours, and
  failed redemptions are limited to 10 per minute and user. Login codes and the confirmation page are rate-limited
  per address.
- Everyone with `join` on a hosted project can download all its files with `project/load`; grant it accordingly.
- `testing.enabled` lets anyone move the clock, end sessions, edit hosted projects and cut connections. Never enable it on a server
  people use.
- The server never interprets operations, so it cannot tell a malicious one from a good one: anyone you let into a
  session can change (or delete) everything in it. Rights and invitations are the only gate.

## License

GNU Affero General Public License v3.0 — see [LICENSE](LICENSE). If you run a modified version as a network service, you must offer its source to the people using it.
