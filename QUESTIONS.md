# Open questions about the protocol

Points where the protocol description (dzcollab 1.0, [PROTOCOL.md](PROTOCOL.md)) is ambiguous or incomplete. For
each, the server implements the plainest reading named here; none of this is invented silently. Section names are
those of PROTOCOL.md. Every point of the earlier list is answered by the protocol now and was removed.

## Sending operations: ops/send / Large steps: chains

1. **Which links does the answer with the last link name?** "Only the response to the request with the last link
   names their `seq`" — every link's `{clientSeq, seq}`, or only the last link's? *Implemented:* every link, in index
   order; a later repetition of any link names its own seq.

2. **"The clientSeq … already counts" for parts.** Duties of a server › Restart says accepted, not yet appended parts
   *and* links count already. For links `nextClientSeq` counts on; for the parts of one operation it cannot (the rule
   "clientSeq lower than nextClientSeq: a repetition" would swallow the remaining parts). *Implemented:* the clientSeq
   of a part-wise operation counts once its last part is there; links count when they are complete.

3. **Parts and links when the participant is 30 s without contact.** The protocol drops locks and live states after
   30 s and unappended links on leave, removal and timeout; for parts of a large operation it says nothing.
   *Implemented:* parts are kept like links until leave, removal or the 10-minute timeout (dropping them would leave a
   client that resumes with the next part waiting forever).

4. **Which error comes first** when one item breaks several rules: an operation without `chain` while a chain is open
   *and* with a clientSeq gap; a checkpoint from someone not asked *and* with a seq inside a chain. *Implemented:*
   `batch_pending` before `client_seq_gap` (as for missing parts), and in `snapshot/put` `not_allowed` before range
   and chain checks.

## Live states: live/put

6. **A repeated `end`.** "It always accepts an `end`", but the retry table says a repeated `liveId + n` gets
   `accepted: false`. *Implemented:* `end` is always `accepted: true` (no 180 ms gap, no check of `n`); a repetition
   has no further effect.

## Projects on the server: project/list, project/revision, project/load

8. **A malformed `hostedRevision`** (from another store, or garbage) at `project/load`. *Implemented:*
   `revision_unknown` like a revision that is gone, so the client starts again at the head (also at the head with an
   explicit `hostedRevision`).

10. **`part` at `project/load` of a file** is "number from 0, may be missing". *Implemented:* missing means part 0.

## Retries and timeouts

11. **Error answers in the retry window.** "With the stored response" — also when the first attempt failed
    (e.g. `not_allowed`, `store_changed`)? *Implemented:* only successful answers are kept; a repeated failed request
    is processed again (it changed nothing the first time).

## Snapshots: snapshot/put, snapshot/get (checkpoints)

12. **Local session whose host has no contact.** For hosted sessions the protocol says "if nobody has contact, it asks
    for none for the time being"; for local ones only "the host". *Implemented:* the host stays the one asked (nobody
    else may upload); a host without contact for 300 s ends the session anyway.

## Attachments: attachment/put, attachment/get, attachment/want

13. **How long a complete attachment is kept.** "At least as long as it keeps the log from this `seq`, at least 10
    minutes after the upload." *Implemented:* for the whole life of the session (more than the minimum); its end drops
    them all. A server with many large loot texts may want to prune by the log rule; this one does not yet.

14. **"Per participant and checksum it gives a notice at most every 10 s"** — per asking participant or per receiving
    one? *Implemented:* per receiver and checksum: two wishes of A within 10 s give B one notice, and so do a wish of A
    and one of C. A wish that reaches nobody new answers ok without effect.

15. **`seq` of attachment/put** above the highest seq handed out. *Implemented:* `bad_request`. A repeated upload of a
    complete attachment with a later seq answers `complete: true` and records the later seq; the uploader asked by
    attachment/want stays the one who completed it.

16. **Who began an upload.** Incomplete parts are dropped "when the uploader leaves". Several participants may send
    parts of the same checksum. *Implemented:* the participant who sent the first part owns the incomplete upload;
    when they go, it is dropped, also parts others sent.

## Saving: project/save

17. **merged naming one target twice.** *Implemented:* `bad_request`, like a file named twice.

18. **Where the file checksum is formed.** The core forms the SHA-256 of every file over the concatenated pieces that
    arrived and answers `checksum_mismatch` before `not_confirmed`; the store forms it again over the bytes it writes
    (the reference store over the files of the new version, before switching the link). Both have to agree for a
    write, so a store that changes bytes on the way is caught too.

## Errors › Order

19. **poll for a session that does not exist.** The protocol allows `not_allowed` ("on `poll` like `session_unknown`
    (the server checks rights before existence)"). *Implemented:* the reference server answers `session_unknown` (it
    has no rights to check on a session it does not know); the suite accepts both.

20. **client_outdated before too_large.** Stage 1 names the version check before the size of the request, but a
    body over `maxRequestBytes` is never read whole, so its `client` cannot be seen. *Implemented:* such a request
    answers `too_large`; every request the server reads checks the version before anything else of stage 1 after
    the protocol (`capability_missing` included, so `auth/code/start` on a server without `auth.code` is
    `client_outdated` for an old DZSTUDIO).

## Versions and capabilities › Client version

21. **client without name, or a name that is not a text.** "About other names it decides itself" covers other
    names. *Implemented:* only a non-empty name other than `dzstudio` skips the check; a missing name is checked like
    `dzstudio` (`allowedClients` still decides about other names). A `version` that is not a text counts as missing
    (`yourVersion: ""`).
