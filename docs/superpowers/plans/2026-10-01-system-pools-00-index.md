# System pools: plan index

**Spec:** `docs/superpowers/specs/2026-10-01-system-pools-design.md`

The spec covers five subsystems that build on each other. Each gets its own plan, executed in
order, and each plan ends with `just ci` green and working software. A plan is written in full only
when the plan before it has landed, so it can name the real types and functions instead of
guessing them.

| # | Plan | Spec sections | Ends with |
|---|---|---|---|
| 1 | Foundations (`2026-10-01-system-pools-01-foundations.md`) | 2.6 process group, 2.6 INCODA_HELD format and verification (steps 1 to 3), 4.6 text hygiene, 2.2 and 4.4 config schema 2 | the shipped process-group bug fixed; `KEY=TICKET` held entries verified for liveness and ancestry; one escaper; config writes are locked read-modify-write and keep unknown fields. Still on today's `queues/` layout. |
| 2a | Layout, registry and migration (`2026-10-01-system-pools-02a-layout-migration.md`) | 2.1, 2.3 (layout, fence, re-fence), 3.1, 3.2 first paragraph, 3.3 M0 to M8 and every recovery row, 3.5 bootstrap content, 3.6, 5.5 layout part | `lanes/` layout, `machine.lock`, `machine.json`, the fence with atomic exchange and race rule, M0 to M8 with crash-injection tests for every recovery row, fail-closed registry, `doctor` layout checks and `--rebuild-registry`. Pools registered and usable directly. |
| 2b | Strays, old-holder kill and doctor (not yet written) | 2.3 stray counting and cleanup, 3.2 old-holder kill, orphan records and `force-release --live` refusal, 5.3 fence and stray warnings, 5.5 PATH versions, strays, orphans, stopped holders | old runs no pool admitted are counted; `kill --force` ends an old holder's whole tree; doctor completes. |
| 3 | Pools at top level | 2.2 kinds, 2.4, 2.5, 2.7 `via`/`wait`, 2.8, 3.4, 3.5, 4.1 to 4.5 | lane-set expansion and total order, plan-enroll-verify with replan, unlinked refusal and suggestions, `--pool`, `config` link flags, `link`, `init`, `pools`, quiet-machine, closed and require_reason on pools. |
| 4 | Nesting | 2.6 ordering rule, non-blocking acquisition, fix lines, outer trailer, self-wait, sibling check, `root` and `pgid` ticket fields, 2.8 nested quiet | deadlock-free nesting with the exact refusals and rerun lines of the spec. |
| 5 | Observability and docs | 5.1 to 5.4, section 10 | status JSON, plain and tree, busy lines, watch pool tree; README, docs/DESIGN.md, AGENT-RULE.md, demo tape and gif, release notes. |

Branch: `feat/system-pools`. No release is cut between plans; the first release is after plan 5.

## Carried forward from plan 1

Plan 1 landed as `90edfee..84f58a5`. Its final review routed these items to later plans; each later plan must cover the items listed for it.

- Plan 2: do not cut any build from this branch before the fence lands (a v0.6 binary and this one queue behind each other's nested runs until `--wait`).
- Plan 3: `lane.Enroll` fails closed on `NewerSchemaError` (the waiter poll surfaces it; `effectiveSlotsLocked` may keep its fallback); table-drive the `config` text checks when link flags arrive; rewrite the closed refusal per 4.5.
- Plan 4: the `pgid` clause of the process-group rule and the `set -m` test; non-blocking `out-of-order-busy` replaces today's blocking wait for live non-ancestor entries (`TestLiveNonAncestorEntryDoesNotPassThrough` then expects exit 120, not 121); `ParentChain` stops at a ppid of 0 or less; a probe error other than "not found" on `registry.lock` counts as live and unverifiable (keep "dead" for a delete-pending ticket on Windows); merge the duplicate comment above the main `child.Run`; a non-ancestor variant of `TestNestedChildStaysInOuterGroup`.
- Plan 5: escape status, misc and TUI output; one quoting layer for `dir=` in lane.log (today a Windows path is double-escaped; needs a spec amendment); the escaper also covers U+061C, U+2028 and U+2029 and disambiguates 0x85; decide whether `status --json` config carries `schema` and unknown fields; update docs/DESIGN.md and README.md on `INCODA_HELD`; log malformed bare-key drops; align spec text with logging live held-dropped entries.
