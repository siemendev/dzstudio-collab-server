# dzcollab 1 — protocol for editing together

dzcollab 1 is the protocol that DZSTUDIO, the in-game editor mod for DayZ, uses to edit one project together with
others. DZSTUDIO's plugin “Edit together” speaks it with a **server for editing together**. The protocol is open:
anyone may build a server that speaks it — dzbot does, and so does a small reference server for self-hosting. This
document is the complete description; whoever implements it needs no other.

The protocol relies on these guarantees: every change is an operation with an ID, the whole project state has a text
form made of pieces, and the core applies operations in a fixed order and confirms its own. For this protocol,
operations and the project state are opaque pieces, and so are the files that a save sends.

“Must” means a duty for every conforming server or client, “should” an urgent recommendation, “may” optional. A
**capability** (Versions and capabilities) marks what a server may offer in addition.

## Role of the server

The server holds the power; the client is thin. There are two kinds of session (`kind`):

- **`local`**: The project lives at the client of the person who creates the session, the **host**
  (`role: "host"`); everyone else is a guest (`"guest"`). The host saves on their own, without the server. Without a
  host there is no session.
- **`hosted`** (optional capability `hosted`): The project lives in the server's **project store** (at dzbot, a mod
  or a placement). The server is the host; all participants have `role: "participant"`. Saving goes through the
  server (Saving). The session outlives every participant.

The server:

- decides who may see and enter a session (rights to the project, standing permissions per user, invitations), and
  assigns colour and rights (`canInvite`, `canSave`, `canManage`) per participant;
- fixes the one order of all operations: gap-free sequence numbers `seq` per session, in order of arrival; it passes
  every operation on byte for byte, unchanged, to all participants, including its sender;
- keeps, per session and in its own storage, the **session log**: the most recently uploaded project state (the
  **snapshot**, a text form with the sequence number up to which it reaches) and the operations after it, each with
  its number. From it, joining and returning participants catch up from their last known number. The protocol only
  requires that catching up works this way; how and with what the server stores the log is its own business;
- keeps participants, presence, locks, live states, invitations and the save state.

The server **reads no operation** and checks no precondition. Whether an operation holds, every client decides the
same way by applying the log in the order of the sequence numbers. **Syncing is not saving:** nothing from the session
log goes into a project store. The server never touches the project store of a `local` session; into that of a
`hosted` session it writes only the files that a participant sends with `project/save`, in one step.

Not part of this protocol is the administration at the server: who has rights to which project, who gives whom “may
always join”, friend lists, blocking users. Each server handles that itself (dzbot with a friend list per dzbot
account, maintained on dzbot.de and through the dzbot editor endpoints). Of all this, the protocol contains only what
the client needs: the lists of sessions and of available users, the rights per participant, and redeeming an
invitation.

**Standing permission** (“may always join”): Whether a server knows standing permissions is its own business; they
are not required, and the client needs nothing of its own for them. If the server knows them and one user gives
another a standing permission, the other may join directly the `local` sessions whose host is the giver. In the
protocol it shows only like this: such sessions appear in the receiver's `sessions/list` with `access: "direct"`, and
on joining the receiver has the rights to the project of a `local` session (`canInvite: true`, `canSave` and
`canManage` `false`). If the giver withdraws it, that counts like losing a right (Joining): whoever is already a
participant stays one until they leave or are removed; after that, the session no longer appears in their list. For
`hosted` sessions the rights to the project store count; whether a server also knows permissions there is its own
business (dzbot: no).

## Transport

The limits come from the game: only outgoing GET and POST requests, no sockets, no incoming connections, no connection
between players; every request ends after 10 s; about 6 requests at the same time; GET carries no headers of its own;
status codes and response headers cannot be read; the game's simple JSON reader cuts texts off after 1023 bytes.

- **Address:** A server has a **base address** (such as `https://collab.example.org/dzcollab/`); every endpoint is
  base address + name (`…/dzcollab/session/join`). The server should offer HTTPS; it may offer HTTP for servers in the
  local network.
- **Request:** `info` is a GET without a body and without login (a POST with an empty object must work as well). All
  other endpoints are POST with a JSON object in UTF-8. The server must read the body as JSON regardless of the stated
  content type. The token stands only in the body, never in the address.
- **Response:** always HTTP 200, never a redirect, never without a body. The body starts with the **header line**
  `dzcollab ‹major version›` (for this version `dzcollab 1`) and a line break (`\n`), followed by exactly one JSON
  object in UTF-8. A response without this header line or with incomplete JSON is an error “Server not reachable” for
  the client, even if it arrives as a success (such as the error page of a proxy).
- **Common fields of every response:** `ok` (boolean); if `false`, the object `error` (Errors).
- **Common fields of every request** except `info`: `protocol` (text, the version spoken, such as `"1.0"`) and, where
  logged in, `token`.
- **Texts:** No text in a response is longer than 1000 bytes (UTF-8, counted after decoding escape sequences), and
  none ends in the middle of a character. The server should write characters outside ASCII raw, not as a `\u`
  sequence. That way each of the game's two JSON readers copes. Requests keep the same limit.
- **Pieces:** Long content — an operation, the project state, a file, an attachment (Attachments) — travels as the
  field `pieces`: a list of texts of at most 1000 bytes each, split only at character boundaries. For an operation and
  the project state they are exactly the pieces of the text form; their concatenation is the text form. For a file
  and an attachment their concatenation is the content of the file in UTF-8. The server must store and return pieces
  byte for byte and with the same boundaries. It writes each piece as tightly as JSON allows: as escape sequences only
  `\"`, `\\` and control characters (`\n`, `\r`, `\t`, `\b`, `\f`, otherwise `\u00XX`), everything else raw,
  including `/`. That way a piece in a response is never longer than the client counted it (Sizes).
- **Numbers:** Integers lie between 0 and 2,147,483,647 (32 bit, as in the game), unless stated otherwise.
  Floating-point numbers (positions, angles) are written as the shortest decimal text that yields the same float32
  value, never with an exponent and always with a decimal point, whole values with `.0` (`6512.0`, `0.1`,
  `0.0000001`). Times never travel as clock times, only as an age or a duration in seconds or milliseconds.
- **IDs:** `sessionId`, `participantId`, `userId`: texts from `A–Z a–z 0–9 _ -`, 1 to 64 characters, assigned by the
  server. IDs of elements and operations (`‹prefix›-‹n›`): at most 64 bytes, assigned by the client; the server
  treats them as opaque texts.
- **Sizes** (1 MB = 1,048,576 bytes): A request is at most `maxRequestBytes` large (info, at least 4 MB); the server
  must accept requests up to this size. A response is at most 4 MB. An **operation** is at most `maxOpBytes` = 3.5 MB
  (3,670,016 bytes), counted as the sum of its pieces as the server writes them (with quotation marks, tightly
  escaped, see Pieces); the client counts the same way before sending. The server must accept operations up to this
  size and rejects larger ones with `too_large`. That way every operation fits whole into one response (poll); a
  larger step (an import of 50,000 objects) travels as a chain (Large steps: chains). Snapshots, files and operations
  over 1 MB travel in **parts** of at most 1 MB of pieces, counted the same way (Snapshots, Sending operations,
  Projects on the server). A save always travels in one request (Saving).
- **Response time:** The server answers every request at most 5 s after it arrives, `poll` at most `waitMs` + 1000 ms
  after it arrives, `project/save` at most 9 s after it arrives, none later than 9 s. What would take longer it rejects
  with `busy`.
- **Field names** are English; both sides ignore unknown fields.

**Example** of a response (body as it arrives):

```
dzcollab 1
{"ok":true,"results":[{"clientSeq":7,"seq":118}],"nextClientSeq":8}
```

## Versions and capabilities

- The protocol version has the form `‹major›.‹minor›`; this document describes **1.0**. A minor version only adds
  (new fields, new capabilities, new error codes); a 1.0 client talks to a 1.3 server and the other way round. Whatever
  cannot be added that way gets a new major version.
- `info` names the major versions the server speaks, each with its highest minor version. The client takes the
  highest major version both speak and sends `protocol` in every request as `"‹major›.‹minor›"` with its own minor
  version; the server answers in this major version and uses nothing that came only after this minor version. If it
  does not speak the major version: `protocol_unsupported`. If `protocol` is missing or does not have the form of two
  numbers with a dot: `bad_request`.
- **Capabilities** are names in the list `capabilities` of `info`. Everything this document names without a
  capability is **required** in major version 1 and is not in the list. The client uses an **optional** capability
  only if its name is in the list; if it is missing, the client hides the related controls. It ignores an unknown name.

| Capability | Required/optional | Content |
|---|---|---|
| — | required | `info`, at least one login method, `sessions/list`, `session/create` with `kind: "local"`, `snapshot/put`, `snapshot/get`, `session/join` with returning, `ops/send`, `poll` with presence and save state, `locks/set`, `locks/release`, `live/put`, `invite/create` (code), `invite/redeem`, `participant/remove`, `attachment/put`, `attachment/get`, `attachment/want`, `session/leave`, `session/end` |
| `users` | optional | `users/list`: users the server reports as available to the requester (such as friends) |
| `invite.user` | optional | `invite/create` with `forUserId`: an invitation for a specific user, without them typing a code |
| `hosted` | optional | Sessions for projects in the server's project store (`kind: "hosted"`, `hostedRef`): the server as host, finding the session of a project, `project/save`, discarding with `session/end`, listing with `project/list`, loading with `project/revision` and `project/load`; without a provider plugin of its own, always in a session |
| `auth.code` | optional | Login with a code that the user confirms on a page of the server |
| `auth.name` | optional | Login with a display name only (for servers in the local network) |
| `agents` | optional | Participants without a game who mark themselves as an agent (`client.kind`), with phase and status in presence, without saving, managing, inviting and creating projects; a user may always remove their own agents (Agents) |

**Client version:** A server may require a minimum version for DZSTUDIO (a setting of the operator). The client names
itself in the field `client`: `{"name":"dzstudio","version":"0.4.0"}`, the version in the form `MAJOR.MINOR.PATCH`
(compared part by part as numbers). `client` is required in `session/create` and `session/join`; in
`auth/code/start` and `auth/name` it may be missing. A server that checks checks exactly these four requests,
`session/join` only without `resume`, so that a running session never ends because of the version: if `client.name`
is `"dzstudio"` and `version` is missing, does not have the form or is below the minimum version, or if `client` is
missing entirely, it answers `client_outdated` (Errors); about other names it decides itself. In `info` it may name
the minimum version, the recommended version and the Workshop page. A server that does not check leaves these fields
out and never answers `client_outdated`; a client that does not know the code treats it like `internal`. All of this
belongs to 1.0; the header line stays `dzcollab 1`.

**Client kind:** `client` may also carry `kind`: `"studio"` (a DZSTUDIO in the game; the default if it is missing) or
`"agent"` (a participant without a game who speaks for an AI agent). What `"agent"` does is set out in Agents; a
server with the optional capability `agents` answers any other value with `bad_request`. This too belongs to 1.0.

## Login

Logging in is an exchangeable part: the client gets a **token** from a provider; the protocol only carries it. A token
is a text of printable ASCII characters, at most 1000 bytes, opaque to the client. Every request except `info` and the
login requests carries it in the field `token`.

`info` names the login methods in `auth`, in the order the server prefers:

| Method | Required/optional | Flow |
|---|---|---|
| `{"method":"provider","provider":"‹name›"}` | one of the three methods is required | The client asks a login plugin that has registered for this name (the dzbot plugin for `dzbot`) for a token for this base address. How the plugin gets and renews it is the plugin's business. Without such a plugin the client cannot use this method. |
| `{"method":"code"}` | optional `auth.code` | `auth/code/start` (may carry `client`, Client version, with `kind: "agent"` for an agent, Agents) returns `loginCode` (8 characters like an invitation code, Invitations), `url` (page for confirming, at most 1000 bytes), `expiresInSeconds`, `intervalSeconds` (at least 2). The client shows the code and the address and asks with `auth/code/poll` (`loginCode`) at the given interval; response `state`: `waiting`, `confirmed` (with `token`, `name`), `expired`, `denied`. |
| `{"method":"name"}` | optional `auth.name` | `auth/name` with `name` (1 to 64 bytes) and, may be missing, `client` returns `token`. The server may reject names (`not_allowed`). How a name becomes a user is the server's decision; the same accepted name always yields the same `userId` at that server, so that a user with a new token finds their way back into their sessions. |

Responses with a token may name `tokenExpiresInSeconds`. If a response reports `token_expired`, the client gets a new
token once (from the provider plugin, or with the same method) and repeats the request unchanged with the same request
ID; if that fails, it counts as not logged in. On `token_invalid` it counts as not logged in at once. The server
derives the user from the token (`userId`, display name); the client never sends either itself.

## Errors

An error is a normal response with `"ok":false` and `error`:

| Field | Type | Meaning |
|---|---|---|
| `code` | text from the table below | machine-readable; an unknown code counts as `internal` |
| `message` | text, at most 1000 bytes, may be missing | for humans, in a language the server chooses (the client does not translate it); the client shows it only for `not_allowed` and `internal` (when starting “Cannot start a session: ‹message›”, when joining “Cannot join: ‹message›”) |
| `retryAfterMs` | number, may be missing | with `busy` and `throttled`: try again no earlier than this |
| further fields | per code | see table |

| Code | When | Client does |
|---|---|---|
| `protocol_unsupported` | major version not spoken | server not usable, hint “Update DZSTUDIO” or “Server too old” |
| `client_outdated` | client version missing, invalid or older than the server's minimum version (Client version); fields `minVersion` (text ≤ 32 bytes), `yourVersion` (text ≤ 32 bytes, as it arrived, otherwise shortened; `""` if it is missing) and `updateUrl` (Workshop page, text ≤ 1000 bytes, may be missing) | dialog “DZSTUDIO is out of date”; no more requests to this server until restart |
| `auth_required`, `token_invalid`, `token_expired` | no, invalid, expired token | Login |
| `not_allowed` | a right is missing (join, invite, save, manage, name), including what an agent may never do (Agents) | message with `message`; for a button that should have been off, nothing; on `poll` like `session_unknown` (the server checks rights before existence, Order) |
| `session_unknown` | the session does not exist (any more) | like ended, reason `server`; when joining the hint “This session is already over” |
| `session_ended` | session ended; field `reason` (with `discarded` also `by`, name ≤ 64 bytes) | end dialog: `host_ended` and `host_left` “‹host› ended the session”, `host_gone` “… can no longer be reached”, `discarded` “‹by› discarded the unsaved changes”, `server` “The server ended the session” |
| `session_full` | maximum reached | message “Session is full” |
| `not_participant` | participant unknown or removed; field `reason`: `"removed"` (with `by`, name ≤ 64 bytes) or `"timeout"` | for `removed` dialog “‹name› removed you from the session”; for `timeout` “Away too long” (Duties of the client › Returning) |
| `instance_key_taken` | session prefix already taken | roll a new prefix, join again |
| `snapshot_not_ready` | snapshot not fully uploaded yet | again after 1 s |
| `snapshot_unknown` | snapshot of this sequence number no longer there | join again |
| `client_seq_gap` | sequence number skipped; field `expectedClientSeq` | send again from this number |
| `batch_pending` | `ops/send` with new operations while the previous batch is not fully appended (parts or links of a chain are missing) | finish sending the previous one first |
| `cursor_too_old` | log from the cursor no longer there | “Away too long” (Duties of the client › Returning) |
| `too_large` | request, part, operation (over `maxOpBytes`) or chain (over `maxChainOps` links) too large, at the head of `project/load` more than 5000 files; for a limit in bytes (request, part, operation, snapshot, attachment) field `maxBytes` with this limit, for a limit on a count (links of a chain, files in the head) field `maxCount` instead, with this count (`maxChainOps` or 5000) | split smaller; for `project/save` message “Project too large for the server”; for `ops/send` a client error; for `project/load` opening fails (“Project too large for the server”) |
| `throttled` | too many requests of this kind | after `retryAfterMs` |
| `invite_invalid`, `invite_expired`, `invite_used` | the code does not match, has expired, was already redeemed | hint in the dialog |
| `already_running` | a session is already running for this project; field `sessionId` | join this session |
| `store_changed` | `project/save`: the project store was changed from outside since the session's last save; field `changed` (list of file names, at most 50) | question “Save anyway” / “Cancel” |
| `not_confirmed` | `project/save` with `seq` above the highest assigned sequence number | client error |
| `checksum_mismatch` | `project/save`: a checksum of the request does not match the server's, or a merged state that the server must check is missing in `merged` (Save checksums); the server has written nothing. Field `mismatches`: a list per differing entry with `kind` (`"file"` \| `"merged"`), `name` (name of the file or the `target`, text ≤ 200 bytes), `expected` (checksum from the request, text ≤ 200 bytes; missing if the request named none) and `actual` (the server's checksum, text ≤ 200 bytes; missing if the server forms no state for this name). The server always checks `"file"` entries, also with `force`; `"merged"` entries only without `force` and only when there is no `store_changed` (Order for project/save) | “Save failed: ‹file› did not reach the server intact” with the first entry; submit nothing, upload nothing |
| `project_unknown` | `project/revision`, `project/load`: no project under this `hostedRef` | opening fails as for a missing project |
| `revision_unknown` | `project/load`: this revision is no longer there | load once more from the start, without `hostedRevision` |
| `attachment_unknown` | `attachment/get`: no complete attachment with this checksum (never uploaded, not complete yet, or no longer there) | `attachment/want` (Attachments) |
| `capability_missing` | an optional capability was requested that the server does not have | client error |
| `busy` | server overloaded | after `retryAfterMs`, otherwise 1 s |
| `bad_request` | the request violates this document | client error |
| `internal` | otherwise | like a failed request |

**Order:** If a request violates several rules, the server answers with the error of the first stage that is violated
and does not check later stages:

1. **Transport and size:** the request is not a JSON object, `protocol` missing or invalid (`bad_request`), major
   version not spoken (`protocol_unsupported`), client version too old (`client_outdated`), request over
   `maxRequestBytes` (`too_large`), endpoint of an optional capability the server does not have
   (`capability_missing`);
2. **Login:** `auth_required`, `token_invalid`, `token_expired`;
3. **Rights:** `not_allowed` — checked before the server looks whether the thing named exists: whoever would have no
   right to a `hostedRef`, a session or an action gets `not_allowed`, whether or not the project or the session
   exists. That way the response does not reveal whether something exists;
4. **Existence:** `session_unknown`, `session_ended`, `not_participant`, `project_unknown`, `revision_unknown`,
   `snapshot_unknown`, `attachment_unknown`, `invite_invalid`;
5. **Validity of the content:** fields and values that violate this document (`bad_request`, such as a wrong `index`
   of a chain, a `seq` inside a chain, a file or a `part` out of range, a file without `checksum` in `project/save`),
   operation, part, chain or attachment too large, head with more than 5000 files (`too_large`), the checksum of a file
   in `project/save` that does not match (`checksum_mismatch` with `kind: "file"`, also with `force`);
6. **State** (conflict, precondition): `batch_pending` before `client_seq_gap`, then `cursor_too_old`,
   `snapshot_not_ready`, `session_full`, `instance_key_taken`, `already_running`, `invite_expired`, `invite_used`,
   `not_confirmed`, `store_changed`, and last `checksum_mismatch` with `kind: "merged"`.

**Order for project/save:** For `project/save` this means, after login, rights and existence, in this sequence:
(1) fields, a file without `checksum` (`bad_request`); (2) the checksum of every file over the bytes that arrived
(`checksum_mismatch`, `"file"` entries only), with and without `force`; (3) `not_confirmed`; (4) `seq` not above
`savedSeq` (except for the first save of a new project, New project on the server): `superseded: true`, not an error,
nothing else checked; (5) without `force`: changed from outside (`store_changed`) — with all files of the project
store, including those that are not in `files` but contribute to a merged state of the save; (6) without `force`: the
merged states (`checksum_mismatch`, `"merged"` entries only), except for a target path to which a file outside `files`
contributes that has changed since the session was created — the client's expectation does not know this change, and
after a “Save anyway” every further save would otherwise be rejected. With `force`, (5) and (6) are dropped: the
server then does not compare the request's `merged`, not even missing or surplus entries, but it still checks the
checksums of the files. That way a change from outside to a file outside the save that contributes to the same target
path yields `store_changed` and not `checksum_mismatch`, and “Save anyway” remains possible.

In `ops/send` the order applies per entry, the entries in their order; what came before the first faulty entry
stands. The server may answer `busy`, `throttled` and `internal` at any stage.

## Endpoints

| Name | Purpose | Required/optional | Repeatable via |
|---|---|---|---|
| `info` (GET) | information | required | changes nothing |
| `auth/code/start`, `auth/code/poll` | login with a code | `auth.code` | changes nothing, or `loginCode` |
| `auth/name` | login with a name | `auth.name` | `requestId` |
| `sessions/list` | sessions I can join | required | changes nothing |
| `users/list` | available users | `users` | changes nothing |
| `session/create` | create a session | required (`hosted`: optional) | `requestId` |
| `snapshot/put`, `snapshot/get` | upload and download a snapshot | required | sequence number + part |
| `session/join` | join, return | required | `requestId` |
| `ops/send` | send operations | required | `clientSeq` |
| `poll` | fetch what is new, report presence | required | cursor |
| `locks/set` | set own locks | required | `lockSeq` |
| `locks/release` | release another participant's locks (`canManage`) | required | `requestId` |
| `live/put` | live state | required | `liveId` + `n` |
| `invite/create` | request an invitation | required (`forUserId`: `invite.user`) | `requestId` |
| `invite/redeem` | redeem an invitation | required | `requestId` |
| `participant/remove` | remove a participant (`canManage`) | required | same effect |
| `project/save` | save the project (`canSave`) | `hosted` | `requestId` |
| `project/list`, `project/revision`, `project/load` | list projects, learn the revision, load a project | `hosted` | changes nothing |
| `attachment/put` | upload an attachment | required | checksum + part |
| `attachment/get` | fetch an attachment | required | changes nothing |
| `attachment/want` | ask again for a missing attachment | required | same effect |
| `session/leave`, `session/end` | leave, end or discard | required | same effect |

`requestId`: 16 characters from `0–9 a–z`, rolled by the client before the first send and the same on every repetition
of the same request (Retries and timeouts). Fields in the tables below are required where “may be missing” is not
stated; `protocol` and `token` are not repeated there.

## Information: info

| Response field | Type | Meaning |
|---|---|---|
| `server` | object: `name` (text ≤ 64 bytes), `software` (text, may be missing) | name for the list of servers and for `hosted` sessions |
| `protocols` | list of `{"major":1,"minor":0}` | versions spoken |
| `capabilities` | list of texts | optional capabilities the server offers |
| `auth` | list of login methods | Login |
| `limits` | object | `maxRequestBytes` (at least 4,194,304), `maxWaitMs` (at least 8000), `maxParticipants` (at least 2), `maxOpBytes` (3,670,016, Sizes), `maxChainOps` (16, Large steps: chains), `liveIntervalMs` (200), `lockLapseSeconds` (300), `connectionGraceSeconds` (30), `hostGoneSeconds` (300, `local` only). The last six are fixed in 1.0; they are there for information only. |
| `notice` | text ≤ 1000 bytes, may be missing | note from the operator; the client shows it in the settings |
| `minClientVersion` | text ≤ 32 bytes, may be missing | minimum version for `client.name` `"dzstudio"` (Client version); the client compares on its own before it logs in, creates or joins |
| `recommendedClientVersion` | text ≤ 32 bytes, may be missing | recommended version; only meaningful above `minClientVersion` |
| `updateUrl` | text ≤ 1000 bytes, may be missing | Workshop page of DZSTUDIO, as in `client_outdated` |

```
dzcollab 1
{"ok":true,"server":{"name":"dzbot"},"protocols":[{"major":1,"minor":0}],
 "capabilities":["users","invite.user","hosted"],"auth":[{"method":"provider","provider":"dzbot"}],
 "limits":{"maxRequestBytes":8388608,"maxWaitMs":8000,"maxParticipants":8,"maxOpBytes":3670016,"maxChainOps":16,
 "liveIntervalMs":200,"lockLapseSeconds":300,"connectionGraceSeconds":30,"hostGoneSeconds":300},
 "minClientVersion":"0.4.0","updateUrl":"https://steamcommunity.com/sharedfiles/filedetails/?id=‹id›"}
```

## Lists: sessions/list, users/list

**`sessions/list`** names the sessions the requester may join now: directly (rights to the project, standing
permission) or through an invitation that was already redeemed or issued for them. Request: may contain `hostedRef`
(optional capability `hosted`); then only the running session of this project, if there is one and the requester may
enter it — that is how a client finds a project's session when it opens a project on the server. The list names at
most 200 sessions; which ones if there are more, the server chooses.

| Field per session (`sessions`) | Type | Meaning |
|---|---|---|
| `sessionId` | ID | |
| `kind` | `"local"` \| `"hosted"` | |
| `projectName` | text ≤ 200 bytes | as named on creation |
| `map` | text ≤ 64 bytes | map key as named on creation (session/create) |
| `host` | `{"userId","name"}`, `local` only | host |
| `participants` | number | current participants (for `hosted` also 0) |
| `unsavedCount` | number, `hosted` only | as in `poll` › `saving` |
| `access` | `"direct"` \| `"invited"` | why the requester may join |
| `requires` | list of `{"type":"plugin:typ","version":n}` (`type` text ≤ 200 bytes), may be missing | element types from the head of the snapshot, as named on creation; the client greys out “Join” if it lacks one of them |
| `hostedRef` | object, `hosted` only | which project |

**`users/list`** (optional capability `users`) names in `users`, per user, `userId`, `name` and `state` (`"online"`,
`"offline"`, `"in_session"`), at most 200. Whom the server names is its decision (at dzbot, the requester's friends).
`in_session`: the user is a participant of a session and had contact there in the last 30 s; otherwise `online`: they
sent a request to the server in the last 60 s; otherwise `offline`. A server with its own source for this (dzbot) may
determine `online` from that source. To a requester to whom the named user should not show their state, the server may
report them as `offline` (dzbot shows `online` and `in_session` only if both list each other as friends); being named
does not invite anyone and grants no right.

## Creating a session: session/create

| Request field | Type | Meaning |
|---|---|---|
| `requestId` | text | Retries and timeouts |
| `client` | `{"name":"dzstudio","version":"…"}`, plus `kind` (may be missing) | name and version of the client; the server may reject with `bad_request` if it does not want to know it, and with `client_outdated` if the version is too old (Client version); `kind` as described there under Client kind (Agents) |
| `projectName` | text ≤ 200 bytes | for display and as the suggestion for “Save a copy” |
| `map` | text ≤ 64 bytes | map key of the project, as in the project file (`chernarus`, `livonia`, `sakhal`, `badlands`); a joining participant loads the world from it. The server passes it on unchanged. A client treats a key it does not know like a map that is not available |
| `kind` | `"local"` \| `"hosted"` | where the project lives; `hosted` only with the optional capability `hosted` |
| `hostedRef` | `{"provider":"dzbot","key":"…"}`, texts ≤ 200 bytes | `hosted` only: which project of the project store; the key is the provider's business; for a new project see New project on the server |
| `hostedRevision` | text ≤ 200 bytes | `hosted` only: the state of the project store from which the uploaded project state comes, as the storage provider names it (at dzbot the revision of the manifest after the save when starting, otherwise from project/load or from the last save); opaque to the client; empty (`""`) only for a project that the project store does not have yet |
| `requires` | as in `sessions/list`, may be missing | |
| `instanceKey` | text, 10 characters | session prefix of the instance; the server checks that no participant has the same one |
| `snapshotBytes`, `snapshotParts` | numbers | size of the snapshot that follows: `snapshotParts` must equal `parts` of the parts of snapshot 0 (otherwise `snapshot/put` answers `bad_request`); the server checks `snapshotBytes` only against its own limit for snapshots (above it `too_large`), not against the uploaded bytes |

| Response field | Type | Meaning |
|---|---|---|
| `sessionId`, `participantId` | IDs | |
| `role` | `"host"` (`local`) \| `"participant"` (`hosted`) | |
| `colour` | number 1–8 | position in the colour sequence |
| `canInvite`, `canSave`, `canManage` | booleans | as in session/join |
| `capabilities` | list | optional capabilities that apply to this session |

After that, the creator uploads the snapshot with sequence number 0 (Snapshots). Until it is complete, the session
appears in no list, and `session/join` answers `snapshot_not_ready`. For `hosted`, the server checks that the creator
may save the project (otherwise `not_allowed`), and answers `already_running` if a session is already running for this
`hostedRef`; the client then joins that session, even if its snapshot 0 is still uploading (its `session/join` then
answers `snapshot_not_ready` until the snapshot is complete). The server remembers `hostedRevision` as the state of the
last save, with sequence number 0; with an empty `hostedRevision`, nothing is saved yet. If the creator has no contact
for 300 s before snapshot 0 is complete, the server discards the session; it appeared in no list, and `session/join`
answers `session_unknown`.

**New project on the server:** The client takes the `provider` from project/list › `stores`, only one with
`canCreate: true`; it generates the `key` itself: 16 random characters from `a`–`z` and `0`–`9`. A server that reports
`canCreate` accepts any such key. Along with it go `hostedRevision: ""` and, as `projectName`, the name the user
entered. After snapshot 0 the client saves at once with `project/save` and `seq` 0 (still without operations): this
first save of the session writes (`superseded: false`), creates the project with the session's `projectName` and
`map`, and sets `savedSeq` 0, `savedBy` and `hostedRevision`; only after that does superseded (Saving) apply as usual.
If the project store already has something under the key, the save answers `store_changed` and writes nothing.

## Snapshots: snapshot/put, snapshot/get

A **snapshot** is the text form of the whole project state after all operations up to and including a sequence
number `seq`. It travels in parts; each part is a list of pieces of at most 1 MB together. A snapshot belongs to the
session log in the server's storage, not in the project store.

| Field (`snapshot/put`) | Type | Meaning |
|---|---|---|
| `sessionId`, `participantId` | IDs | |
| `seq` | number | 0 for the first snapshot, otherwise the sequence number up to which it reaches |
| `part`, `parts` | numbers, `part` from 0 | this part out of how many |
| `pieces` | list of texts | the pieces of this part |

Response: `complete` (boolean: all parts are there). The same part twice changes nothing. The creator uploads
snapshot 0. A snapshot with `seq` > 0 (a **checkpoint**) is uploaded only by the participant to whom a response to
`poll` names `checkpointWanted: true` — for `local` always the host, even while they have no contact (nobody else
takes over; the server waits until the host asks again, or the session ends after 300 s with `host_gone`), for
`hosted` a participant of the server's choice: the one who is in `participants` and had contact most recently (in the
last 10 s); if they leave or lose contact before the checkpoint is complete, the server chooses another; if nobody has
contact, it asks for none for the time being —, and only at a moment when that participant has applied all operations
up to `seq` and has no unconfirmed operation of their own. The server should ask for a checkpoint when the log has more
than 1000 operations or 8 MB since the last snapshot; whether it then throws away the operations before a checkpoint is
its own business (Duties of a server). Only when a new snapshot is complete does it apply; the server must keep the
previous one for another 10 minutes for running downloads. If a participant other than the one asked uploads, the
server answers `not_allowed`. `checkpointWanted: true` stands in every response to the one asked until a checkpoint is
complete; only the first time does it wake a waiting poll. The server rejects a `seq` inside a chain (Large steps:
chains) with `bad_request`.

**`snapshot/get`** with `sessionId`, `participantId`, `seq`, `part` returns `seq`, `part`, `parts`, `pieces`; for a
snapshot that is no longer there, `snapshot_unknown`.

## Joining and returning: session/join

| Request field | Type | Meaning |
|---|---|---|
| `requestId`, `client` | | as for `session/create` |
| `sessionId` | ID | |
| `instanceKey` | text, 10 characters | session prefix of the new instance |
| `resume` | `{"participantId","after"}`, may be missing | returning: the same participant with their cursor (last applied sequence number); `instanceKey` is then the previous prefix |

| Response field | Type | Meaning |
|---|---|---|
| `participantId`, `colour` | | unchanged with `resume` |
| `kind` | `"local"` \| `"hosted"` | |
| `role` | `"host"` \| `"guest"` (`local`), `"participant"` (`hosted`) | |
| `canInvite` | boolean | may use `invite/create`: everyone with rights to the project, never someone who came only by invitation |
| `canSave` | boolean | may use `project/save` (`hosted` only; for `local` always `false`, the host saves without the server) |
| `canManage` | boolean | may use `participant/remove`, `locks/release` and, for `hosted`, `session/end` (discarding); for `local` exactly the host |
| `mode` | `"snapshot"` \| `"resume"` | `resume`: continue from the cursor with `poll`; `snapshot`: load the snapshot |
| `snapshot` | `{"seq","parts","bytes"}` | with `mode: "snapshot"`: the current snapshot |
| `seq` | number | highest assigned sequence number |
| `capabilities` | list | optional capabilities that apply to this session |

The server checks the right as in Role of the server: directly or with an invitation for this session, otherwise
`not_allowed`. With `resume` it answers `mode: "resume"` if it still has the log from `after`, otherwise
`mode: "snapshot"`. After that, the client loads the snapshot with `snapshot/get`, opens an instance from it and polls
with `poll` from `after = snapshot.seq`. Participants, locks, presence and save state come with the first response to
`poll` (with versions 0). A removed participant cannot come back with `resume` (`not_participant`, `removed`); without
`resume` only if they have a right or a new invitation again.

Without `resume` the server always assigns a new `participantId` (and a colour as in Duties of a server), also to the
same user, and never assigns a `participantId` twice in a session; `clientSeq` and `lockSeq` then start at 1 again. A
joining participant appears in other participants' `participants` only from their first `poll` request; until then
(while loading the snapshot) they do not count in `sessions/list` › `participants`. If a client aborts joining, it sends
`session/leave`; if it did not know the `participantId` yet, it first repeats `session/join` with the same `requestId`
to learn it.

For `maxParticipants` (`session_full`) and the uniqueness of `instanceKey`, all participants count who are still in
the session, including joining ones before their first poll (they already carry a colour); not those who have left,
been removed or been taken out. After a participant's own `session/leave`, the server answers `resume` and every other
request of this `participantId` with `not_participant`, `reason: "timeout"`, except a repeated `session/leave` (`ok`);
the client then joins again without `resume`. Whoever has lost their rights to the project but is not removed returns
with `resume`: they stay a participant until they leave or are removed, as with invitations.

## Agents (optional capability agents)

An **agent** is a participant without a game who speaks for an AI agent. It speaks the same protocol as DZSTUDIO and
logs in with its user's account, usually with the code login (optional capability `auth.code`). A server that admits
agents names the optional capability `agents`; everything in this section applies only to such a server. Agents work
with people, never in their place: a person creates the project, and a person saves or discards what an agent did.

- **Marking:** An agent sends `client.kind: "agent"` in `session/create` and `session/join` (Client kind). The kind
  applies from joining without `resume` and stays for this `participantId`; with `resume` the field does not count. If
  an agent got its token with `auth/code/start` and `client.kind: "agent"`, the token is **bound to an agent**: every
  participant with this token is an agent, whatever `session/create` or `session/join` names; the page for confirming
  should say that an agent is logging in. A version check (Client version) applies only to `client.name`
  `"dzstudio"`; about other names the server decides as before.
- **Name:** `client` of an agent may also carry `agentName`: the short name its user gave it (text, one line without
  control characters, 1 to 64 bytes, may be missing; longer, empty or with a control character: `bad_request`). It
  names the agent beside its user; it is not a display name of its own and grants nothing. In `auth/code/start` it is
  checked the same way, and the page for confirming should name it.
- **Rights:** An agent always has `canSave`, `canManage` and `canInvite` `false`, in `session/create`, `session/join`
  and `participants`, even if its user may do more. To `project/save`, `session/end`, `participant/remove`,
  `locks/release` and `invite/create` from an agent the server answers `not_allowed`, without exception; with a token
  bound to an agent already at the stage Rights, before it looks whether the session exists (Order). It may use
  `session/leave`, `ops/send`, `locks/set`, `live/put`, `snapshot/put` (checkpoints), `attachment/put`,
  `attachment/get`, `attachment/want` and `invite/redeem` like anyone.
- **Creating:** An agent is never a host and creates no project. `session/create` from an agent with `kind: "local"`,
  or with `kind: "hosted"` and an empty `hostedRevision` (New project on the server): `not_allowed`. With
  `kind: "hosted"` for a project the project store has, the server checks the **user's** right to save the project,
  as for any creation (otherwise `not_allowed`); the session then has the agent as its only participant. What it does
  there stays in the session log, as in any `hosted` session, until a person with `canSave` saves it or one with
  `canManage` discards it.
- **Joining:** An agent may join where its user could (Role of the server): with rights to the project, with a
  standing permission or with an invitation its user redeemed. In addition it may directly join a `local` session
  whose host is its user (the same `userId`); `sessions/list` names that session to it with `access: "direct"`. An
  agent counts for `maxParticipants`, colours and the end of a `hosted` session like any participant.
- **Removing one's own agents:** A participant who is not an agent may always remove an agent with the same `userId`
  with `participant/remove`, even without `canManage` — also as a guest in someone else's `local` session. Removing it
  this way invalidates no invitation (they are its user's). If someone else removes an agent, Removing applies
  unchanged, including the invitations of its user.
- **Presence:** An agent sends in `poll` › `presence`:
  - `phase`: `"waiting"` (waiting for work), `"working"` (working) or `"review"` (waiting for review); required for an
    agent;
  - `status`: text, one line without control characters, at most 200 bytes, may be missing or empty;
  - `position`: the centre of the place where it works — the centre of the bounding box around the elements it
    currently holds, otherwise around those it changed last; may be missing as long as it has none;
  - `idleSeconds`: seconds since its last `ops/send` or `locks/set` request with a new set, not since its last poll;
    `idle` and the lapse of its locks follow from it as for anyone.

  If `phase` is missing or has another value, or if `status` is too long or contains a control character:
  `bad_request`, and nothing of the poll applies. The server passes on `phase` and `status` of every agent in
  `presence` of every response, as they last arrived (initially `"waiting"` and empty, also after a restart of the
  server); like position and idleness, they do not wake a waiting poll. The server ignores `phase` and `status` from a
  participant that is not an agent and does not pass them on.
- **Participant list:** `participants` › `list` names `kind` for each participant, and `agentName` for an agent that
  named one. `name` is the user's display name, not “Agent of …”; the client builds that text in its own language
  (with `agentName`: “‹agentName› · ‹name›'s agent”).
- **Several sessions:** With the same token, an agent may be a participant in several sessions at the same time, each
  with its own `participantId` and its own presence; it keeps one poll open per session.
- **Operations:** An agent sends the same operation types as DZSTUDIO, in the same text form (Sending operations),
  with its user as the author; there are no operation types of its own. The server reads them no more than any other.
- **Duties of an agent:** Besides those Duties of the client that apply without a game, an agent reports phase,
  status, position and idleness as above; sends none of the five requests named under Rights; sets `"review"` when it
  has finished a task and stays in the session until a person has saved or discarded; and leaves a session after 30
  minutes in `"waiting"` without a task (it joins again for the next one).
- **Without the capability:** A server without `agents` does not know `client.kind`, ignores the field (unknown fields)
  and would treat an agent as a human. An agent therefore joins only servers whose `info` names `agents`, and creates no
  session on others. A DZSTUDIO on a server without the capability never sees `kind`; for it there are no agents there.
  A DZSTUDIO that does not know agents yet, on a server with the capability, ignores `kind`, `phase` and `status` and
  shows an agent as a person with its user's name; a server need not require a minimum version for that.

```
{"protocol":"1.0","token":"…","requestId":"a8d0k2m4p6r8t0v2","sessionId":"s8Kq2","instanceKey":"q7w2e9r4t1",
 "client":{"name":"dzstudio-mcp","version":"0.1.0","kind":"agent","agentName":"Builder"}}
{"protocol":"1.0","token":"…","sessionId":"s8Kq2","participantId":"p5","after":131,"waitMs":5000,
 "versions":{"participants":4,"locks":58,"live":12,"notices":2,"saving":9},
 "presence":{"position":[4100.0,182.5,10200.0],"idleSeconds":14,"phase":"working",
             "status":"currently building a military complex at Krasno airfield"}}
dzcollab 1
{"ok":true,"seq":131,"more":false,"ops":[],
 "presence":[{"participantId":"p5","state":"active","idleSeconds":14,"position":[4100.0,182.5,10200.0],
              "lastSeenSeconds":0,"phase":"working","status":"currently building a military complex at Krasno airfield"}]}
```

## Sending operations: ops/send

| Request field | Type | Meaning |
|---|---|---|
| `sessionId`, `participantId` | IDs | |
| `ops` | list, at most 200 entries | in `clientSeq` order |
| — `clientSeq` | number from 1 | running number of this participant's operation in the session, without gaps; on returning it continues as before |
| — `opId` | ID ≤ 64 bytes | ID of the operation |
| — `system` | boolean, may be missing (default `false`) | `true` for an operation with origin system (such as the consequences of a save); does not count as an unsaved change |
| — `liveId` | text, may be missing | this operation ends the live state `liveId` (Live states) |
| — `part`, `parts` | numbers, may be missing | only if the operation is larger than 1 MB: this part (from 0) out of how many; each part at most 1 MB; all parts of an operation carry the same `clientSeq` |
| — `chain` | `{"index","count"}`, may be missing | only on a link of a chain (Large steps: chains): this link (from 0) out of how many (2 to `maxChainOps`) |
| — `pieces` | list of texts | the pieces of the operation's text form (or of this part) |

| Response field | Type | Meaning |
|---|---|---|
| `results` | list of `{"clientSeq","seq"}` | for each complete operation its sequence number; parts without the last part and links of an incomplete chain do not appear; with the last link all links of the chain appear (Large steps: chains) |
| `nextClientSeq` | number | the next expected `clientSeq` |

Rules for the server: It processes the entries in their order. `clientSeq` lower than `nextClientSeq`: a repetition —
it names the `seq` assigned earlier and appends nothing. Equal: append (after the last part), assign the next sequence
number. Higher: abort with `client_seq_gap` and `expectedClientSeq`; what came before the error stands. An `opId` it
already knows (for links of a chain: `opId` and `index`) under a new `clientSeq` it does not append again (it names
its `seq`). An operation over `maxOpBytes` it rejects with `too_large`; what came before stands. **Parts:** An
operation in parts is complete when its last part has been accepted (all `parts` parts are there); only then does the
server append it, and only then does `nextClientSeq` rise above its `clientSeq`. Until then, a further part with this
`clientSeq` (= `nextClientSeq`) is not a repetition but is accepted; the same part twice changes nothing. Accepted
parts of an operation that is not complete yet are kept like the links of a chain, until the participant leaves, is
removed or is taken out (not already after 30 s without contact), and across a restart (Duties of a server › Restart).
**Batch:** The operations that one request makes complete are appended directly one after another, with no operation
of anyone else in between. The server changes no pieces. It also passes on operations of a participant who currently
holds no lock — whether they hold is decided by the preconditions at the clients.

```
{"protocol":"1.0","token":"…","sessionId":"s8Kq2","participantId":"p3",
 "ops":[{"clientSeq":7,"opId":"k3f9a0x2mq-41","liveId":"k3f9a0x2mq-L3","pieces":["{\"textform\":1,\"op\":{\"id\":\"k3f9a0x2mq-41\",\"type\":\"placement:edit\",\"tv\":1,…"]}]}
```

## Large steps: chains

An operation is at most `maxOpBytes` (3.5 MB) large, so that it always fits whole into one response to `poll`. An
operation whose text form is larger — in practice a compound one: a large import, pasting very many objects, the undo
of these — is sent by the client as a **chain**: it splits the sequence of its pieces at piece boundaries into *k*
consecutive sections of at most `maxOpBytes` each (2 ≤ *k* ≤ `maxChainOps` = 16) and sends each as an operation of its
own, a **link**: with consecutive `clientSeq`, the same `opId` (the ID of the compound operation), on every link the
same `system` and `liveId`, and with `chain` (`index`, `count`). The concatenation of the pieces of all links in the
order of `index` is the text form of the one operation. For the core it stays one operation: applied whole or not at
all, one undo step, one ID.

Rules for the server:

- A chain is **open** from its first link (`index` 0) to its last (`index` = `count` − 1). The next link of an open
  chain — also in a later request — is not a new batch and never yields `batch_pending`. An operation without `chain`
  while a chain of this participant is open: `batch_pending`. These yield `bad_request`: a first link with `index` ≠ 0;
  a link whose `index` is not the next one, or whose `opId`, `count`, `system` or `liveId` differs from that of the
  first link; `count` below 2. `count` above `maxChainOps`: `too_large`. As always, what came before the error stands.
- It appends the links only when the last one is complete, then all directly one after another with consecutive
  sequence numbers, with no other operation in between — like a batch. Until then `ops/send` does not name them in
  `results`; `nextClientSeq` counts on per link as soon as the link is complete (a link in parts with its last part).
  A repetition of an accepted link that is not appended yet changes nothing and does not appear in `results` either.
  The response to the request that makes the last link complete names **all** links of the chain in `results`, each as
  `{"clientSeq","seq"}`, in the order of `index`, including those from earlier requests; a later repetition of a link
  names that link's own `seq`.
- `poll` delivers every link with its `chain`; the links of a chain may be spread over several responses (the cursor is
  a sequence number), but each link always whole.
- A chain counts as one operation in `unsavedCount`. A checkpoint (`snapshot/put`) or a save (`project/save`) with a
  `seq` **inside** a chain (the sequence number of a link other than the last) is `bad_request`.
- Links it has not appended yet are discarded only when the participant leaves, is removed or is taken out (not already
  after 30 s without contact); it keeps them across a restart (Duties of a server › Restart).

Rules for the client:

- It applies a chain only when it has all links, as one operation at the position of its last link; because the links
  stand gap-free one after another, this is the same position for all of them. As `after` for `poll` it takes the
  sequence number of the link received last and keeps the received links until the last one; as “last applied sequence
  number” (`resume`, checkpoint, save) it never takes a number inside a chain, but the one before its first link.
- The links of its own chain go one after another over the send channel, one or more per request, with one unconfirmed
  request at a time (Duties of the client › One batch in flight); only the response to the request with the last link
  names their `seq`, and then that of all links. It holds back other participants' operations, as with any batch,
  until the poll has reached the chain.
- A step whose text form would be larger than `maxChainOps` × `maxOpBytes` (56 MB) does not occur in a session.

## Fetching what is new: poll

Every participant keeps exactly one poll open: “What is new after sequence number N?”. The server answers as soon as
there is something new, otherwise empty after `waitMs`. The poll is also the sign of life and carries the presence.

| Request field | Type | Meaning |
|---|---|---|
| `sessionId`, `participantId` | IDs | |
| `after` | number | cursor: the highest sequence number that the client has applied or received as a link of a chain (Large steps: chains) |
| `waitMs` | number 0–8000, default 5000 | how long the server waits at most; 0 = answer at once |
| `maxBytes` | number, may be missing (default 2 MB, at most 4 MB) | limit for the response; the fields other than `ops` always come whole or not at all (they stay under 2 MB through the limits on locks; see below), `ops` fill the rest |
| `versions` | `{"participants","locks","live","notices","saving"}`, numbers | what the client already knows (initially 0) |
| `presence` | object | `position` (3 numbers, camera or player character, m), `idleSeconds` (seconds since the last input in DZSTUDIO); from an agent also `phase` and `status`, and `position` may then be missing (Agents) |
| `saving` | object, only from the host of a `local` session, may be missing | `state` (`"saved"`, `"unsaved"`, `"saving"`, `"failed"`), `unsavedCount` |

The server answers **at once** if one of these applies, otherwise after `waitMs`: there are operations after `after`;
one of the versions is higher than the one sent; the session has ended or the participant was removed;
`checkpointWanted` is new for this participant. Position and idleness of others do not wake the poll; they are in
every response.

| Response field | Type | Meaning |
|---|---|---|
| `ops` | list | operations with `seq` > `after`, gap-free and in order, until `maxBytes` would be reached, but always at least one if there is one; each **whole**, with all its parts in the same response, even if it alone exceeds `maxBytes`: `seq`, `participantId`, `opId`, `system` (may be missing), `liveId` (may be missing), `part`/`parts` (for large ones), `chain` (for links), `pieces`. Including the client's own. |
| `more` | boolean | there are more; the client asks again at once with `waitMs: 0` |
| `seq` | number | highest assigned sequence number |
| `participants` | object, only if the version is higher | `version`, `list` per participant: `participantId`, `userId`, `name` (≤ 64 bytes), `colour` (1–8), `role`, `canInvite`, `canSave`, `canManage`, with the optional capability `agents` also `kind` (`"studio"` \| `"agent"`) and for an agent `agentName` (may be missing, Agents), in order of joining |
| `presence` | list, always | per participant `participantId`, `state` (`"active"`, `"idle"` from 300 s without input, `"interrupted"` 10–30 s without contact, `"disconnected"` from 30 s; if several apply, `disconnected` wins over `interrupted` over `idle`), `idleSeconds` (last reported plus the time since that report), `position` (last reported; missing for an agent that has reported none), `lastSeenSeconds` (since the last contact, Duties of a server), for an agent also `phase` and `status` (last reported, Agents) |
| `saving` | object, only if the version is higher | `version`; for `local`: `state` as reported by the host, or `"none"`, `unsavedCount` as reported, `savedSecondsAgo` (since the reported `state` last changed to `"saved"`; missing if never). For `hosted`: `state` (`"saved"`, `"unsaved"`, `"saving"` during a `project/save`), `savedSeq` (up to which sequence number it was last saved), `unsavedCount` (operations after `savedSeq` without `system: true`), `savedBy` (`{"userId","name"}`, missing if nobody has saved in the session yet), `savedSecondsAgo`. The version rises with every change except the counting on of `savedSecondsAgo`. |
| `locks` | object, only if the version is higher | `version`, `holders` per participant with locks: `participantId`, `lapsed` (boolean), `elements` (list of IDs). The version also rises when `lapsed` of a holder changes (300 s without input reached, or they give input again) |
| `live` | object, only if the version is higher | `version`, `states`: per participant the live state accepted last (Live states) |
| `notices` | object, only if the version is higher | `version`, `list` of the notices to this participant since the version sent: `kind` (`"locks_released"` with `by`, `"locks_taken"` with `by` and `count`, `"participant_removed"` with `participantId`, `name` and `by`, `"attachment_wanted"` with `checksum`, Attachments); `by` and `name` are display names ≤ 64 bytes. A `participant_removed` comes in the same response as the participant list without the target; if a participant is missing without this notice, they have left the session |
| `checkpointWanted` | boolean, may be missing | to one participant: please upload a checkpoint (Snapshots) |

If the first operation and the other fields together do not fit into 4 MB, the server leaves out `participants`,
`locks`, `live`, `notices` and `saving` and sets `more: true`; because the client then does not raise their versions,
they come with the next response. That way a response with an operation of `maxOpBytes` stays under 4 MB.

`poll` reports an ended session as the error `session_ended` with `reason`: `"host_ended"`, `"host_left"`,
`"host_gone"` (`local` only), `"discarded"` (`hosted` only, with `by`), `"server"`. The server must answer like this for
10 minutes; after that it may say `session_unknown`. To a removed participant it answers `not_participant` with
`reason: "removed"`.

```
dzcollab 1
{"ok":true,"seq":120,"more":false,
 "ops":[{"seq":119,"participantId":"p2","opId":"m1x0c7r2ab-88","pieces":["{\"v\":1,…}"]},
        {"seq":120,"participantId":"p3","opId":"k3f9a0x2mq-41","liveId":"k3f9a0x2mq-L3","pieces":["…"]}],
 "presence":[{"participantId":"p2","state":"active","idleSeconds":2,"position":[6512.0,210.4,9300.0],"lastSeenSeconds":1}],
 "saving":{"version":9,"state":"unsaved","savedSeq":112,"unsavedCount":7,
           "savedBy":{"userId":"u17","name":"Ben"},"savedSecondsAgo":48},
 "locks":{"version":57,"holders":[{"participantId":"p2","lapsed":false,"elements":["m1x0c7r2ab-12"]}]}}
```

## Locks: locks/set, locks/release

**`locks/set`** sets the **whole** set of elements the participant wants to lock (their selection).

| Request field | Type | Meaning |
|---|---|---|
| `sessionId`, `participantId` | IDs | |
| `lockSeq` | number from 1 | rises with every new set of this participant |
| `elements` | list of IDs, at most 20,000 | the wanted set; empty = release everything. A session holds at most 25,000 locked elements in total; whatever would go beyond that is named by `denied` with `"holder": null` |

Response: `lockSeq` (the one applied last), `granted` (list), `denied` (list of `{"element","holder"}`), `version`
(version of the locks after this request). Rules:

- A request with a `lockSeq` lower than or equal to the one applied last changes nothing; the response names the state
  as of that last applied one.
- Elements that nobody else holds go to the requester. Elements that someone else holds do not go to the requester
  (`denied`) — unless that lock has lapsed (`idle`, 300 s without input): then the element passes to the requester,
  and the previous holder gets the notice `locks_taken`.
- What is missing from the new set, the server releases. Elements it does not know (such as ones just placed) it grants
  like free ones; it need not check IDs.
- After 30 s without a request from a participant, the server releases all their locks; when they come back, they set
  them again with `locks/set`. If they leave the session or are removed, the same happens at once.
- The server never changes another participant's lock, except through lapse, `locks/release`, leaving, removal or 30 s
  without contact.

**`locks/release`** with `requestId`, `sessionId`, `participantId` (the one releasing) and `target` (participant)
empties the target's set; a repetition with the same `requestId` changes nothing more, so that it does not empty a set
the target has set again in the meantime; only someone with `canManage`, otherwise `not_allowed`. The target gets the
notice `locks_released`, even if it held nothing; a `target` that is not (or no longer) in the session yields `ok`
without effect. The target's next `locks/set` with a higher `lockSeq` applies normally again.

```
{"protocol":"1.0","token":"…","sessionId":"s8Kq2","participantId":"p3","lockSeq":12,
 "elements":["k3f9a0x2mq-7","m1x0c7r2ab-12"]}
dzcollab 1
{"ok":true,"lockSeq":12,"granted":["k3f9a0x2mq-7"],"denied":[{"element":"m1x0c7r2ab-12","holder":"p2"}],"version":58}
```

## Live states: live/put

Live states are not operations: the server does not put them in the log, keeps per participant only the one accepted
last, and passes it on with `poll`.

| Request field | Type | Meaning |
|---|---|---|
| `sessionId`, `participantId` | IDs | |
| `liveId` | text ≤ 64 bytes | one gesture; new for each gesture |
| `n` | number from 1 | rises with each live state of the gesture |
| `mode` | `"values"` \| `"transform"` \| `"end"` | |
| `values` | list, at most 100, with `values` | per element `element`, `position` (3 numbers, m), `ypr` (yaw, pitch, roll, degrees), `scale` |
| `transform` | object, with `transform` | `pivot` (3 numbers), `translate` (3 numbers), `yaw` (degrees): applies to all elements the sender currently holds — new position = pivot + rotation about the vertical axis by `yaw` applied to (old position − pivot) + translation; new yaw = old yaw + `yaw` |
| `end` | `"commit"` \| `"cancel"`, with `end` | gesture finished; with `commit` an operation with this `liveId` follows |

Response: `accepted` (boolean). Rules for the server:

- It accepts a live state only if at least 180 ms have passed since the last accepted one of the same participant,
  whatever the gesture, and `n` is higher than in the last accepted one with the same `liveId` (a new gesture starts at
  1 again); otherwise `accepted: false` (not an error), also for a repeated `liveId` + `n`. It always accepts an `end`
  (`accepted: true`), without the 180 ms gap and without checking `n`, also a repeated one; the repetition has no
  further effect. A client sends one at most every 200 ms (5 per second).
- With `cancel` it deletes the live state at once. With `commit` it keeps the last one until it appends an operation
  with this `liveId`, and then deletes it; if that operation does not come within 10 s, it deletes it anyway. After
  30 s without contact from the participant it deletes it as well.
- Every change raises the version `live`; `poll` delivers per participant `participantId`, `liveId`, `n`, `mode` and
  `values` or `transform` (with `commit`, the last one with `committed: true`).

For the object in hand and for brush strokes the client sends no live states.

## Invitations: invite/create, invite/redeem

**Code:** 8 characters from `ABCDEFGHJKLMNPQRSTUVWXYZ23456789` (without I, O, 0, 1), rolled by the server at random and
not guessable (at least 40 bits). Shown as `XXXX-XXXX`; on redeeming, upper and lower case, spaces and hyphens do not
count. A code is valid for exactly one person and only for the session it was issued for, at most 24 hours after it
was issued and never beyond the session's end. What it allows (joining, and joining again after leaving on one's own)
ends the same way; whoever is in the session by code stays in it until they leave or are removed.

| Endpoint | Request | Response |
|---|---|---|
| `invite/create` | `requestId`, `sessionId`, `participantId`, `forUserId` (may be missing; only with the optional capability `invite.user`) | `code`, `expiresInSeconds`, `forUserId` (if set). With `forUserId` the session appears in that user's `sessions/list` as `invited`, without redeeming; the code still works, but only for them — if someone else redeems it: `invite_used`. The server need not check `forUserId`; the invitation for a user it does not know takes effect as soon as someone with this `userId` logs in. Only someone with `canInvite`, otherwise `not_allowed`. |
| `invite/redeem` | `requestId`, `code` | `sessionId`, `kind`, `projectName`, `map`, `host` (`local` only), `requires` (may be missing, as on creation). After that, the redeemer may join this session and join again, limited as above. Errors: `invite_invalid`, `invite_expired`, `invite_used` (redeemed by someone else, or invalid after removal), `session_ended`. The same user redeeming the same code again gets the same response, as long as they were not removed. |

The server must limit failed attempts at redeeming: every redeem with `invite_invalid`, `invite_expired` or
`invite_used` counts as a failed attempt. If a user has 10 failed attempts in the last 60 s, the server answers every
further redeem with `throttled` and `retryAfterMs` (until the oldest of them is 60 s old); attempts rejected this way
do not count.

## Removing: participant/remove

Request: `sessionId`, `participantId` (the one removing), `target` (participant). Only someone with `canManage`,
otherwise `not_allowed` — except a user removing their own agent (optional capability `agents`, Agents); nobody
removes themselves or the host of a `local` session (`bad_request`). Response: `ok`.
The server

- takes the target out of the session, releases their locks and deletes their live state; operations that are already
  appended stay in the log, and it rejects further `ops/send` from the target;
- answers the target's every further request in this session with `not_participant` with `reason: "removed"` and `by`,
  a waiting poll included, at once;
- invalidates every invitation the target redeemed; they come back only with a new invitation or a right to the
  project;
- gives all others the notice `participant_removed`.

Repeated, or for a target that is already removed, has left or was taken out: `ok` without effect. A `target` that
never existed in the session: `bad_request`.

## Saving: project/save

Only with `kind: "hosted"` (optional capability `hosted`). The client builds all files of the project from its state
after sequence number `seq` and sends them in **one** request; the server writes them into the project store in one
step — whole or not at all.

| Request field | Type | Meaning |
|---|---|---|
| `requestId`, `sessionId`, `participantId` | | |
| `seq` | number | sequence number up to which the state of the files reaches; the client has applied all operations up to it and has no unconfirmed operation of its own |
| `files` | list | all files the project consists of in the editor, except those of “unreadable” groups (they are in no list and stay untouched), each with `name` (text ≤ 200 bytes, the name of the file at the storage provider; at dzbot its key or `"manifest"`), `pieces` (content) and `checksum` (text ≤ 200 bytes: SHA-256 of the bytes the client sends, Save checksums) |
| `deleted` | list of names, may be missing | files the project had at the last save and no longer has |
| `force` | boolean, may be missing | `true`: write even if the project store was changed from outside; the server then does not compare the request's `merged`, but it does compare the checksums of the files (Errors › Order for project/save) |
| `merged` | list, may be missing | per merged state that the write changes: `target` (text ≤ 200 bytes) and `checksum` (text ≤ 200 bytes), the expected checksum (Save checksums); only to a server whose store merges, with the provider plugin that forms them (at dzbot, the dzbot plugin) |

Response: `savedSeq` (up to where it is saved now), `hostedRevision` (new state of the project store), `superseded`
(boolean), plus, with `superseded: false` (with `true` they are missing):

| Response field | Type | Meaning |
|---|---|---|
| `revisions` | list | per file from `files`, not for `deleted`: `name`, `revision` (text ≤ 200 bytes, for the consequences of the save at the client) and `checksum` (text ≤ 200 bytes: checksum of the bytes the server stored, Save checksums) |
| `merged` | list | per merged state of the project store after the write: `target` (text ≤ 200 bytes) and `checksum` (text ≤ 200 bytes: the server's checksum, Save checksums) |

Rules for the server:

- Only someone with `canSave`, otherwise `not_allowed`. `seq` above the highest assigned sequence number:
  `not_confirmed`; `seq` inside a chain (Large steps: chains): `bad_request`.
- It checks the checksum of every file (Save checksums), also with `force`; if one does not match, it writes nothing:
  `checksum_mismatch`.
- `seq` lower than or equal to the previous `savedSeq`: it writes nothing and answers `superseded: true` with the
  previous state — a later save already contains everything. Exception: the first save of a new project (New project
  on the server; empty `hostedRevision` at creation) writes even with `seq` 0.
- If the project store has been changed from outside since the session's last save (or since `hostedRevision` at
  creation) (also directly, bypassing the server, Projects on the server › Rules for the server) and `force` is missing,
  it writes nothing: `store_changed` with the changed files. This includes a file that is not in `files` but
  contributes to a merged state that the write changes (at dzbot, another delta on the same target path). If it does
  not know the `hostedRevision` from creation (from another store, or empty for a project the store already has), every
  file counts as changed from outside; empty for a project the store does not have yet means unchanged.
- Without `force` it then checks the merged states in `merged` (Save checksums); if one does not match, or one that it
  must check is missing, it writes nothing: `checksum_mismatch`. With `force` it does not compare `merged`, nor a target
  path to which a file outside `files` contributes that has changed since the session was created (Errors › Order for
  project/save).
- Otherwise it writes `files` and deletes `deleted` in one step; files of the project store that are in neither list
  stay untouched. If any part fails, none of it holds (`internal`).
- After that: `savedSeq` = `seq`, `savedBy` = the one who saved, and the version of `saving` rises, so that every
  waiting poll answers at once. While writing, it reports `state: "saving"`.
- It never writes what it keeps from the session log into the project store.
- It stores every file byte for byte as `files` names it (the concatenation of its pieces); it rounds nothing and
  reformats nothing. It forms the checksums over the bytes it stores and over the merged state the write produces, not
  from the checksums of the request (Save checksums); the response names the same values in `revisions` and `merged`.

The request must fit into `maxRequestBytes`; otherwise `too_large`. The client checks the size beforehand against
`info` › `limits` and does not send a request that is too large. After success with `superseded: false` the client
submits the consequences of the save (with the `revisions`) with `system: true` through `ops/send`, after it has
uploaded the new loot files as attachments (Attachments); with `superseded: true` it submits nothing and uploads
nothing — the consequences come from the later save. If the response does not arrive, it repeats unchanged; 30 s after
the first send it shows the save as failed, but keeps repeating in the send channel until a response comes or 10
minutes are over (that is how long the server keeps the response, Retries and timeouts). If the response then comes
with `superseded: false`, it still submits the consequences and shows the save as having succeeded after all; if
`checksum_mismatch` comes, it submits nothing. Per project, a client has at most one `project/save` in flight: it
sends a new one only when the previous one is decided (response, or 10 minutes over). What the server has actually
written is shown by `poll` › `saving`.

```
{"protocol":"1.0","token":"…","requestId":"q0w9e8r7t6y5u4i3","sessionId":"s8Kq2","participantId":"p3","seq":131,
 "files":[{"name":"f-basis-nord","pieces":["{\"Objects\":[…"],"checksum":"9f2c…e41a"},
          {"name":"manifest","pieces":["{…}"],"checksum":"03b7…5d90"}],"deleted":[]}
dzcollab 1
{"ok":true,"savedSeq":131,"hostedRevision":"m-418","superseded":false,
 "revisions":[{"name":"f-basis-nord","revision":"77","checksum":"9f2c…e41a"},
              {"name":"manifest","revision":"418","checksum":"03b7…5d90"}],
 "merged":[{"target":"f-basis-nord","checksum":"9f2c…e41a"},{"target":"manifest","checksum":"03b7…5d90"}]}
```

```
dzcollab 1
{"ok":false,"error":{"code":"checksum_mismatch",
 "mismatches":[{"kind":"file","name":"f-basis-nord","expected":"9f2c…e41a","actual":"5a01…c3d7"}]}}
```

**Save checksums:** Whether the files arrive intact is checked by the **server**, before it writes; the client reads
nothing back from the project store afterwards (in the game, reading large files in a second time costs too much) and
compares nothing afterwards.

- **Checksum of a file** (`files` › `checksum`): SHA-256 over the content of the file in UTF-8 (the concatenation of
  its pieces), as 64 lowercase hex digits. The client forms it over the bytes it sends, continuously, while it builds
  the file and splits it into pieces — not in a separate pass over the finished file. The server forms it over the
  bytes it stores; because it stores them as they arrive (it rounds and reformats nothing), both agree if nothing was
  corrupted on the way.
- **Merged state** (`merged`): If the project store merges something from the files that is delivered only later (at
  dzbot, per target path the mission file with all deltas of the mod), the client sends, per merged state that the
  write changes, `target` and the expected checksum by the method of the provider plugin — at dzbot over entries or
  lines, without duplicates and without order; the provider plugin forms the values while building. The server forms,
  per such state, the checksum over the state the write produces (with the files of the request in place of the
  previous ones). Without a provider plugin for the server, the client sends no `merged`; a server that merges nothing
  (the reference server only stores files) ignores the request's `merged`.
- **In the step of writing:** The server forms its values before the write takes effect. If a value differs, if a
  merging store's merged state that the write changes is missing in `merged`, or if `merged` names a `target` for
  which the store forms no state, it writes **nothing** — no file, no deletion, no new revision; `savedSeq` and
  `savedBy` stay — and answers `checksum_mismatch` with every differing entry in `mismatches`. A file without
  `checksum` violates this document (`bad_request`). The checksums of the files belong to the stage Validity of the
  content and come before `superseded` and `store_changed`, also with `force`; the merged states it checks only after
  that and only without `force`, because a change from outside to a contributing file changes the state the write
  produces and is a `store_changed`, not a corrupted request (Errors › Order for project/save). With `force`, the
  merged state may therefore differ from the client's expectation; the response names the server's values in `merged`,
  and the client does not compare them.
- **Succeeded:** The response names the server's values in `revisions` and `merged` — without `force`, equal to those
  of the request. The client does not compare them again; they serve the conformance suite and debugging. In `merged`,
  a server that merges nothing names one entry per file from `files` with `target` = `name` and the same checksum as in
  `revisions`.
- **Failed** (`checksum_mismatch`): The client shows “Save failed: ‹file› did not reach the server intact”, submits no
  consequences of the save and uploads no attachments (Attachments); its revisions stay as they were. Because nothing
  was written, a later save with the same `seq` (and, like every new save, a new `requestId`) has nothing to supersede
  and writes. The other participants see nothing of this; “Saved by ‹name›” appears for them only after a save that the
  server has checked and written.
- With `superseded: true` the response names neither `revisions` nor `merged`. The response to a repetition of the same
  `requestId` after a success names the same checksums as the first; after `checksum_mismatch` the server keeps no
  response, and the repetition is checked anew (Retries and timeouts).

## Attachments: attachment/put, attachment/get, attachment/want

The raw text of whole files that are only read (the loot files of a map) is named by the project state only with name
and checksum; operations never carry it, and the text form of the whole state carries it as an attachment. A joining
participant therefore gets it with the snapshot (Snapshots). After a save with loot turned on, however, the
consequences of the save name new checksums whose text only the one who saved has; likewise “Turn on loot” in a
running session (also through undo or redo) names files whose text only the one who turned it on is sure to have. The
server passes this text on as an **attachment**: the same content that the text form of the state carries as an
attachment, addressed by its **checksum** (text ≤ 200 bytes, formed by the client, opaque to the server). The same for
`local` and `hosted` sessions.

**`attachment/put`** uploads an attachment in parts, like `snapshot/put`:

| Request field | Type | Meaning |
|---|---|---|
| `sessionId`, `participantId` | IDs | |
| `checksum` | text ≤ 200 bytes | checksum of the content, as the project state names it |
| `seq` | number | sequence number the attachment belongs to: when uploading after a save, the `seq` of that save (for `local`, the host's last applied sequence number), otherwise (before “Turn on loot”, on `attachment_wanted`) the last applied one |
| `bytes` | number | size of the content in UTF-8, at most 16 MB (16,777,216 bytes, fixed in 1.0); above that `too_large` with `maxBytes` |
| `part`, `parts` | numbers, `part` from 0 | this part out of how many; per part, pieces of at most 1 MB together (counted as in Transport › Sizes); an empty file has one part without pieces |
| `pieces` | list of texts | the pieces of this part |

Response: `complete` (boolean: the attachment with this checksum is completely there). The same part twice changes
nothing. If an attachment with this checksum is already completely there, the request changes nothing and answers
`complete: true`; the client then sends no further parts. If `parts` or `bytes` differ from an upload of the same
checksum that has been started: `bad_request`. Any participant may upload; the server checks neither the checksum nor
the content.

**Keeping:** The server keeps a complete attachment together with the point of the log at which it was uploaded: at
least as long as it keeps the log from this `seq` (Duties of a server), at least 10 minutes after the upload, and
across a restart (Duties of a server › Restart). If the same attachment is uploaded again for a later `seq`, the later
one counts. It discards incomplete parts when the uploader leaves, is removed or is taken out. With the end of the
session, its attachments are gone. After that it may answer `attachment_unknown`.

**`attachment/get`** with `sessionId`, `participantId`, `checksum` and `part` (from 0, may be missing, then 0) returns
`checksum`, `bytes`, `part`, `parts` and `pieces` of this part, the way project/load returns a file; for an attachment
that is not completely there, `attachment_unknown`; for a `part` out of range, `bad_request`.

**`attachment/want`** with `sessionId`, `participantId` and `checksum` asks for an attachment that the server does not
have (any more). Response: `ok`. The server gives the notice `attachment_wanted` with this `checksum` (the version
`notices` rises, a waiting poll answers at once) to the participant who last uploaded this checksum completely (after a
save, the one who saved — for `local`, that is the host; after “Turn on loot”, the one who turned it on); if that
participant is no longer in the session, or nobody ever uploaded it, to all participants except the one asking. The
same for `local` and `hosted`.

If the attachment has become complete in the meantime, it gives no notice. Per participant and checksum it gives a
notice at most every 10 s; a request within that time yields `ok` without effect.

Rules for the client:

- **Before an operation with files:** If the consequences of a save name checksums of new loot files, the one who
  saved (for `local`, the host after saving to disk; for `hosted`, after `project/save` with `superseded: false`)
  uploads each of them with `attachment/put`, in the send channel **before** the `ops/send` with the consequences.
  Likewise, whoever turns on loot (also through undo or redo) uploads every file this operation names, before its
  `ops/send`. That way the server has the text before anyone sees the checksum; if the first part answers
  `complete: true`, the rest is dropped.
- **If a text is missing:** If the client's own state, after applying an operation or after opening from a snapshot,
  names a checksum for which the client has no text, it fetches the text with `attachment/get`, part by part through
  the send channel, and checks the checksum over the fetched bytes. On `attachment_unknown` or a wrong checksum it sends
  `attachment/want` and then tries `attachment/get` again every 5 s, a new request no earlier than after 60 s. Until it
  has the text, loot is view-only for it. The same applies if a snapshot lacks an attachment on joining.
- **On `attachment_wanted`:** If the client has the text for this checksum, it uploads it with `attachment/put`
  (`seq` = last applied sequence number); otherwise it does nothing.

```
{"protocol":"1.0","token":"…","sessionId":"s8Kq2","participantId":"p2","checksum":"c-8f3a…","part":1}
dzcollab 1
{"ok":true,"checksum":"c-8f3a…","bytes":1874211,"part":1,"parts":2,"pieces":["\t\t<container name=\"lootFloor\">…"]}
```

## Projects on the server: project/list, project/revision, project/load

Only with the optional capability `hosted`. With these, DZSTUDIO finds and opens a project from the project store of
any server that offers the capability, without a provider plugin of its own for it (at dzbot, the dzbot plugin loads
over its own connection; as part of the capability such a server must offer them too, DZSTUDIO just does not use them
then). All three endpoints change nothing; the client may repeat them freely. The server names `hostedRef` in
`project/list`; it chooses `provider` and `key`, and the client passes both on unchanged (only for a new project, New
project on the server, does it generate the `key`). Whoever has rights to the project may load it (Role of the
server), otherwise `not_allowed`; a `hostedRef` under which the project store has nothing: `project_unknown`. The
server checks the rights first (Errors › Order): whoever would have no right to this `hostedRef` gets `not_allowed`,
whether or not the store has something under it; only someone who would have the right gets `project_unknown`.

**`project/list`** names the projects of the project store that the requester may open, in pages. Request: `after`
(text, may be missing): the marker `next` of the previous page; missing for the first. Response: `projects`, at most
200 per page, per project `hostedRef`, `projectName`, `map`, `hostedRevision`, `canSave` and `sessionId` (may be
missing; missing if no session is running), all as in `project/revision`; `stores`, the same on every page, per
project store of the server `provider` (text ≤ 200 bytes) and `canCreate` (boolean: may this user create a new project
there, New project on the server); and `next` (text ≤ 200 bytes, may be missing): if there are more, the marker for
the next page. The server chooses the order; it stays fixed while paging. A project that is added or removed between
two pages may be missing or appear twice; the client lists each `hostedRef` once. A marker is valid for at least 10
minutes; an invalid one: `bad_request`, and the client starts from the beginning.

```
{"protocol":"1.0","token":"…"}
dzcollab 1
{"ok":true,"projects":[{"hostedRef":{"provider":"collab","key":"basis-nord"},"projectName":"Basis Nord",
 "map":"chernarus","hostedRevision":"r12","canSave":true,"sessionId":"s8Kq2"},
 {"hostedRef":{"provider":"collab","key":"zaeune"},"projectName":"Zäune","map":"livonia","hostedRevision":"r3",
 "canSave":false}],"stores":[{"provider":"collab","canCreate":true}],"next":"p2-k7"}
```

**`project/revision`** with `hostedRef` names the state without transferring files:

| Response field | Type | Meaning |
|---|---|---|
| `hostedRevision` | text ≤ 200 bytes | the current state of the project store, as in session/create; it changes with every save, also from outside, and with every direct change in the store (Rules for the server) |
| `projectName` | text ≤ 200 bytes | name of the project at the server |
| `map` | text ≤ 64 bytes | map key as in session/create |
| `canSave` | boolean | may this user save, and thereby start a session |
| `sessionId` | ID, may be missing | the running session of this project, if the user may enter it (like sessions/list with `hostedRef`) |

**`project/load`** loads in two steps: first the **head** (without `file`), then every file in parts.

| Request field | Type | Meaning |
|---|---|---|
| `hostedRef` | object | as in session/create |
| `hostedRevision` | text, may be missing | for the head: missing for the current state; required when loading a file: the state from the head |
| `file` | text, may be missing | name of the file; missing for the head |
| `part` | number from 0, may be missing | only with `file`: this part; if missing, part 0 applies |

| Response field | Type | Meaning |
|---|---|---|
| `hostedRevision` | text ≤ 200 bytes | the state the response comes from |
| `projectName`, `map`, `canSave`, `sessionId` | | only for the head, as in `project/revision` |
| `files` | list, at most 5000, only for the head (that way the head stays under 4 MB; the server answers a project with more files with `too_large`, and it cannot be loaded this way) | per file `name` (text ≤ 200 bytes, as in project/save), `bytes` (size in UTF-8), `parts` (number of parts, at least 1) and `revision` (text ≤ 200 bytes, may be missing; like `revisions` in `project/save`) |
| `file`, `part`, `parts`, `pieces` | | only when loading a file: this part out of how many, with pieces of at most 1 MB together (counted as in Transport › Sizes); the concatenation of the pieces of all parts is the content of the file in UTF-8; an empty file has one part without pieces |

The files are exactly those that `project/save` writes: the same names and contents, byte for byte, among them the
manifest under the name `manifest` as a project file document with only `format`, `version` and `project`, as at dzbot;
its group entries name the other files' names in `file`. Files DZSTUDIO cannot read are in the list like the others
(“unreadable” groups).

**Rules for the server:**

- All responses for one `hostedRevision` describe the same state. If a save replaces a revision, the server keeps it
  available for `project/load` for at least another 10 minutes; after that it may answer `revision_unknown`.
- The 10 minutes apply only to revisions that the server replaced itself: with `project/save` or with a save through its
  own interface (at dzbot, the dzbot plugin). If an operator changes the project store directly, bypassing the server
  (such as overwriting a file by hand), that bypasses this protocol: the change appears like a save from outside as a
  new revision, at the latest in the next response that names a `hostedRevision`, and `project/save` recognises it as
  `store_changed`. The server then need not keep the replaced revision available; a part it can no longer deliver from
  it is answered with `revision_unknown`.
- A `hostedRevision` it cannot interpret (from another store, not in its form, nonsensical) it treats like one that is
  no longer there: `revision_unknown`, for the head as well as when loading a file.
- A file that does not exist in this revision, or a `part` out of range: `bad_request`.
- Like every request, it answers at most 5 s after it arrives.

Rules for the client:

- If the head names a `sessionId`, the client loads no files but joins the session.
- Otherwise it loads all parts of all files with the head's `hostedRevision`, at most two requests at the same time. On
  `revision_unknown` it starts once more from the head; if that fails again, opening fails. Then it opens the project
  from the files as from the project store (name and map from the head) and keeps `hostedRevision` and the `revision`
  per file as after a save, for `session/create` and `project/save`.
- Without a provider plugin of its own for this server (it was added by hand), a project opened this way is **always in
  a session**: if none was running, the client creates one after loading and before the project is open
  (`session/create` with `kind: "hosted"` and the head's `hostedRevision`), without saving first; without `canSave` it
  does not open a project that has no running session. If the server answers `already_running`, it joins that session
  and discards what it loaded. If creating or uploading snapshot 0 fails, opening fails, and what was loaded is
  discarded. A project on the server that is open on its own does not exist then; saving happens only with
  `project/save`.
- It fills the project browser with `project/list`, page by page, until a response names no `next`.

```
{"protocol":"1.0","token":"…","hostedRef":{"provider":"collab","key":"basis-nord"}}
dzcollab 1
{"ok":true,"hostedRevision":"r12","projectName":"Basis Nord","map":"chernarus","canSave":true,
 "files":[{"name":"manifest","bytes":2210,"parts":1,"revision":"12"},
          {"name":"f-basis-nord","bytes":1830400,"parts":2,"revision":"7"}]}
```

## Leaving, ending, discarding: session/leave, session/end

- **`session/leave`** (`sessionId`, `participantId`): The participant is out; their locks and their live state are
  gone. If they are the host of a `local` session, the session ends (`host_left`). With `hosted`, the session stays.
  Repeated: `ok`.
- **`session/end`**: for `local`, only the host; it ends for everyone (`host_ended`). For `hosted`, only with
  `canManage`: discards the unsaved changes — the server ends the session (`discarded`, with `by`) and deletes its log;
  the project store stays at the last save. Others get `not_allowed`.
- **End of a `hosted` session by itself:** If no participant is left in it and it has `unsavedCount` 0, the server
  ends it (`server`). Otherwise it keeps it without a time limit until someone saves or discards; whoever opens the
  project finds it through `sessions/list` with `hostedRef`.
- **Without contact:** For `local`, the session ends when the host has gone 300 s without a request (`host_gone`). For
  `hosted` there is no host who could be missing. A participant who is not a host may be taken out by the server after
  10 minutes without contact; after that it answers them `not_participant` with `reason: "timeout"`, and they can join
  again without `resume`.

## Retries and timeouts

If a request ends with a timeout or fails, the client does not know whether it arrived. It therefore repeats it
**unchanged**, and the server recognises the repetition:

| Request | Recognised by | Server answers the repetition |
|---|---|---|
| `ops/send` | `clientSeq`, plus `opId` | with the `seq` assigned earlier, without appending again |
| `locks/set` | `lockSeq` | with the state of the set applied last |
| `live/put` | `liveId` + `n` | `accepted: false`, no effect; with `mode: "end"` `accepted: true`, no effect |
| `snapshot/put` | `seq` + `part` | as the first time |
| `attachment/put` | `checksum` + `part` | as the first time |
| `session/create`, `session/join`, `invite/create`, `invite/redeem`, `project/save`, `locks/release`, `auth/name` | `requestId` per user | with the stored response (the same session, the same participant, the same code, the same save without a second write, no second release); if the first is still running, it waits for it or answers `busy` |
| `session/leave`, `session/end`, `participant/remove` | same effect (the target is an ID that is never assigned again) | `ok` |
| `attachment/want` | same effect (at most one notice per 10 s) | `ok` |
| `info`, lists, `poll`, `snapshot/get`, `attachment/get`, `project/list`, `project/revision`, `project/load` | change nothing | anew |

The server must keep request IDs with their response for at least 10 minutes — the **retry window**; no client
repeats for longer (Saving) —, and the last `clientSeq` and `lockSeq` of every participant for the whole session, both
also across a restart of the server (Duties of a server › Restart). A repetition after a restart gets the same response
as before, never a second append, save, release or issue.

In the retry window it keeps only **successful** responses (`ok: true`). A request with a `requestId` that it answers
with an error changes nothing; it processes the repetition with the same `requestId` anew, like a first request, and
the repetition may get a different response (such as success after `token_expired`, `busy`, or `store_changed` with
`force`).

## Duties of a server

A conforming server must, in addition to the rules of the individual endpoints:

- answer every request as set out in Transport: HTTP 200, header line, one JSON object, no text over 1000 bytes, errors
  in the body, within the response times;
- assign sequence numbers per session gap-free and in order of arrival, never reorder the operations of a participant,
  append those of one request as a batch with no others in between, append every operation exactly once and deliver it
  to all participants in order, including the sender;
- keep the pieces of operations, snapshots, files and attachments byte for byte and with their boundaries, and keep
  attachments and ask for them again as set out in Attachments;
- keep the session log in its own storage: the current snapshot and the log after it, plus the log from the cursor of
  every participant still in the session, for at least 10 minutes after their last contact (if the cursor lies inside
  a chain, from its first link), and for `hosted` enough that `unsavedCount` stays exact (such as from `savedSeq`); it
  may reject older ranges with `cursor_too_old`. What it stores the log with is its choice; none of it belongs in a
  project store;
- keep session prefixes unique within a session (`instance_key_taken`);
- count a participant's **contact** uniformly: contact is every incoming request from this participant, and a poll the
  server keeps open counts as contact until its response. Every period “without contact” (10 s, 30 s, 300 s, 10
  minutes) runs from the end of the last contact;
- handle locks, lapse (300 s), release after 30 s without contact, release with `canManage`, and live states (at most
  one per 180 ms) as set out in this document;
- assign colours 1–8 on joining: the lowest that no participant carries; if all eight are already carried by
  participants, the lowest among those carried by the fewest participants. With `resume` the colour stays;
- allow joining only with a right or an invitation for exactly this session, issue codes as set out in Invitations,
  limit failed attempts and invalidate the invitations of removed participants;
- give `canInvite` to everyone with rights to the project, for `local` also to everyone with a standing permission
  (Role of the server) from the host, and to nobody who came only by invitation; `canManage` for `local` to exactly the
  host;
- end a `local` session when its host leaves or is 300 s without contact, and report the end with its reason for 10
  minutes;
- with the optional capability `hosted`: keep a session with unsaved changes until it is saved or discarded, across a
  restart included; let at most one session run per `hostedRef`; write `project/save` in one step, check the checksums
  of Save checksums over what it stores beforehand and write nothing on a difference, recognise changes from outside as
  set out in Saving;
- with the optional capability `agents`: recognise and mark agents as set out in Agents (also by a token bound to an
  agent), never give them more rights than stated there, and let every user remove their own agents.

**Restart:** The server may lose `local` sessions on a restart (then `session_unknown`), but should keep them. What it
keeps of a session it keeps whole: log, snapshots, complete attachments, participants, sequence numbers, `clientSeq`
and `lockSeq`, invitations, save state and the stored responses of the retry window (Retries and timeouts), plus
accepted parts of an operation and links of a chain (Large steps: chains) that are not appended yet (the `clientSeq` of
a complete link already counts, that of an operation in parts only with its last part, Sending operations). It may lose
locks, live states and presence. Every version (`participants`, `locks`, `live`, `notices`, `saving`) is afterwards
higher than any reported before, so that every participant's next poll brings the state.

It may limit sizes and rates more strictly than stated here, but not below the minimum values of `limits` and not for
the values that are fixed there.

## Duties of the client

- **At most two requests** at the same time: the one poll (`poll`) and a send channel that sends all other requests one
  after another. In the send channel, the order in which things came about applies: a lock before the operation that
  needs it; operations in `clientSeq` order, bundled up to 200 per request; a waiting live state is replaced by a newer
  one instead of queuing. Together with the at most 4 requests of the dzbot plugin, that stays at most 6.
- **One batch in flight:** The client sends new operations only when the previous `ops/send` request has been
  answered; operations that come about in the meantime it collects for the next one.
- **Poll:** after every response, the next one at once (with `waitMs: 0` on `more: true`, otherwise 5000); after an
  error, no earlier than after 1 s.
- **Interruption:** It begins when a request fails, when a request of the send channel has no response on sending (the
  clock runs from sending) or when the poll is still open `waitMs` + 1 s after sending; it ends with the first
  successful response. The times 3 s and 30 s count from its beginning. The client repeats failed and timed-out
  requests unchanged (Retries and timeouts), in the send channel in the same order.
- **Applying in order:** The client applies the operations from `poll` strictly by `seq`, through the reconciliation
  slot, a chain (Large steps: chains) as one operation at the position of its last link. If it has a sent batch whose
  operations it has not yet seen in `poll`, it holds back other participants' operations until the poll has reached the
  batch. Then it has the core take back the unconfirmed own operations, apply the others' operations before them and
  apply its own anew, and marks the operations of the batch as confirmed. Because the batch stands in the log with no
  other operations in between, every sent own operation meets exactly the state that everyone has at its position: if
  it drops out, it drops out for everyone. Its ID then counts as known; if it comes in `poll`, the core does not apply
  it. If an operation not yet sent drops out, the client does not send it. That way every client has the same project
  state after sequence number N.
- **Change only with a lock:** The client submits an operation that changes existing objects only once it holds all
  these objects (confirmed in `granted`, or newly placed by its own operations that are not confirmed yet). Groups, loot
  and new elements need no lock.
- **Locks:** After every change of the selection it sends the whole set with a new `lockSeq`; it bundles at most 100 ms
  between two sets. What `denied` names, or what the server attributes to someone else in `locks`, it removes from the
  selection. If elements that were confirmed to it are missing in `locks` without a notice or 30 s without contact
  explaining it (a restart of the server), it sets its set again with a new `lockSeq`. Newly placed elements that are
  selected afterwards stand in a `locks/set` that comes **before** the `ops/send` creating them in the send channel —
  the server does not check IDs, so nobody locks them before their creator. For **undo and redo** it sends the set of
  the selection and all existing objects the step changes; if it gets all of them, it submits the inverse operation and
  then sets the set to the new selection, otherwise it sets it back to the previous selection.
- **Live states** at most every 200 ms and only on change; up to 100 objects `values`, above that `transform` if the
  gesture moves all of them alike, otherwise `values` for the first 100 of the selection. On release `end: "commit"`
  and the operation with the same `liveId`; on abort `end: "cancel"`. It shows others' live states only in the display,
  with 200 ms of gliding, until the operation with their `liveId` is applied or the live state disappears from `live`.
- **Upload a checkpoint** only on `checkpointWanted`, only without unconfirmed own operations, only with the text for
  every checksum its state names (if one is missing, it fetches it first, Attachments), and with the last applied
  sequence number as the snapshot's `seq` (never one inside a chain).
- **Attachments:** upload after saving and before “Turn on loot”, fetch missing ones and ask for them again, upload on
  `attachment_wanted`: Attachments › Rules for the client.
- **Saving** (`hosted`): only when all own operations are confirmed; `seq` = the last applied sequence number (never
  one inside a chain); the files from exactly this state, with the checksums formed while building in the request,
  which the server checks (Save checksums). With `local`, the host saves on their own and only reports `saving` in
  `poll`; no participant of a session silently reloads. At most one `project/save` in flight per project, until it is
  decided (Saving).
- **Counting unsaved changes:** with `hosted` every client shows `saving.unsavedCount`; with `local` the host shows
  their own count, the guests the reported one.
- **Returning:** After an interruption of 30 s or more, the client reports back with `session/join` with `resume`
  (cursor = last applied sequence number) before it sends what it collected, and takes role, rights and colour from the
  response. With `mode: "resume"` it carries on as before (send what was collected, set the lock of the selection
  again). With `mode: "snapshot"`, `cursor_too_old`, or `not_participant` with `timeout`, the connection is lost (“Away
  too long”): it sends nothing it collected any more; “Join again” sends `session/leave` for the old `participantId`
  (if it is still a participant) and joins again without `resume` — it never loads a new snapshot under the old
  `participantId`, because it does not know which of its `clientSeq` the server already has.
- **Display:** It shows `presence.state: "interrupted"` like `"active"`; with `hosted`, a participant with
  `canInvite: false` carries the badge “Guest”, unless they are an agent (`kind: "agent"`): DZSTUDIO shows them as an
  agent (“‹agentName› · ‹name›'s agent”, without `agentName` “‹name›'s agent”), with phase and status from `presence`,
  and offers “Remove” on the agents of its own user (the same `userId`) even without `canManage`. It takes the name of a removed participant for “‹name› was removed”
  from the notice `participant_removed`.
- **Reading** with a JSON reader that reads texts in full, or with the simple one, since no text exceeds 1000 bytes;
  the client assembles JSON for **writing** itself, as text, without text functions that cap at 8191 bytes.

## Acceptance tests

Against a test server that can delay, drop, duplicate, corrupt and count responses, and against the reference server;
plus a conformance suite that checks a third-party server against this document.

- **Header line:** A response with status 200 without a header line, and one with truncated JSON, yield “Server not
  reachable”; nothing of either is applied.
- **1000 bytes:** The conformance suite finds no text over 1000 bytes in any response of the reference server and of
  the dzbot server, and none that ends in the middle of a character. An operation with 10 KB of source text with
  umlauts arrives at all participants byte for byte the same, with the same piece boundaries.
- **Large content:** An import of 10,000 objects (over 1 MB) travels in parts; every participant applies it exactly
  once; a snapshot of 5 MB uploads and downloads in parts, and the downloaded one is equal as text to the uploaded one.
- **Sent twice:** The test server accepts `ops/send` but drops the response; the client repeats: every operation stands
  in the log exactly once, and the response names the same `seq`. The same with `session/create` (one session),
  `invite/create` (one code), `project/save` (one new revision) and `locks/set` (an older `lockSeq` that arrives later
  changes nothing).
- **Gap:** `clientSeq` 9 before 8: `client_seq_gap` with `expectedClientSeq: 8`; the client sends from 8, and the log
  has 8 before 9.
- **Batch:** Two clients each send 50 operations in one request at the same time: in the log, the 50 of one stand
  gap-free one after another, then the 50 of the other (or the other way round). No client ever has two `ops/send`
  unanswered (the test server's counter).
- **Same state:** Three participants, 300 mixed changes in quick succession, the test server delays at random between
  0 and 3 s: after things settle, all three project states are equal as text and equal to replaying the log on
  snapshot 0.
- **Dropped out for everyone:** A sets field F of a loot entry from 1 to 2 and sends; before A at the server: B sets F
  from 1 to 3, C from 3 to 1. A applies B's operation before C's arrives at A: A holds C back until the poll has reached
  A's batch; after that, A, B and C have the same value in F, equal to replaying the log, and nobody has applied A's
  operation twice.
- **Long poll:** With nothing new, `poll` answers empty after 5 s ± 0.5 s; another participant's operation wakes a
  waiting poll within 200 ms of its arrival; a change of position does not wake it, a save does. No client keeps more
  than two requests open at the same time (the test server's counter).
- **Lock:** Two `locks/set` on the same element: the one that arrived first gets it, the second names it in `denied`
  with the holder. The holder reports `idleSeconds` ≥ 300: someone else gets the element, the previous holder the
  notice `locks_taken`. 30 s without a request from a participant: their locks are free, `presence` names them
  `disconnected`. `locks/release` without `canManage`: `not_allowed`.
- **Live state:** 10 `live/put` in 1 s: at most 5 with `accepted: true`; `end: "commit"` and the operation with the same
  `liveId`: after that the live state has disappeared from `live`, in the same response as the operation or a later
  one, never earlier.
- **Invitation:** A code is 8 characters from the alphabet; “k7qm-2xpa” and “K7QM2XPA” redeem the same; a second user
  gets `invite_used`; after the end of the session `session_ended`; 24 hours after issuing `invite_expired`; 11 wrong
  codes in one minute: the eleventh gets `throttled`. `invite/create` from a participant who came by code:
  `not_allowed`.
- **Standing permission** (only servers that know it): The test server is set up so that A gives B a standing
  permission. A creates a `local` session: B's `sessions/list` names it with `access: "direct"`, without an invitation;
  B's `session/join` yields `role: "guest"`, `canInvite: true`, `canManage: false`, and B's `invite/create` succeeds. C
  without a permission does not see it and gets `not_allowed` on `session/join`. If B creates a `local` session, A's
  `sessions/list` does not name it. The permission is withdrawn while B is a participant: B's `poll` and `resume` keep
  succeeding; after B's `session/leave`, their `sessions/list` no longer names the session, and `session/join` yields
  `not_allowed`.
- **Removing:** After `participant/remove` the target's waiting poll ends within 1 s with `not_participant`,
  `removed`; their locks are free; their old code yields `invite_used`; the others get `participant_removed`.
- **End of local:** Host `session/leave`: the polls of the others end within 1 s with `session_ended`, `host_left`;
  this response stays for 10 minutes. Host 300 s without a request: `host_gone`.
- **hosted:** The creator leaves: the session runs on, no notice, nobody has `role: "host"`. Everyone leaves with
  `unsavedCount` 3: `sessions/list` with the `hostedRef` names the session with 0 participants, also after a restart of
  the server; after saving and everyone leaving, it is gone. `session/end` with `canManage`: the others get
  `session_ended`, `discarded`; the project store is unchanged.
- **Saving:** `project/save` with `seq` 131: the files stand in the project store in one step, `saving.savedSeq` is
  131, `unsavedCount` afterwards counts only operations above 131 without `system`. A second one with `seq` 129:
  `superseded: true`, nothing written. File changed from outside: `store_changed` names it, nothing written; with
  `force`, written. Without `canSave`: `not_allowed`. Before the first save the project store has no new revision, and
  after it only what `files` and `deleted` named is there.
- **New project:** `project/list` names in `stores` the reference server's project store with `canCreate: true` for a
  user with the right to save for every key, `false` for one without. `session/create` with this `provider`, a new
  `key` of 16 characters and `hostedRevision: ""`, snapshot 0 uploaded, `project/save` with `seq` 0:
  `superseded: false`, `savedSeq` 0, a non-empty `hostedRevision`; `project/revision` names it and the session's
  `projectName`, `poll` › `saving` names `savedBy`. A second `project/save` with `seq` 0: `superseded: true`, nothing
  written.
- **Save checksums, reference server:** `project/save` with the manifest and a file of 1.8 MB with umlauts: every file
  in `files` carries a `checksum` of 64 lowercase hex digits, equal to the SHA-256 the conformance suite forms over the
  concatenation of the pieces sent; the response names the same values in `revisions`, equal to the SHA-256 over the
  bytes `project/load` delivers afterwards; `merged` names per file `target` = `name` with the same checksum. The
  repetition with the same `requestId` names the same checksums; a `superseded: true` names neither `revisions` nor
  `merged`.
- **Save checksums do not match:** The test server corrupts one byte of a piece of a file on the way (or the
  conformance suite sends a wrong `checksum`): the server answers `checksum_mismatch`, and `mismatches` names exactly
  this file with `kind: "file"`, `expected` from the request and `actual` = SHA-256 of the bytes that arrived.
  `project/revision` afterwards names the previous `hostedRevision`, `project/load` delivers the previous bytes,
  `poll` › `saving` names the previous `savedSeq` and `savedBy`. The one who saved sees “Save failed: ‹file› did not
  reach the server intact”; the test server's counter afterwards sees no `ops/send` with `system: true` and no
  `attachment/put`, and the others see no “Saved by …”. Saving again without a new operation sends the same `seq` with
  a new `requestId`, without corruption: the server writes (no `superseded`, no `store_changed`), and a new revision is
  created. A file without `checksum`: `bad_request`, nothing written.
- **Checksum of a file before other errors:** `project/save` with a wrong `checksum` of one file and at the same time a
  file that was changed from outside (or with `seq` lower than `savedSeq`, or with `force: true`): `checksum_mismatch`
  with `kind: "file"`, not `store_changed` or `superseded`, nothing written. The same request with the same
  `requestId` but the right `checksum`: `store_changed`, or `superseded: true`, or written.
- **Save checksums at the dzbot server:** A `hosted` session of a mod with loot turned on; B moves a point in
  `mapgroupproto.xml` and rotates a garage, then saves: B's request names in `merged` the target paths of
  `mapgroupproto.xml` and `mapgrouppos.xml` with the checksums B's dzbot plugin formed while building; dzbot writes,
  although the delta appends the changed line, and the response names the same values in `merged`; B's DZSTUDIO reads
  no file afterwards and shows “Saved ‹time›”. The same request with a changed expected checksum for the target path of
  `mapgroupproto.xml`: `checksum_mismatch` with `kind: "merged"` and this `target`, no new revision in dzbot. Without an
  entry for this target path in `merged`: `checksum_mismatch` with this `target`, without `expected`.
- **Change from outside on the same target path:** The same session; in the web workbench, another file of the mod is
  changed that is not in B's `files` but is a delta on the target path of `mapgroupproto.xml`. B saves with the
  checksums from the session's state: `store_changed`, `changed` names this file, not `checksum_mismatch`; no new
  revision in dzbot. The same request with the same `requestId` and `force: true`: dzbot writes, although the merged
  state now differs from B's expectation; the response names dzbot's values in `merged`, and the other file in dzbot
  still has the content from outside. The same with `force: true` and a byte of a file from `files` corrupted on the
  way: `checksum_mismatch` with `kind: "file"`, nothing written. After the write with `force`, B moves another point and
  saves without `force` with the checksums from the session's state: dzbot writes, no `store_changed`, no
  `checksum_mismatch`. A request with a changed expected checksum for a target path and `seq` equal to `savedSeq`:
  `superseded: true`, no `checksum_mismatch`.
- **Returning:** After a 60 s interruption, `session/join` with `resume`: `mode: "resume"`, and the poll from the cursor
  delivers all missed operations; after 15 minutes without contact, `not_participant` (`timeout`) or
  `mode: "snapshot"`.
- **Versions:** A 1.0 client against a server that reports 1.3 and additional fields and capabilities: everything
  works; a server that speaks only major version 2: `protocol_unsupported`, or the client does not add it. Without
  `users` the client never asks `users/list`, without `hosted` never `project/save`.
- **Login:** `token_expired` leads to exactly one new token and one repetition with the same `requestId`; no token ever
  stands in an address.
- **Contact:** A participant who only sends polls with `waitMs` 8000 never appears to the others as `interrupted` or
  `disconnected`; if they stop completely, they count as `disconnected` 30 s ± 1 s after the last response to them, and
  their locks are free.
- **Lapse:** A holder reports `idleSeconds` 290 in a poll that the server keeps open for 8 s, and gives no new input
  afterwards: at the latest 11 s after this report the version of `locks` rises, and `lapsed` is `true`; if they then
  report `idleSeconds` 0, it rises again, and `lapsed` is `false`.
- **New elements:** A places a barrel and keeps it selected; the test server's counter sees A's `locks/set` with the
  barrel's ID before the `ops/send` that creates it; B, who clicks the barrel at once after it arrives, gets it in
  `denied`.
- **Release twice:** `locks/release` is accepted, the response dropped; the target then sets a new set; the repetition
  with the same `requestId` leaves the new set in place.
- **Join aborted:** `session/join` with no `poll` following, then `session/leave`: no one else ever sees the
  participant in `participants`.
- **Colours:** Nine participants one after another: colours 1–8, the ninth 1; if the one with 3 leaves, the next gets
  3.
- **Map:** `sessions/list` and `invite/redeem` name `map` byte for byte as on creation; a client with an unknown key
  does not join.
- **Removed:** After `participant/remove`, the others' response to `poll` names the notice `participant_removed` with
  the target's name in the same response in which the target is missing from `participants`.
- **Chain:** An import with a 12 MB text form travels as 4 links of at most 3.5 MB; in the log they stand gap-free one
  after another with no other operation in between, although another client sends at the same time; every participant
  applies the import exactly once and as one step, `unsavedCount` rises by 1, and the importer's undo takes it back
  completely. With `maxBytes` 1 MB each link comes whole in a response of its own.
- **Chain interrupted:** The test server interrupts a participant for 60 s after the response with the second link;
  after `resume` with the cursor before the first link, it applies the chain exactly once. An operation outside the
  chain before its last link: `batch_pending`; the next link in a request of its own, on the other hand, never; a link
  with a different `count` or a skipped `index`, and a checkpoint or save with the `seq` of the second link:
  `bad_request`. If the server restarts after the second of four links, it accepts the third and the fourth, and the
  chain stands whole and exactly once in the log.
- **Too large:** A single operation of 3.6 MB: `too_large`, and nothing of it stands in the log. The conformance suite
  finds more than 4 MB in no response, not even in one with a link of 3.5 MB at 25,000 locks (then `locks` is missing
  and comes with the next response, `more: true`).
- **Live state per gesture:** A new gesture with `n` 1, 200 ms after the last accepted live state of another gesture:
  `accepted: true`; the same `liveId` + `n` again: `accepted: false`.
- **Invitation for a user:** The code of an invitation with `forUserId`, redeemed by another user: `invite_used`; by
  the invited user: success. 10 wrong codes, then one more every second for 30 s: all `throttled`; 60 s after the first
  failed attempt a correct code goes through.
- **Restart:** The test server accepts `ops/send` and `project/save`, drops the responses, and the server restarts;
  the repetitions within 10 minutes get the same `seq` or the same save, every operation stands in the log once, and one
  revision is created in the project store. The first poll after that brings participants, locks and save state with
  higher versions; a client whose locks are missing sets them again.
- **One save at a time:** The test server holds back the response to a `project/save` for 45 s, the client saves again
  after 35 s: until the response, the test server counts no second `project/save` with a new `requestId`, only
  repetitions of the first; after that, the consequences of the first save come through `ops/send`, and then the second
  `project/save`.
- **Loading:** `project/load` of a project with a manifest and two files (one of them 1.8 MB): the head names three
  files with `bytes` and `parts`; the loaded files are byte for byte equal to those in the project store. If someone
  saves during loading, the remaining parts with the old `hostedRevision` keep delivering the old revision; after more
  than 10 minutes `revision_unknown` may come, and the client loads once more from the start. `project/revision`
  transfers no file and names the new revision after a save; if a session is running, it names its `sessionId`, and the
  client joins instead of loading. Without a right to the project: `not_allowed`; unknown `hostedRef`:
  `project_unknown`. Without `hosted` the client never asks `project/load`. A project with 5001 files: the head answers
  `too_large`.
- **Without dzbot:** DZSTUDIO without the dzbot plugin opens a project of the reference server from `project/list`:
  without a running session it loads with `project/load` and creates a session with the loaded `hostedRevision` at
  once; the session's first `project/save` yields no `store_changed`. A second user who opens the project joins this
  session. A user without `canSave` opens it only while a session is running. At no time is the project open at a
  client without a session.
- **Paging:** `project/list` with 450 projects: three pages (200, 200, 50), the last without `next`; every `hostedRef`
  exactly once. A marker older than 10 minutes may yield `bad_request`.
- **Numbers and version:** The server passes on the position 6512 as `6512.0` and 0.1 as `0.1`, never with an exponent.
  A request without `protocol` or with `"eins"`: `bad_request`; with `"2.0"` to a server only for 1:
  `protocol_unsupported`.
- **Chain, response:** A chain of 3 links in three requests: the first two responses have empty `results`, the third
  names all three `{"clientSeq","seq"}` in the order of `index`; a repetition of the first request after that names the
  `seq` of the first link.
- **Parts:** An operation of 2.5 MB with `clientSeq` 5 in three parts, one part per request: after the first and the
  second part `nextClientSeq` is 5 and `results` is empty, after the third it is 6 with `{"clientSeq":5,…}`. The test
  server interrupts the participant for 60 s after the first part; after that the server accepts the second and the
  third, and the operation stands exactly once in the log. After `session/leave` with an open part, that part is
  discarded.
- **Error order:** `ops/send` without `chain` while a chain is open and with a skipped `clientSeq`: `batch_pending`.
  `snapshot/put` from someone not asked, with a `seq` inside a chain: `not_allowed`. `project/load` without a right
  with a `hostedRef` under which nothing lies, and with one under which a project lies: `not_allowed` both times. An
  invalid token with a `sessionId` that does not exist: `token_invalid`.
- **Count limits:** A chain with `count` 17 and a head with 5001 files: `too_large` with `maxCount` 16 or 5000 and
  without `maxBytes`; an operation of 3.6 MB: `too_large` with `maxBytes` 3670016.
- **end twice:** `live/put` with `mode: "end"` 50 ms after an accepted live state: `accepted: true`; the same request
  again: `accepted: true`, and the version of `live` does not rise again.
- **Revision:** `project/load` with `hostedRevision` `"???"`, for the head and when loading a file:
  `revision_unknown`. `project/load` of a file without `part` delivers part 0. The operator overwrites a file directly
  in the store: `project/revision` afterwards names a new `hostedRevision`, the session's `project/save` without
  `force` yields `store_changed` with this file; a part of the old revision the server no longer has yields
  `revision_unknown`, and the client loads once more from the start.
- **Errors not stored:** `project/save` yields `store_changed`; the same request with the same `requestId` and
  `force: true` writes. `session/join` yields `session_full`; if a participant leaves, the repetition with the same
  `requestId` succeeds.
- **Checkpoint with local:** The host is 20 s without contact while `checkpointWanted` applies: no guest gets
  `checkpointWanted`, and a guest's `snapshot/put` yields `not_allowed`; when the host asks again, their response names
  `checkpointWanted: true`.
- **Attachment after saving:** A `hosted` session with loot turned on; B changes a loot point and saves: the test
  server's counter sees B's `attachment/put` with the checksum of the new `mapgroupproto.xml` (for the `seq` of the
  save, all parts) before the `ops/send` with the consequences. A fetches it with `attachment/get` in parts; the
  concatenation is byte for byte the file B wrote, and its checksum equals the one in the consequences. A second
  `attachment/put` of the same checksum answers `complete: true` at the first part. The same for `local` after the
  host's save.
- **Attachment missing:** The test server forgets the attachment: `attachment/get` yields `attachment_unknown`; A's
  `attachment/want` gives B (the uploader) the notice `attachment_wanted` within 1 s; B uploads again, and A's next
  `attachment/get` succeeds. If B has left the session, all others except A get the notice, and C, who had fetched the
  text, uploads it. With `local`, after the host's save, the host gets the notice. Two requests from A within 5 s yield
  one notice.
- **Attachment before turning on loot:** A session with loot turned off; B turns on loot: the test server's counter sees
  B's `attachment/put` for every file the operation names, before the `ops/send` with it; A fetches them with
  `attachment/get`. D joins while loot is off, B turns it off and takes the turning off back with undo: before the
  inverse operation one `attachment/put` per file (first part `complete: true` as long as the server keeps them), and D
  fetches them. If the test server forgets them, the notice on A's `attachment/want` goes to B.
- **Attachment across a restart:** A complete attachment can be fetched with `attachment/get` after a restart of the
  server; an attachment of 17 MB yields `too_large` with `maxBytes` 16777216.
- **Checkpoint with attachments:** C is to upload a checkpoint but does not have the new loot text yet: C fetches it
  first; the snapshot C uploads carries it as an attachment, and someone joining after that needs no
  `attachment/get`.
- **Client version:** Reference server with minimum version `0.4.0`: `info` names `minClientVersion: "0.4.0"` and
  `updateUrl`. `session/create` with `client.version` `"0.3.9"`: `client_outdated` with `minVersion: "0.4.0"`,
  `yourVersion: "0.3.9"` and `updateUrl`, and no session is created; the same for `"0.4"` and a `client` without
  `version` (`yourVersion: ""`). `session/join` without `resume` and `auth/name` with `"0.3.9"`: `client_outdated`, no
  participant, no token. With `"0.4.0"` and `"0.10.0"` all succeed.
- **Version before login:** `session/create` with an invalid token and `"0.3.9"`: `client_outdated`, not
  `token_invalid`; with `protocol: "2.0"`, `protocol_unsupported` first.
- **Running session:** The operator raises the minimum version during a session above the version of its
  participants: `poll`, `ops/send`, `project/save` and `session/join` with `resume` keep succeeding; a new
  `session/join` without `resume` yields `client_outdated`.
- **Server without a check:** the same requests with `"0.0.1"` succeed; `info` names none of the three fields; the
  conformance suite accepts the server.
- **Agent recognised:** Reference server with the optional capability `agents`; Patrick has a `hosted` session, an
  agent with Patrick's token joins with `client.kind: "agent"` and `agentName: "Builder"`: the response names
  `canSave`, `canManage` and `canInvite` `false`; in Patrick's next response to `poll` the agent stands in
  `participants` with `kind: "agent"`, `agentName: "Builder"` and Patrick's `name` and `userId`, Patrick himself with
  `kind: "studio"` and without `agentName`. After a restart of the server the agent is still `kind: "agent"`.
- **Agent may not:** `project/save`, `session/end`, `participant/remove`, `locks/release` and `invite/create` from this
  agent each yield `not_allowed`; project store, participants, locks and invitations are unchanged afterwards.
  `session/create` with `kind: "local"` yields `not_allowed`. With a token bound to an agent, each of the five with a
  `sessionId` that does not exist: `not_allowed`, not `session_unknown` or `ok` (rights before existence).
- **No project by an agent:** An agent's `session/create` (`hosted`, a new `key`, `hostedRevision: ""`): `not_allowed`;
  `project/revision` afterwards answers `project_unknown`, and `sessions/list` with this `hostedRef` names nothing.
- **Agent alone on a project of the server:** For a project the store has, with no session running, Patrick's agent
  creates a `hosted` session with the current `hostedRevision`: `role: "participant"`, `canSave` and `canManage`
  `false`; it uploads snapshot 0, sends an operation, sets `"review"` and leaves: `sessions/list` with the
  `hostedRef` names the session with 0 participants and `unsavedCount` 1, and the project store has no new revision.
  Patrick joins with `canSave: true` and saves; only then is there a new revision.
- **Creating without the right:** The agent's user may not save the project: the agent's `session/create`
  (`hosted`) yields `not_allowed`.
- **Token of an agent:** A token from `auth/code/start` with `client.kind: "agent"` and `agentName: "Scout"` (the
  confirmation page says that an agent is logging in and names “Scout”; with `agentName` empty, with a line break or
  of 65 bytes: `bad_request`), then `session/join` without `kind` or with `kind: "studio"`: the participant is an agent
  (`kind: "agent"`, `canSave: false`). A token from the same login without `kind`: a person.
- **Client kind:** `session/join` with `client.kind: "robot"` yields `bad_request`; without `kind` (token from a login
  without agent) the participant is `kind: "studio"`. An agent returns with `resume` and `client.kind: "studio"`: it
  stays `kind: "agent"`. A server with a minimum version for DZSTUDIO never answers `client_outdated` to `client`
  `{"name":"dzstudio-mcp","version":"0.0.1","kind":"agent"}`.
- **Phase and status:** The agent sends `phase: "working"` and a status of 200 bytes with umlauts: the responses to
  `poll` of all participants name both byte for byte the same, at the latest in the response after the agent's next
  poll; a status of 201 bytes, one with a line break, `phase: "busy"` and a `presence` without `phase` yield
  `bad_request`. A DZSTUDIO that sends `phase` and `status`: both are missing in the responses. Without a reported
  phase, the agent stands in `presence` with `"waiting"` and an empty status.
- **Presence of an agent:** The agent sends `presence` without `position`: no `bad_request`, and in the others'
  `presence` its `position` is missing. Another participant waits in `poll` with `waitMs` 5000; the agent changes only
  phase and status: the waiting poll does not end earlier, and its response names the new phase.
- **Idleness of an agent:** The agent locks 3 elements and then sends only `poll` for 300 s: its locks are
  `lapsed: true`, and someone else's `locks/set` gets them; its next `ops/send` sets `idleSeconds` back to 0.
- **Own host:** Patrick is the host of a `local` session; `sessions/list` with the token of Patrick's agent names it
  with `access: "direct"`, and the agent joins without an invitation. Carla's agent without a permission and without
  an invitation: `not_allowed`.
- **Removing one's own agent:** Carla is a guest by code in Patrick's `local` session, and her agent and Patrick's
  agent are in it too. Carla's `participant/remove` of Patrick's agent, and of Patrick: `not_allowed`; of her own
  agent: `ok` — the agent's next `poll` yields `not_participant` with `removed`, Patrick gets `participant_removed`,
  and after `session/leave` Carla joins again with her code. An agent's `participant/remove` of another agent of the
  same user: `not_allowed`. Patrick (host) removes Ben's agent that came with Ben's code: the code yields `invite_used`
  afterwards, as for anyone removed.
- **Several sessions:** An agent is in two sessions with one token: two `participantId`, each with its own phase and
  its own status; a status in one does not appear in the other.
- **Server without the capability:** Reference server without `agents`: `info` does not name it; `client.kind`, even
  `"robot"`, and `phase` are ignored, the participant has the rights of its user, and `participants` names no `kind`.
  An MCP sends no `session/join` and no `session/create` there (the test server's counter).
