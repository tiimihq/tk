# Handoff benchmark

Does a fresh agent session finish cheaper when the previous session left a `tk handoff`?

## Result so far (2026-11-07): no measurable gain on small repos

| Setup | Model | Session B with handoff | Session B without |
| --- | --- | --- | --- |
| `task/` (2 files, tax + refund bugs, A fixes tax, B refunds) | Sonnet 5.5 | $0.069, 5 turns, tests pass | $0.058, 4 turns, tests pass |

Calibration on `task2/` (`ledgerly`, ~50 files, a refund bug behind three tempting
wrong fixes), one uncapped session each:

| Model | Cost | Turns | Fixed correctly |
| --- | --- | --- | --- |
| Sonnet 5.5 | $0.088 | 7 | yes, first try |
| Haiku 5.5 (2 runs) | $0.007 | 8 | yes, both |

Current models solve repos this size in one pass for cents, so there is nothing for a
handoff to save; we stopped before spending more. Handoffs should matter when
re-orientation is expensive (large real repos, long sessions, dead ends that leave no
trace in the code). That needs real repositories and a larger budget, and is not
measured here. Total spend: about $0.41.

## Run it

    bench/run.sh [runs-per-condition] [model]     # task/; CAP_USD, STOP_USD, OUT env vars

Each run copies the task into a temp folder with its own `TK_HOME`; your real `~/.tk`
is not touched. Claude Code runs with `--setting-sources project,local`, so only the
task's own settings (and tk's hook in the tk condition) apply.
