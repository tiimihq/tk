// Command tk is the agent-facing work loop: compact views of assigned work and
// start/note/handoff/done for sessions developers run themselves. Work lives in
// a backend: on this machine (local mode, the default) or on a Paperclip server
// through the tk-bridge plugin.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

const usage = `tk - Tiimi (tiimi.dev): your coding agents as teammates. Issues, handoffs and cost per issue, in your tracker

Work on an issue (you or your agent):
  tk next                    your assigned issues, one line each
  tk start <KEY>             claim an issue and print its brief (with the last handoff)
  tk note "<text>"           record progress on the issue you're working on
  tk handoff "<note>"        pause: what's done, what's left, what didn't work
  tk done "<summary>"        finish: move the issue on and record its cost
  tk peek <KEY> | tk resume <KEY> [--full]   one-line status | full brief

Issues on this machine (local mode):
  tk new "<title>" [-- description]   create an issue (also Linear, Jira, GitHub)
  tk board                   terminal board: create, edit, move, comment, cancel
  tk list [--all] | tk edit <KEY> field=value... | tk comment <KEY> "<text>" | tk cancel <KEY> [reason]

Cost:
  tk cost                    record this session's token usage now (tkd does it every 30s)
  tk cost update             download the latest model prices and show what changed

Connect agents and trackers:
  tk init                    set up every coding agent found here (Claude Code, Codex, Cursor, Gemini, OpenCode)
  tk setup [--list] | tk setup <tool> [--check | --remove] [--global]
  tk login linear [client-id] [you]    agent app login for Linear (you stay the owner)
  tk login github [client-id]          GitHub App login (comments show you + tk)
  tk logout linear | github
  tk secret set | list | delete | import   keys in the macOS Keychain / Linux Secret Service

Background service (tkd):
  tk daemon install [--no-login] | uninstall | start | stop      macOS launchd or Linux systemd
  tk daemon                  run it in the foreground
  tk status                  mode, tkd, open sessions, queued writes

Other: tk version, tk measure <KEY> (Paperclip), tk hook install (Claude Code only), --json on reads.

Mode: local (default, no account), linear, jira, github or paperclip, in ~/.tk/config.json or TK_MODE.
Several agents at once: one session per folder (git worktrees); TK_ISSUE=<KEY> picks one explicitly.
Docs: https://tiimi.dev tkd timings: TKD_REFRESH (15s), TKD_COST_INTERVAL (30s), TKD_IDLE_TIMEOUT (30m).`

const defaultSummaryChars = 700

// version is set at release time: -ldflags "-X main.version=1.2.3".
var version = "dev"

func main() {
	args, flags := splitFlags(os.Args[1:])
	if len(args) == 0 || args[0] == "help" || flags["help"] {
		fmt.Println(usage)
		return
	}
	if args[0] == "version" || flags["version"] {
		fmt.Println("tk " + version + " (Tiimi, https://tiimi.dev)")
		return
	}
	switch {
	case args[0] == "login" && len(args) > 1 && args[1] == "linear":
		check(cmdLinearLogin(args[2:]))
		return
	case args[0] == "logout" && len(args) == 2 && args[1] == "linear":
		check(cmdLinearLogout())
		return
	case args[0] == "login" && len(args) > 1 && args[1] == "github":
		check(cmdGitHubLogin(args[2:]))
		return
	case args[0] == "logout" && len(args) == 2 && args[1] == "github":
		check(cmdGitHubLogout())
		return
	case args[0] == "status":
		check(cmdStatus())
		return
	case args[0] == "cost" && len(args) > 1 && args[1] == "update": // needs no backend
		check(cmdCostUpdate())
		return
	case args[0] == "secret":
		check(cmdSecret(args[1:]))
		return
	case args[0] == "init":
		check(cmdInit(flags))
		return
	case args[0] == "setup":
		check(cmdSetup(args[1:], flags))
		return
	case args[0] == "hook" && len(args) > 2 && (args[1] == "codex" || args[1] == "cursor" || args[1] == "gemini"):
		cmdAgentHook(args[1], args[2], os.Stdin, os.Stdout) // never fails: the agent must always run
		return
	case args[0] == "hook" && len(args) > 1:
		switch args[1] {
		case "session-start":
			cmdHookSessionStart() // never fails: Claude Code must always start
		case "install", "uninstall":
			check(cmdHookInstall(args[1] == "uninstall"))
		default:
			fmt.Fprintln(os.Stderr, "usage: tk hook [install | uninstall | session-start]")
			os.Exit(2)
		}
		return
	case args[0] == "daemon" && len(args) > 1:
		check(cmdDaemonService(args[1], flags))
		return
	}

	be, err := openBackend()
	check(err)
	rest := strings.Join(args[1:], " ")
	switch args[0] {
	case "next":
		err = cmdNext(be, flags)
	case "peek":
		err = withKey(args, func(k string) error { return cmdPeek(be, k, flags) })
	case "resume":
		err = withKey(args, func(k string) error { return cmdResume(be, k, flags) })
	case "new":
		err = cmdNew(be, args[1:])
	case "list":
		err = cmdList(be, flags)
	case "edit":
		err = withKey(args, func(k string) error { return cmdEdit(be, k, args[2:]) })
	case "comment":
		err = withKey(args, func(k string) error { return cmdComment(be, k, strings.Join(args[2:], " ")) })
	case "cancel":
		err = withKey(args, func(k string) error { return cmdCancel(be, k, strings.Join(args[2:], " ")) })
	case "board":
		err = cmdBoard(be)
	case "measure":
		err = withKey(args, func(k string) error { return cmdMeasure(be, k) })
	case "start":
		err = withKey(args, func(k string) error { return cmdStart(be, k) })
	case "note":
		err = cmdNote(be, rest)
	case "handoff":
		err = cmdFinish(be, "handoff", rest)
	case "done":
		err = cmdFinish(be, "done", rest)
	case "cost":
		err = cmdCost(be, args[1:])
	case "daemon":
		err = cmdDaemon(be)
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	check(err)
}

func cmdNext(be backend, flags map[string]bool) error {
	items, err := be.inbox()
	if err != nil {
		return err
	}
	if flags["json"] {
		return printJSON(items)
	}
	if len(items) == 0 {
		fmt.Println("no assigned work")
		return nil
	}
	for _, it := range items {
		fmt.Println(formatInboxLine(it))
	}
	return nil
}

func cmdPeek(be backend, key string, flags map[string]bool) error {
	is, err := be.issue(key)
	if err != nil {
		return err
	}
	if flags["json"] {
		return printJSON(is)
	}
	line := formatPeek(is)
	if cr, ok := be.(costReporter); ok { // backends that keep cost on this machine
		if t, err := cr.costSummary(key); err == nil && t.Events > 0 {
			line += " | cost " + t.String()
		}
	}
	fmt.Println(line)
	return nil
}

func cmdResume(be backend, key string, flags map[string]bool) error {
	hc, err := be.context(key)
	if err != nil {
		return err
	}
	if flags["json"] {
		return printJSON(hc)
	}
	limit := defaultSummaryChars
	if flags["full"] {
		limit = 0
	}
	fmt.Println(formatResume(hc, limit))
	return nil
}

// cmdNew creates an issue: tk new <title words> [-- <description words>].
func cmdNew(be backend, args []string) error {
	title, desc := strings.Join(args, " "), ""
	if i := indexOf(args, "--"); i >= 0 {
		title, desc = strings.Join(args[:i], " "), strings.Join(args[i+1:], " ")
	}
	if strings.TrimSpace(title) == "" {
		return errors.New(`usage: tk new <title> [-- description]`)
	}
	is, err := be.createIssue(strings.TrimSpace(title), strings.TrimSpace(desc))
	if err != nil {
		return err
	}
	fmt.Printf("%s created | %s\n", is.Identifier, is.Title)
	return nil
}

// cmdMeasure prints raw vs. compact sizes; tokens are estimated as bytes / 4.
func cmdMeasure(be backend, key string) error {
	pb, ok := be.(*paperclipBackend)
	if !ok {
		return errors.New("tk measure compares against Paperclip's raw responses; it needs Paperclip mode")
	}
	issueRaw, err := pb.c.get(issuePath(key, ""))
	if err != nil {
		return err
	}
	ctxRaw, err := pb.c.get(issuePath(key, "/heartbeat-context"))
	if err != nil {
		return err
	}
	var is issue
	if err := json.Unmarshal(sanitizeJSON(issueRaw), &is); err != nil {
		return err
	}
	hc, err := parseContext(ctxRaw)
	if err != nil {
		return err
	}
	rows := []struct {
		name     string
		raw, out string
	}{
		{"peek  (vs full issue)", string(issueRaw), formatPeek(is)},
		{"resume (vs heartbeat-context)", string(ctxRaw), formatResume(hc, defaultSummaryChars)},
	}
	fmt.Printf("%-32s %10s %10s %8s\n", "command", "raw ~tok", "tk ~tok", "saved")
	for _, r := range rows {
		rt, tt := len(r.raw)/4, len(r.out)/4
		saved := 0
		if rt > 0 {
			saved = 100 - tt*100/rt
		}
		fmt.Printf("%-32s %10d %10d %7d%%\n", r.name, rt, tt, saved)
	}
	return nil
}

// parseContext tolerates raw control characters Paperclip can leave in summary text.
func parseContext(raw []byte) (heartbeatContext, error) {
	var hc heartbeatContext
	if err := json.Unmarshal(sanitizeJSON(raw), &hc); err != nil {
		return hc, fmt.Errorf("unexpected heartbeat-context format: %w", err)
	}
	return hc, nil
}

func sanitizeJSON(raw []byte) []byte {
	out := make([]byte, 0, len(raw))
	inString, escaped := false, false
	for _, ch := range raw {
		switch {
		case escaped:
			escaped = false
		case ch == '\\' && inString:
			escaped = true
		case ch == '"':
			inString = !inString
		case inString && ch < 0x20:
			out = append(out, []byte(fmt.Sprintf(`\u%04x`, ch))...)
			continue
		}
		out = append(out, ch)
	}
	return out
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func indexOf(xs []string, x string) int {
	for i, v := range xs {
		if v == x {
			return i
		}
	}
	return -1
}

func withKey(args []string, fn func(string) error) error {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	return fn(strings.ToUpper(args[1]))
}

// splitFlags separates --flags from arguments; a bare "--" is kept as an argument
// (tk new uses it to separate title from description).
func splitFlags(in []string) ([]string, map[string]bool) {
	flags := map[string]bool{}
	var args []string
	for _, a := range in {
		if strings.HasPrefix(a, "--") && a != "--" {
			flags[strings.TrimPrefix(a, "--")] = true
			continue
		}
		args = append(args, a)
	}
	return args, flags
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "tk: "+err.Error())
		os.Exit(1)
	}
}
