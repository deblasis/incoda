# Rule block for agent instructions

Copy everything below the line into the machine's `~/.claude/CLAUDE.md` (or a
repository's `AGENTS.md`, or any equivalent agent instruction file) and replace
`<KEY>` with the queue key for that machine or project. Adjust the "must run
under the lane" list to that machine's temperament; the thresholds below are a
starting point, not a law.

If a repository always uses one key, set `INCODA_QUEUE=<KEY>` once in the
machine's environment instead of repeating `--queue` everywhere. Do **not** set
`INCODA_DIR` per project: it is a machine-level override, and setting it in one
checkout splits the lane in two while making both halves look healthy.

---

## Heavy job lane (MANDATORY on this machine)

This machine cannot run two heavy jobs at once. Concurrent memory-heavy builds
have taken it down with no warning (the kernel reports normal memory pressure
right up until the watchdog fires), and concurrent GUI/UI test runs corrupt each
other by fighting over focus, the foreground window and synthesized input.

Before running ANY of the following, acquire the lane and run the command under
it:

- any build expected to take longer than ~5 minutes, or with an LLVM/C++ debug
  link step
- any `zig build` / `zig test` with `-Denable-llvm`
- any node job with `--max-old-space-size` of 4096 or more
- any large Rust or C++ debug build
- **any GUI or UI test run**; anything that drives a real window, needs the
  desktop to itself, or synthesizes input. Two of these at once fail in ways
  that look like product bugs.
- any test suite that is timing-sensitive enough to fail on a loaded machine,
  with `--exclusive`, so it holds the queue alone even where the queue allows
  two

Commands:

```
incoda run --queue <KEY> --reason "what this is" -- <cmd...>
incoda status --queue <KEY>          # who holds it, from which folder, and who is waiting
incoda watch  --queue <KEY>          # live view
```

- `run` blocks while the queue is busy and releases on exit, including on a
  crash: the lock is an OS file lock, so a killed holder frees the lane on its
  own.
- `--wait` defaults to 30 minutes. `--wait 0` fails immediately instead of
  queueing, which is occasionally what you want in a script.
- Do not pass `--slots`. The queue's own config carries the number
  (`incoda config <KEY>` shows it); a stray `--slots 1` narrows the queue for
  everyone.
- A run that exits 120 saying the queue is closed is telling you which keys
  replaced it. Use those; do not force the old one.
- Use `--reason` every time. When six sessions share a lane, "which worktree is
  that build in and why" is the first question anyone asks, and `status` can
  only answer it if you said.
- Set `INCODA_OWNER` once per session (your session id or worktree name) so
  `status` and `watch` can say whose job is whose without reading the cwd.
- A recipe that takes its own lane is safe to wrap in `run` on the same key:
  the nested run passes through instead of queueing behind you.

### The rules that matter

- **Never bypass the lane.** No running the same command outside it "just this
  once", no `nohup ... & disown`, no starting a second heavy job in another
  terminal while one is queued. The lane binds only what is routed through it,
  so a single bypass reintroduces exactly the collision it prevents.
- **Never run two heavy jobs at once**, even if both would "probably" fit.
- **If the lane makes you wait longer than ~10 minutes, surface it to the user.**
  Say what is holding it (`incoda status` names the pid, the command and the
  directory) and ask. Do not silently keep waiting for half an hour, and do not
  decide on your own to go around it.
- **`incoda force-release` is a human decision, never an agent's.** It refuses
  while a live holder exists for a reason: a blind force-release once caused a
  real collision. If you think you need it, you have found something worth
  telling the user about instead.
- The working directory does not matter. Every session in every worktree that
  uses key `<KEY>` shares one lane, which is the entire point.
