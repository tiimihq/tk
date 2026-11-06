# tk: developer notes

## Install

One static binary for macOS, Linux and Windows (amd64 and arm64), released the way Beads and
lazygit ship: GitHub release archives plus `checksums.txt`, built by GoReleaser
(`.goreleaser.yaml`). `tiimihq` is a placeholder until the name and org are decided.

```sh
brew install tiimihq/tap/tk                       # macOS / Linux (Homebrew tap)
curl -fsSL https://raw.githubusercontent.com/tiimihq/tk/main/install.sh | sh   # checksum-verified, no sudo
go install github.com/tiimihq/tk@latest           # Go users (after the module path is renamed)
```
Windows: download the zip from the releases page. `tk version` prints the installed version.

Agent work loop for coding agents: compact views of assigned work, and sessions
(start / note / handoff / done) with per-issue token cost, for agents a developer runs
themselves. Works against any tracker through a backend.

```sh
go build -o bin/tk . && go build -o bin/tk-board ./cmd/tk-board
go test ./...                  # includes the Linear backend against a fake GraphQL server

tk next                        # assigned work, one line per issue
tk peek ENG-1                  # one-line status (plus recorded cost)
tk resume ENG-1                # brief to continue work (--full for the whole summary)
tk new "Title" -- description  # create an issue assigned to you (local, linear)
tk start ENG-1                 # open a session: claim the issue, print the brief
tk note "text"                 # note on the session's issue
tk cost                        # record the session's Claude Code token usage (safe to repeat)
tk handoff "what's done/left"  # pause; next session resumes from this note
tk done "summary"              # finish: sync cost, move the issue on
tk status                      # mode, daemon, open session

# local mode: managing issues yourself
tk board                       # terminal board (below)
tk list [--all]                # every open issue; --all adds done and cancelled
tk edit LOC-1 title="New title" priority=high status=in_progress description="..."
tk comment LOC-1 "text"        # comment as yourself
tk cancel LOC-1 "reason"
```

## tk board

A terminal board for local mode: columns Todo / In progress / Done (`a` shows Cancelled),
refreshed every 3 s so an agent's notes and handoffs appear while it works.
Keys: arrows or hjkl move, enter details, n new, c comment, e title, d description
(ctrl+s saves), p priority, < > move between columns, x cancel (asks y/n), r refresh, ? help, q quit.

It is a separate binary, `tk-board` (Bubble Tea v2, Lip Gloss v2, Bubbles v2; needs Go 1.26+),
installed next to `tk`; it reads and writes only through `tk` commands (`tk board-data`,
`tk edit`, ...), so `tk` itself stays standard-library only. Colours follow the terminal's
background, asked for without blocking; `TK_THEME=light|dark` forces one.

`--json` on read commands prints the data as JSON.

## Connecting coding agents: tk init / tk setup

`tk init` sets up every coding agent it finds (CLI on PATH, its app, or a folder only it creates),
like Beads' `bd init`. `tk setup <recipe>` does one tool; `--check` verifies, `--remove` undoes,
`--global` installs for all projects (claude, codex, cursor, gemini). tk edits only its own marked
section (`<!-- BEGIN TK INTEGRATION -->`) or its own hook entries, and backs up changed files to `.bak`.

| Recipe | What it installs (project) | Usage captured |
| --- | --- | --- |
| claude | SessionStart hook in `.claude/settings.local.json` (fires after compaction too) | yes, exact session |
| codex | `.codex/hooks.json` (SessionStart; PostCompact + UserPromptSubmit restore the brief after compaction), `[features] hooks = true` in `.codex/config.toml`, `AGENTS.md` section | yes, exact session (unpriced) |
| cursor | `.cursor/rules/tk.mdc` (always applied), `.cursor/hooks.json` (sessionStart, preCompact, postToolUse) | no: Cursor keeps no local usage log |
| gemini | SessionStart hook in `.gemini/settings.json`, `GEMINI.md` section | yes (unpriced) |
| opencode, factory, mux | `AGENTS.md` section | opencode yes (its SQLite db via `sqlite3`, or legacy JSON); others no |
| copilot, junie | `.github/copilot-instructions.md`, `.junie/guidelines.md` sections | no |
| windsurf, cody, kilocode, kiro | `tk.md` in each tool's rules folder | no |
| aider | `.aider/TK.md` plus a `read:` entry in `.aider.conf.yml` | no |

Hooks call `tk hook <tool> <event>`; they never fail or block the agent. Hook files use plain `tk`
when the `tk` on PATH is this binary (portable for teammates), otherwise its absolute path.
Gemini and OpenCode usage formats follow ccusage's documented adapters and are tested on fixtures only.

## Backends (modes)

Chosen by `TK_MODE`, else Paperclip settings in the environment, else `~/.tk/config.json`, else local.

```json
{
  "mode": "local",
  "paperclip": { "url": "http://127.0.0.1:3100", "plugin": "<tk-bridge plugin id>" },
  "linear": { "team": "ENG", "doneState": "In Review" }
}
```

| Mode | Issues live in | done moves the issue to | Cost is kept in |
| --- | --- | --- | --- |
| `local` (default) | `~/.tk/local/store.json` (locked, atomic writes) | done | the issue in the local store |
| `paperclip` | a Paperclip server, via the `tk-bridge` plugin (`../tk-plugin`); agent key in `TK_TOKEN[_FILE]` or `~/.tk/agent_token.json` | in_review, assigned to the human reviewer | Paperclip cost events |
| `linear` | your Linear workspace (GraphQL); key in `LINEAR_API_KEY` or `~/.tk/linear_key` | `linear.doneState`, else a "review" state, else the first completed state | `~/.tk/costs/<KEY>.json`; handoff/done comments carry a cost line |
| `jira` | Jira Cloud (REST v3, `search/jql`); `jira.site`, `jira.email`, token in `JIRA_API_TOKEN` or `~/.tk/jira_token` | `jira.doneStatus`, else a "review" status, else a Done-category status (only transitions the workflow offers) | `~/.tk/costs`; comments carry a cost line |
| `github` | one repo's GitHub Issues (`github.repo`); token in `GITHUB_TOKEN`, `~/.tk/github_token`, or `gh auth` | closed as completed, or `github.reviewLabel` added and left open; "in progress" is a label | `~/.tk/costs`; comments carry a cost line |

In Paperclip mode switch the agent's wakes off (`runtimeConfig.heartbeat {enabled:false, wakeOnDemand:false}`)
so Paperclip never launches it; the plugin writes with no heartbeat runs. In Linear mode the agent acts as
the developer whose key it uses; its comments are prefixed `[tk agent]`. Linear reads are cached for offline
use; writes need the network. The personal-key workflow was live-tested on one workspace; the app-login workflow is currently tested with simulated HTTP responses.

## Pricing

Each reply is priced on its own at the provider's standard API list price in force on its date
(`pricing.go`, checked 2026-11-03 against Anthropic's, OpenAI's and Google's pricing pages):
Claude Opus/Sonnet/Haiku 5.x and Fable 5.x (5-minute and 1-hour cache writes priced separately;
Haiku 5.5 tiered above 100k-token prompts), GPT-6 astra/sol/terra/luna and gpt-5.3-codex
(long-context rates above 272k), Gemini 4 Argon, 3.x Flash/Flash-Lite and 3.1 Pro (tiered above
200k; announced Dec 2026 / Jan 2027 increases dated in). Fast/priority replies and unknown models
stay unpriced in their own batch, never folded into a total. These are API-equivalent estimates;
subscription users don't pay them per token.

**Keeping prices current.** Cost events store the full token breakdown (fresh input, cache read,
5-minute and 1-hour cache writes, output, reply count), so cost can be recomputed when prices change;
the dollar figure is the estimate at the time of the work. Price sources, first match wins:
`~/.tk/prices.json` (your own rates) → the LiteLLM community list saved by `tk cost update`
(the list ccusage uses; it has long-prompt, priority and 1-hour-cache prices) → the verified built-in
table. A downloaded price more than 5x from the verified one is ignored; a dated increase in the
verified table (e.g. Gemini 4 Argon on 2026-12-01) beats a list downloaded before that date.
`tk cost update` prints which verified prices the list disagrees with; tk never downloads prices on
its own. `tk cost` shows the price source and warns when it is old (30 days for a download, 60 for
the built-in table). `tkd` reloads the price files when they change.

## Cost capture

`tk cost` and `tk done` read Claude Code's session logs (`~/.claude/projects/<launch dir>/*.jsonl`,
found by walking up from the current directory) for replies logged since `tk start`, and post one
Paperclip cost event per reply on the issue and the session's run. The idempotency key is the
reply's message and request id, so repeating a sync never double counts. Costs are estimates from
Paperclip's own Anthropic rate card, marked API-equivalent; models not on the card stay unpriced.

Known gaps: the reply that runs `tk done` is still being written, so it is missed until a later
sync; two Claude sessions in the same folder at once would both be counted; Codex logs are not read
yet; one event per reply is chatty for long sessions.

## tkd: `tk daemon`

The background service is the same binary. Run `tk daemon` (and `tk status` to check it). It:

- refreshes a local cache of assigned issues every 15s; `tk next/peek/resume` read it first
  (about 8 ms) and fall back to a stale copy only when the server is unreachable;
- syncs the open session's usage every 30s as one cost event per model, through a write-ahead
  ledger (`~/.tk/ledger/`) so a crash between posting and recording never double counts;
- releases a session whose agent has logged nothing for 30 minutes (comment, release, end run);
- closes a session whose run ended outside tk;
- syncs cost once more 60s after `tk done`, for the reply that was still being written.

Settings: `TKD_REFRESH`, `TKD_COST_INTERVAL`, `TKD_IDLE_TIMEOUT`, `TKD_FINAL_SYNC_DELAY`.

### As a macOS login service (optional)

```sh
tk daemon install              # launchd agent dev.tk.tkd: starts at login, restarts after a crash
tk daemon install --no-login   # installed, but start it yourself
tk daemon start | stop         # control it (a clean stop stays stopped)
tk daemon uninstall            # remove it
tk status                      # running? installed? starts at login?
```

`install` copies the agent key to `~/.tk/agent_token.json` (owner-only), which `tk` also uses by
default, and logs to `~/.tk/logs/tkd.log`. The service runs the binary from where it was
installed, so reinstall after moving it.

## Claude Code session-start hook

```sh
tk hook install      # adds the hook to ./.claude/settings.local.json (not committed); uninstall removes it
```

When a Claude Code session starts, resumes, clears or compacts, it runs `tk hook session-start`,
which records the session id and its log file and prints a short brief (how to use tk, assigned
work, or the open session's resume brief). Claude Code adds that to the agent's context. The hook
always exits 0 and prints nothing on errors, so it can't block a session.

`tk start` links its session to the Claude session running it (the one in this project whose log
was written most recently), and cost is then read from exactly that log file. Without the hook,
tk falls back to every log in the project's folder.

## Usage capture (Claude Code and Codex)

Claude Code: replies from `~/.claude/projects/<project>/*.jsonl` (exactly the linked
transcript when the session-start hook recorded one). Codex CLI: `token_usage_record`s from
`~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl` whose `session_meta.cwd` is the session's
directory (override with `TK_CODEX_SESSIONS`); read only when the tk session isn't linked to
a Claude transcript. Models missing from the rate card (all OpenAI models today) are recorded
and shown as unpriced, never as $0.00.

## Not yet

Offline write queue, `tk block`, Jira and GitHub backends, OpenAI rate card, Cursor usage, native Linear agent sessions/webhook delivery,
`tk link` to upload a local store, Keychain for keys, SQLite local store,
Linux/Windows services. The old run-based Paperclip mode and the `tkd_session` adapter
(`../tkd-adapter`) are superseded by the `tk-bridge` plugin.


## Linear agent teammates (developer preview)

An installed Linear OAuth app can now be the agent delegate while you remain the
human assignee. Name your app **Claude via tk** or **Codex via tk** in Linear; that
registered name determines its identity. The CLI does not impersonate an official
Claude/Codex integration, create apps, or launch coding agents.

1. As a Linear workspace admin, register an OAuth application with the desired
   agent name and callback `http://localhost:8765/oauth/callback` (the form Linear's docs use; `tk` listens on IPv4 and IPv6 loopback; `TK_LINEAR_REDIRECT` overrides).
2. Obtain its **public client ID** and your **human Linear user ID** (the UUID,
   not your email). The user ID can be read using `query { viewer { id } }` with
   your existing personal API key. The app's viewer ID is a different identity.
3. Build the CLI and run:

   ```sh
   tk login linear <public-client-id> <human-user-id>
   TK_MODE=linear tk next
   TK_MODE=linear tk start FOC-12
   TK_MODE=linear tk note "Implementation finished; tests remain"
   TK_MODE=linear tk handoff "Continue with edge-case tests"
   ```

Login opens the browser on macOS (otherwise use the printed URL). The callback
listener runs only during login and times out after five minutes. Login validates
that the authenticated principal is an app and the selected owner is an active
human. No client secret is needed. Scopes are `read,write,app:assignable`.

`tk next` shows the selected human's Todo/In Progress work delegated to this app
or not yet delegated. `tk start` sets the app as delegate, preserving the human
assignee; unassigned issues are assigned to the configured human. Work owned by
another person or delegated to another app is refused. `tk new` assigns the human
and delegates the app. Handoff/done preserve the human assignee and retain the
app delegation. Notes and completion comments are authenticated as the app.

This is one installed app identity per `TK_HOME`. Multiple named teammates need
separate registered apps and isolated homes; there is no profile switcher or
multi-operator installation service yet. Merely delegating in Linear does not
wake your laptop or start an agent. Start the work locally.

### Credentials and connection behavior

This preview stores OAuth credentials in an **owner-only (0600) local file**,
`$TK_HOME/linear_oauth.json` (default `~/.tk/linear_oauth.json`), not Keychain.
Writes are atomic; refresh rotation is locked across CLI/daemon processes.
Do not commit or share that file. Access tokens are sent using Bearer auth,
independent of their text prefix.

Saved OAuth credentials take precedence over personal API keys. A corrupt,
expired, or revoked app login never silently falls back to a personal identity.
Login does not change your default backend: use `TK_MODE=linear` or set
`"mode":"linear"` in your config. Restart the daemon after changing its connection.
Linear sessions are pinned to their connection; final accounting is refused on a
different backend/connection. Finish active work and let final sync complete
before replacing or removing a login.

`tk logout linear` removes local OAuth credentials only. It does not uninstall
or revoke the workspace app. An existing personal API key becomes usable again.
`tk status` displays the installed app, workspace, and selected human when an
OAuth login exists.

App-mode costs and read caches are isolated by connection. Historical
personal-key costs retain their existing storage format. Native Linear agent
session activities, assignment-triggered execution, webhook delivery, Keychain,
and retries that prevent duplicate completion comments are follow-up work.
A live OAuth installation has not yet been verified.
