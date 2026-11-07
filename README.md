# Tiimi

**Your coding agents, as teammates. Keep your tracker, keep your agents: Tiimi connects their work,
handoffs and cost to every issue.**

Tiimi ("team" in Finnish) is a small open-source CLI, `tk`, plus a background service, `tkd`, for the
coding agents you run yourself: Claude Code, Codex, Cursor, Gemini CLI, OpenCode. It gives an agent a short brief of its
issue, carries handoffs between sessions and between agents, and records what each issue cost in
tokens and dollars, in the tracker your team already uses. No second tracker to sync.

![Tiimi demo: tk receipt, then Claude Code hands an issue off to Codex](demo/tiimi-demo.gif)

<sub>Demo on a sample project with made-up logs; re-record it with `demo/record.sh`.</sub>

> Status: preview. Commands and file formats may still change. Site: [tiimi.dev](https://tiimi.dev)

## Install

```sh
brew install tiimihq/tap/tiimi        # macOS, Linux
curl -fsSL https://tiimi.dev/install | sh   # checksum-verified, no sudo
go install github.com/tiimihq/tk@latest   # Go 1.26+
```

One binary, `tk`, for macOS / Linux / Windows on Intel and ARM. Check with `tk version`.

## Quick start (one minute, no account)

```sh
cd your-project
tk init                          # connects the coding agents installed here (hooks + instructions)
tk new "Add cursor pagination"   # an issue on this machine
tk daemon install                # optional: background cost sync, starts at login
```

Now start your agent in that folder. It sees a short brief at session start and uses tk itself:

```text
tk start LOC-1      claim the issue, print its brief
tk note "..."       record progress worth keeping
tk handoff "..."    pause: what's done, what's left, what didn't work; any agent resumes from it
tk done "..."       finish; the issue gets its cost
```

`tk board` shows everything in the terminal, live, while the agent works:

```
tk board  local · 3 open · 4 total  ● agent working on LOC-1

Todo 2                       In progress 1                Done 1
╭──────────────────────────╮ ╭──────────────────────────╮ ╭──────────────────────────╮
│ LOC-2  ● urgent          │ │ LOC-1  ● medium          │ │ LOC-3  ● medium          │
│ Fix refund rounding      │ │ Add cursor pagination    │ │ Write migration          │
│                          │ │ ● agent working · ~$0.04 │ │                          │
╰──────────────────────────╯ ╰──────────────────────────╯ ╰──────────────────────────╯
```

## Use your team's tracker

Set the mode in `~/.tk/config.json` and save the key in the Keychain:

| Tracker | Config | Key | Status |
| --- | --- | --- | --- |
| **Local** (default) | nothing | none | stable |
| **Linear** | `{"mode":"linear","linear":{"team":"ENG"}}` | `tk secret set linear` (personal key), or `tk login linear` (agent app) | personal key live-tested; app login in preview |
| **GitHub Issues** | `{"mode":"github","github":{"repo":"owner/name"}}` | your `gh` login, `tk secret set github`, or `tk login github` | live-tested |
| **Jira Cloud** | `{"mode":"jira","jira":{"site":"https://you.atlassian.net","email":"you@x.com","project":"ENG"}}` | `tk secret set jira` | beta (tested against Jira's API spec, not yet a live site) |

How tk maps its steps to each tracker: `start` assigns the issue to you and moves it to In Progress;
`handoff` moves it back to To Do and posts the handoff as a comment; `done` posts a summary with
the cost and moves the issue to review (or done). In Jira, tk only uses moves your workflow allows;
in GitHub, "in progress" is a label and done closes the issue.

**Agent identity.** With a personal key, agent comments show as you, prefixed `[tk agent]`. With the
app logins, Linear shows the agent as the issue's delegate while you stay the assignee, and GitHub
shows comments as you with the tk app badge.

## Agents

`tk init` sets up every agent it finds; `tk setup --list` shows all 14 recipes, and
`tk setup <tool> --check` verifies one.

| Agent | What tk installs | Cost per issue |
| --- | --- | --- |
| Claude Code | session-start hook | yes, from its local logs |
| Codex CLI | native hooks (brief at start, restored after compaction) + `AGENTS.md` | yes |
| Gemini CLI | session-start hook + `GEMINI.md` | yes (beta) |
| OpenCode | `AGENTS.md` | yes (beta) |
| Cursor | always-on rule + hooks | no: Cursor keeps no local usage log |
| Factory, Mux, Copilot CLI, Junie, Windsurf, Cody, Kilo Code, Kiro, Aider | their instructions or rules file | no |

**Codex asks first.** Codex runs a project's hooks only after you trust the project (it asks the
first time) and approve the hooks with `/hooks` inside Codex. `tk setup codex --check` shows whether
the project is trusted.

**Several agents at once.** One tk session per folder: run each agent in its own folder or git
worktree. `TK_ISSUE=<KEY>` picks a session explicitly.

## Cost

tk reads token usage from each agent's own local logs, prices every reply at the provider's
standard API list price on that day, and adds it to the issue. These are **API-equivalent
estimates**: on a Claude or ChatGPT subscription you don't pay per token, but the numbers show what
each issue costs relative to others.

- Tokens are stored too, so cost can be recomputed when prices change.
- `tk receipt` shows what your agents cost per issue over the last 30 days, straight from their logs: no setup. Issues come from branch names (`eng-142-fix-login`, `123-fix-login`).
- `tk cost update` downloads the latest prices (the LiteLLM list ccusage also uses) and shows what
  changed. tk never downloads prices on its own. Your own rates go in `~/.tk/prices.json`.
- A model tk can't price is shown as "not priced", never as $0.00.

## Offline

Reads come from a local cache. `tk note`, `handoff` and `done` are queued when the tracker can't be
reached, and `tkd` sends them in order when it's back; `tk status` shows anything waiting.
`tk start` needs the tracker, because claiming an issue must not race a teammate.

## What stays on your machine

Everything Tiimi knows lives in `~/.tk` (owner-only) and the Keychain. tk talks only to the tracker you
configure, and to GitHub when you run `tk cost update` or an app login. No telemetry. Agent logs are
read locally and never uploaded: the tracker gets what you or your agent write with `tk note`,
`handoff` and `done`, plus token counts and costs, never prompts or session logs. Details:
[SECURITY.md](SECURITY.md).

## Tiimi and Beads

Beads is a tracker for agents that syncs to Linear, Jira and GitHub. Tiimi is a layer on the tracker you
already have: one copy of every issue, no sync conflicts, plus cost per issue. Beads works fully
offline and has a richer task graph; pick what fits.

## License

MIT. See [LICENSE](LICENSE). Developer notes: [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md).
