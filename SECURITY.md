# Security: Tiimi (`tk`)

## Reporting

Please report vulnerabilities privately to security@tiimi.dev (live once the tiimi.dev mail is set
up; until then, a private GitHub security advisory on tiimihq/tk), not in public issues.

## What tk stores

| What | Where | Protection |
| --- | --- | --- |
| API keys, tokens, app logins | macOS Keychain, or Linux Secret Service (`secret-tool`); otherwise `~/.tk/secrets/` | OS store, or owner-only files (0600 in a 0700 folder) |
| Issues (local mode), sessions, cost records, cache, queued writes | `~/.tk` | owner-only |
| Hook and instruction files | your project (`.claude/`, `.codex/`, `.cursor/`, `AGENTS.md`, …) | no secrets; only tk's marked sections are changed; `.bak` copies are kept |

Secret values never appear on a command line (the Keychain gets them through `security -i` on
stdin), in output or in logs. Older key files in `~/.tk` are still read; `tk secret import` moves
them into the OS store.

## Network

tk connects only to: the tracker you configure (Linear, Jira, GitHub, Paperclip); `github.com` for
`tk login github` and `tk cost update`; your browser on `localhost:8765` during `tk login linear`.
App logins use PKCE or GitHub's device flow, so no client secret ships with tk. There is no
telemetry and no automatic update or price check.

## Guards

- Text from trackers is stripped of terminal control characters (escape sequences, bidi overrides)
  before tk prints it.
- The agent's brief labels tracker text as task data, not instructions. Issue text is still
  something your agent reads; treat issues from untrusted people with the care you'd give any input
  to an agent.
- Issue keys and session ids are sanitised before they become file names.
- Responses are size-limited; the OAuth callback listens on loopback only and checks `state`.
- Hook commands quote tk's path for the shell.
- `install.sh` refuses an archive whose SHA-256 doesn't match `checksums.txt`. That proves the
  download is intact, not who published it: macOS release binaries will be signed with a Developer ID
  and notarized, and Linux releases will carry a signature (not set up yet).

## Known limits

- A queued write whose first attempt reached the tracker but whose answer was lost can be posted
  twice when it is replayed.
- Codex and Claude Code decide whether project hooks run; tk never bypasses Codex's project and hook
  approval.
