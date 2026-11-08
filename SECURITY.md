# Security policy

## Reporting

Report vulnerabilities privately to **alex@deblasis.net** (or open a GitHub
security advisory on this repository). Do not open a public issue for
something exploitable.

You will get an acknowledgement within 5 days, and a fix or a stated
assessment as soon as practical afterwards. This is a one-maintainer project;
that is the honest SLA.

## Scope and threat model

`incoda` is a local developer tool. It runs commands you already had the
authority to run, as your own user, and it coordinates them through files in a
per-user state directory on a local filesystem.

The security boundary is the OS user. What is in scope:

- A queue key escaping the state directory (key validation refuses path
  separators, `..`, and Windows device names rather than sanitising them).
- Correctness failures that make two heavy jobs run when the tool said they
  would not: the locking probe in `incoda doctor` exists to catch filesystems
  that do not enforce locks.
- Anything that would make a *less* privileged process able to influence a
  *more* privileged one through the state directory.
- The answer channel typing at a job it should not reach: `incoda answer`
  writes into a pty job's console input, so it is double-gated — the
  owner gate attach shares (`lane.MayAttach`, checked by the command and
  again by the run against the live ticket) and a blocked gate (the run
  delivers only while the job's own OSC 7501 record says `blocked` with a
  kind). The typed text never echoes into the job's log, and a refusal
  carries its reason back through the same request file.

Out of scope: denial of service by a local user against their own machine
(they have `kill`), and multi-user isolation on a shared host. State is
per-user by design; two OS users on one machine get separate lanes and are
expected not to trust each other.
