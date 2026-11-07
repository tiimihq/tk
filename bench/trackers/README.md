# Tracker-integration benchmark

What does it cost an agent to work with an issue tracker? Same workflow, same issue,
through different integrations:

> Find the open issue about EU refunds, read it with its full comment history (8 comments),
> record a given progress note, and hand the issue back (open, not in progress).

Claude Code (`claude -p`, Sonnet 5.5), 3 runs per integration, medians. Each integration
uses its official setup: `tk init`, `bd setup claude`, GitHub's and Linear's remote MCP
servers, or the `gh` CLI. Every run starts from a fresh folder and a freshly seeded issue
(`seed.json`); Claude Code loads only the project's own settings (`--setting-sources
project,local`). Success is checked against the tracker afterwards.

## Results (2026-11-07)

| Integration | Done | Cost | Tracker tokens* | Output tokens | Time | Turns |
| --- | --- | --- | --- | --- | --- | --- |
| **tk (local)** | 3/3 | **$0.051** | **42k** | 401 | **9.7 s** | 3 |
| Beads 1.3.4 | 3/3 | $0.066 | 75k | 263 | 11.2 s | 4 |
| **tk on GitHub Issues** | 3/3 | **$0.049** | **44k** | 309 | 18.1 s | 3 |
| `gh` CLI | 3/3 | $0.059 | 63k | 662 | **15.9 s** | 4 |
| GitHub MCP (remote) | 3/3 | $0.104 | 137k | 1323 | 25.0 s | 8 |
| **tk on Linear** | 3/3 | **$0.053** | **45k** | 559 | **18.1 s** | 3 |
| Linear MCP (remote) | 3/3 | $0.099 | 141k | 776 | 24.0 s | 8 |
| No tracker (Claude Code itself) | 3/3 | $0.024 | 0 (16k fixed) | 6 | 3.0 s | 1 |

\* Input tokens (fresh + cache read + cache write) above the no-tracker baseline of 16k.

Against each alternative, tk used:

- **Beads:** 44% fewer tracker tokens, 23% lower cost, 13% faster.
- **`gh` CLI:** 30% fewer tracker tokens, 17% lower cost, but ~2 s slower (tk also moves the
  issue and labels it, which takes extra API calls).
- **GitHub MCP:** 68% fewer tracker tokens, 53% lower cost, 28% faster.
- **Linear MCP:** 68% fewer tracker tokens, 46% lower cost, 25% faster.

## Read this before quoting the numbers

- **tk was improved during this benchmark; the others were not.** The first tk runs
  (v0.1.1) cost $0.062 and took 5 turns: agents ran `tk --help`, re-read the issue with
  `--full`, and on Linear could not see a backlog issue at all (0/3). We fixed what the
  traces showed (a command card in the session brief, `tk show` with all comments clearly
  labelled, one-line help, `tk comment` on every tracker, backlog in the Linear inbox) and
  re-ran with v0.1.2. Those earlier rows are kept in `results.jsonl`, labelled.
- One workflow, one small issue, one model, 3 runs each. Bigger issues, longer histories
  or other agents (Cursor loads every MCP tool definition on every request; Claude Code
  defers them) will move the numbers.
- Beads pays ~2k tokens up front every session (`bd prime` + its CLAUDE.md) and in return
  the agent knew the exact commands every time; tk's brief is ~200 tokens.
- Total spend for all runs, including the earlier ones: $2.41.

## Re-run it

    python3 bench/trackers/bench.py <integration> [reps] [model]
    python3 bench/trackers/bench.py cleanup     # close / archive the shared noise issues

GitHub runs use `BENCH_GH_REPO` (default: a sandbox repo) and your `gh` login; Linear runs
use `BENCH_LINEAR_TEAM` and your Linear API key, and create issues titled `[bench] …` that
are archived after each run. Tokens are passed through the environment, never written down.
