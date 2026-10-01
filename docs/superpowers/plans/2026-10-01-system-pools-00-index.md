# System pools: plan index

**Spec:** `docs/superpowers/specs/2026-10-01-system-pools-design.md`

The spec covers five subsystems that build on each other. Each gets its own plan, executed in
order, and each plan ends with `just ci` green and working software. A plan is written in full only
when the plan before it has landed, so it can name the real types and functions instead of
guessing them.

| # | Plan | Spec sections | Ends with |
|---|---|---|---|
| 1 | Foundations (`2026-10-01-system-pools-01-foundations.md`) | 2.6 process group, 2.6 INCODA_HELD format and verification (steps 1 to 3), 4.6 text hygiene, 2.2 and 4.4 config schema 2 | the shipped process-group bug fixed; `KEY=TICKET` held entries verified for liveness and ancestry; one escaper; config writes are locked read-modify-write and keep unknown fields. Still on today's `queues/` layout. |
| 2 | Layout and migration | 2.1, 2.3, 3.1 to 3.3, 3.6, 3.2 old-holder kill, 5.5 doctor | `lanes/` layout, `machine.lock`, `machine.json`, fence, M0 to M8 with every recovery row, strays and orphan counting, old-holder kill, fail-closed registry, doctor. Pools registered and usable directly. |
| 3 | Pools at top level | 2.2 kinds, 2.4, 2.5, 2.7 `via`/`wait`, 2.8, 3.4, 3.5, 4.1 to 4.5 | lane-set expansion and total order, plan-enroll-verify with replan, unlinked refusal and suggestions, `--pool`, `config` link flags, `link`, `init`, `pools`, quiet-machine, closed and require_reason on pools. |
| 4 | Nesting | 2.6 ordering rule, non-blocking acquisition, fix lines, outer trailer, self-wait, sibling check, `root` and `pgid` ticket fields, 2.8 nested quiet | deadlock-free nesting with the exact refusals and rerun lines of the spec. |
| 5 | Observability and docs | 5.1 to 5.4, section 10 | status JSON, plain and tree, busy lines, watch pool tree; README, docs/DESIGN.md, AGENT-RULE.md, demo tape and gif, release notes. |

Branch: `feat/system-pools`. No release is cut between plans; the first release is after plan 5.
