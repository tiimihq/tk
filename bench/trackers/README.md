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

## Results (2026-11-07, tk 0.1.2)

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

## Method notes

- tk 0.1.2, Beads 1.3.4, `gh` 2.93, GitHub's and Linear's remote MCP servers; each with its
  official setup. One workflow on one issue with 8 comments; results will vary with longer
  histories and other agents (Claude Code loads MCP tool definitions on demand; Cursor loads
  all of them on every request).
- Beads injects ~2k tokens at session start (`bd prime` + its CLAUDE.md); tk's brief is ~200.
- `results.jsonl` also holds runs of earlier tk builds, labelled by version.

## Re-run it

    python3 bench/trackers/bench.py <integration> [reps] [model]
    python3 bench/trackers/bench.py cleanup     # close / archive the shared noise issues

GitHub runs use `BENCH_GH_REPO` (default: a sandbox repo) and your `gh` login; Linear runs
use `BENCH_LINEAR_TEAM` and your Linear API key, and create issues titled `[bench] …` that
are archived after each run. Tokens are passed through the environment, never written down.
