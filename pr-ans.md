<!-- longform: mechanism documentation with per-claim test mapping -->
Reopens #30's deferral on both halves: the machine-readable record and the reply path for a blocked job.

**`status --json` carries the record.** A pty participant gains an additive `program_status` object (state, kind, progress, app, id, title, msg) for its owner alone - the same gate attach and the watch capture view apply (`--owner` / ``); the schema stays 1, fields are only ever added. A script can now see `state=blocked, kind=question` and act; a non-owner gets the participant without it.

**`incoda answer` types the reply.** `incoda answer --queue KEY --pid N --text TEXT [--raw] [--wait 10s]` delivers the owner's reply to a pty job that reported `state=blocked` with kind `question`/`permission`/`auth`: the text is injected into the job's console input, echo-free (it never reaches the job's log), and a carriage return completes it unless `--raw`.

The mechanism, and what each piece is for:

- **The door** is a `.answer` companion file beside the ticket, the same shape the kill door uses. Every request carries a minted id (`<pid>-<nanotime>`, shape-enforced at the door: a planted id of any other shape is not a request at all).
- **The gates run in the run process**, where the truth lives: the attach gate against the live ticket, and the blocked gate against the job's own log - the door types at a job only while that job says it is waiting on its user. Non-pty runs refuse by name; a request still pending when the run ends is answered (`the run ended before the answer was delivered`), never swept into silence.
- **The verdict is identity-scoped and durable**: the holder answers only by rewriting the door file with `Verdict: delivered|refused` for that exact id (registry-locked, re-read) - deletion is never an ack - and logs the verdict to the lane log; the CLI reads the file first and the id-scoped log line as the fallback that survives the file's sweep. Superseded is what absence says at the deadline, never before.
- **One injector per console**: the replier and the answer door share a single locked injector, so at most one `__coninj` helper exists on a console at any instant - a sibling's flush could otherwise fabricate a read and destroy input.
- **The injector's verdict is the whole payload**: the helper snapshots the input buffer before its write; delivered means everything it wrote was consumed (the one-event tolerance is the completing key's leftover up-event); written-but-unread after the settle window is the honest refusal, flushed. Raw per-key readers - line editors, the terminal-reply loop itself - drain across reads, and partial movement is never delivery.
- **Console-close during delivery is a refusal** (`the job's console closed while the answer was being delivered`): the write's fate is unknowable, and exit 0 is never claimed for a delivery that cannot be verified.
- **Bounded everywhere**: the door read is stat-bounded (16 KiB) on both sides, verdict rewrites drop the payload, refusal text is escaped at the print site, and the blocked row's head now renders the kind (`[blocked:question]`).

Exit codes: delivered `0`, a named refusal `120`, an answer no run took in time `126`.

**Tests**: every claim above has a pin that fails if the claim breaks - the door protocol and id shape, the CLI verdict contract (log-first, absence-is-pending, prefix-id no-match), the verdict mapping, the shared-injector wiring, the run-side refusals (planted door files on pipe runs and killed runs), the echo-free delivery, the whole-payload per-key reader, and the end-to-end loop on a real lane. `go test ./...` is green at the head.

Known bounds (documented, none silent): TOCTOU microsecond windows at the gate and sweep; delivery wait caps under machine load (2s helper cap, pre-dating); a hostile job can forge its own read signal by self-injecting input (the reply is still fully typed); same-user state-dir writes can forge verdicts for their own requests (the pre-existing trust level of the whole state dir); a 64 KiB log-tail window, past which a verdict read degrades to pending, never to a false ack.
