package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// tk init / tk setup: connect coding agents to tk, the way `bd init` and
// `bd setup <recipe>` do for Beads. Three levels:
//   - native hooks (Claude Code, Codex, Cursor, Gemini CLI): the tk brief is
//     injected at session start and restored after compaction;
//   - instruction files (AGENTS.md, GEMINI.md, rules files): the agent reads the
//     tk workflow;
//   - usage: Claude Code, Codex, Gemini CLI and OpenCode logs are read for cost
//     (usage_more.go); the other tools have no local usage log tk can read.
//
// Rules: project files by default (--global only where the tool supports it);
// tk edits only its own marked section or its own hook entries; every file it
// changes is backed up to <file>.bak first; --check reports, --remove undoes.

const (
	tkBegin = "<!-- BEGIN TK INTEGRATION -->"
	tkEnd   = "<!-- END TK INTEGRATION -->"
)

// tkGuidance is the workflow text every instruction file gets (~200 tokens).
const tkGuidance = `## Issue tracking with tk

This project tracks work with the ` + "`tk`" + ` CLI. Use it instead of ad-hoc TODO notes.

- ` + "`tk next`" + ` lists your assigned issues; ` + "`tk resume <KEY>`" + ` prints an issue's brief.
- ` + "`tk start <KEY>`" + ` claims an issue and prints its brief, including the last handoff.
- ` + "`tk note \"...\"`" + ` records progress worth keeping (decisions, dead ends).
- ` + "`tk handoff \"done / left / didn't work\"`" + ` pauses: the next session (any agent) resumes from it.
- ` + "`tk done \"summary\"`" + ` finishes: the issue moves on and its token cost is recorded.

One issue at a time. Run ` + "`tk handoff`" + ` before stopping mid-task.`

type recipe struct {
	name, label string
	detect      []string // binaries on PATH, or paths (~/... or /...) only the tool creates (tk init)
	global      bool     // supports --global
	usage       string   // how its usage is captured
	install     func(c setupCtx) ([]string, error)
	check       func(c setupCtx) []checkItem
	remove      func(c setupCtx) ([]string, error)
}

type setupCtx struct {
	root   string // project directory
	global bool
	home   string
}

type checkItem struct {
	what string
	ok   bool
}

func (c setupCtx) p(rel string) string { return filepath.Join(c.root, rel) }

func recipes() []recipe {
	section := func(name, label, rel string, detect []string, usage string) recipe {
		return recipe{name: name, label: label, detect: detect, usage: usage,
			install: func(c setupCtx) ([]string, error) { return one(upsertSection(c.p(rel), tkGuidance)) },
			check:   func(c setupCtx) []checkItem { return []checkItem{{rel + " section", hasSection(c.p(rel))}} },
			remove:  func(c setupCtx) ([]string, error) { return one(removeSection(c.p(rel))) }}
	}
	owned := func(name, label, rel, header string) recipe {
		return recipe{name: name, label: label, usage: "not captured (no local usage log)",
			install: func(c setupCtx) ([]string, error) { return one(writeOwned(c.p(rel), header+tkGuidance+"\n")) },
			check:   func(c setupCtx) []checkItem { return []checkItem{{rel, fileExists(c.p(rel))}} },
			remove:  func(c setupCtx) ([]string, error) { return one(removeOwned(c.p(rel))) }}
	}
	rs := []recipe{
		{name: "claude", label: "Claude Code", detect: []string{"claude", "~/.claude/projects"}, global: true,
			usage: "captured (exact session via the hook)",
			install: func(c setupCtx) ([]string, error) {
				had := claudeHookPresent(c.claudeSettings())
				if err := claudeHookInstall(c.claudeSettings(), false, true); err != nil || had {
					return nil, err
				}
				return []string{c.claudeSettings()}, nil
			},
			check: func(c setupCtx) []checkItem {
				return []checkItem{{c.claudeSettings() + " SessionStart hook", claudeHookPresent(c.claudeSettings())}}
			},
			remove: func(c setupCtx) ([]string, error) {
				if !claudeHookPresent(c.claudeSettings()) {
					return nil, nil
				}
				return []string{c.claudeSettings()}, claudeHookInstall(c.claudeSettings(), true, true)
			}},
		{name: "codex", label: "Codex CLI", detect: []string{"codex", "~/.codex/sessions"}, global: true,
			usage: "captured (exact session via the hook)",
			install: func(c setupCtx) ([]string, error) {
				var changed []string
				dir := c.codexDir()
				for _, step := range []func() (string, error){
					func() (string, error) { return upsertHooks(filepath.Join(dir, "hooks.json"), "codex", codexHooks) },
					func() (string, error) { return enableCodexHooks(filepath.Join(dir, "config.toml")) },
					func() (string, error) { return upsertSection(c.codexAgentsMD(), tkGuidance) },
				} {
					f, err := step()
					if err != nil {
						return changed, err
					}
					if f != "" {
						changed = append(changed, f)
					}
				}
				return changed, nil
			},
			check: func(c setupCtx) []checkItem {
				dir := c.codexDir()
				items := []checkItem{
					{filepath.Join(dir, "hooks.json") + " hooks", hooksPresent(filepath.Join(dir, "hooks.json"), "codex", codexHooks)},
					{filepath.Join(dir, "config.toml") + " [features] hooks = true", codexHooksEnabled(filepath.Join(dir, "config.toml"))},
					{c.codexAgentsMD() + " section", hasSection(c.codexAgentsMD())},
				}
				if !c.global { // Codex ignores a project's .codex/ until the project is trusted
					items = append(items, checkItem{"project trusted in Codex (~/.codex/config.toml)", codexProjectTrusted(c.home, c.root)})
				}
				return items
			},
			remove: func(c setupCtx) ([]string, error) {
				a, err := removeHooks(filepath.Join(c.codexDir(), "hooks.json"), "codex")
				if err != nil {
					return nil, err
				}
				b, err := removeSection(c.codexAgentsMD())
				return compact(a, b), err // [features] hooks stays: other hooks may need it
			}},
		{name: "cursor", label: "Cursor", detect: []string{"cursor-agent", "cursor", "/Applications/Cursor.app", "~/Applications/Cursor.app"}, global: true,
			usage: "not captured (Cursor keeps no local usage log)",
			install: func(c setupCtx) ([]string, error) {
				var changed []string
				if !c.global {
					f, err := writeOwned(c.p(".cursor/rules/tk.mdc"), "---\nalwaysApply: true\n---\n\n"+tkGuidance+"\n")
					if err != nil {
						return nil, err
					}
					changed = compact(f)
				}
				f, err := upsertHooks(c.cursorHooks(), "cursor", cursorHooks)
				return append(changed, compact(f)...), err
			},
			check: func(c setupCtx) []checkItem {
				items := []checkItem{{c.cursorHooks() + " hooks", hooksPresent(c.cursorHooks(), "cursor", cursorHooks)}}
				if !c.global {
					items = append([]checkItem{{".cursor/rules/tk.mdc", fileExists(c.p(".cursor/rules/tk.mdc"))}}, items...)
				}
				return items
			},
			remove: func(c setupCtx) ([]string, error) {
				var changed []string
				if !c.global {
					f, _ := removeOwned(c.p(".cursor/rules/tk.mdc"))
					changed = compact(f)
				}
				f, err := removeHooks(c.cursorHooks(), "cursor")
				return append(changed, compact(f)...), err
			}},
		{name: "gemini", label: "Gemini CLI", detect: []string{"gemini", "~/.gemini/tmp"}, global: true,
			usage: "captured (beta)",
			install: func(c setupCtx) ([]string, error) {
				a, err := upsertHooks(c.geminiFile("settings.json"), "gemini", geminiHooks)
				if err != nil {
					return nil, err
				}
				b, err := upsertSection(c.geminiFile("GEMINI.md"), tkGuidance)
				return compact(a, b), err
			},
			check: func(c setupCtx) []checkItem {
				return []checkItem{{c.geminiFile("settings.json") + " SessionStart hook", hooksPresent(c.geminiFile("settings.json"), "gemini", geminiHooks)},
					{c.geminiFile("GEMINI.md") + " section", hasSection(c.geminiFile("GEMINI.md"))}}
			},
			remove: func(c setupCtx) ([]string, error) {
				a, err := removeHooks(c.geminiFile("settings.json"), "gemini")
				if err != nil {
					return nil, err
				}
				b, err := removeSection(c.geminiFile("GEMINI.md"))
				return compact(a, b), err
			}},
		section("opencode", "OpenCode", "AGENTS.md", []string{"opencode", "~/.local/share/opencode/opencode.db", "~/.local/share/opencode/storage"}, "captured (from its local database or files)"),
		section("factory", "Factory Droid", "AGENTS.md", nil, "not captured"),
		section("mux", "Mux", "AGENTS.md", nil, "not captured"),
		section("copilot", "GitHub Copilot CLI", ".github/copilot-instructions.md", nil, "not captured"),
		section("junie", "Junie", ".junie/guidelines.md", nil, "not captured"),
		owned("windsurf", "Windsurf", ".windsurf/rules/tk.md", ""),
		owned("cody", "Sourcegraph Cody", ".cody/rules/tk.md", ""),
		owned("kilocode", "Kilo Code", ".kilocode/rules/tk.md", ""),
		owned("kiro", "Kiro CLI", ".kiro/steering/tk.md", "---\ninclusion: always\n---\n\n"),
		{name: "aider", label: "Aider", usage: "not captured",
			install: func(c setupCtx) ([]string, error) {
				a, err := writeOwned(c.p(".aider/TK.md"), tkGuidance+"\n")
				if err != nil {
					return nil, err
				}
				b, err := aiderRead(c.p(".aider.conf.yml"), ".aider/TK.md", false)
				return compact(a, b), err
			},
			check: func(c setupCtx) []checkItem {
				raw, _ := os.ReadFile(c.p(".aider.conf.yml"))
				return []checkItem{{".aider/TK.md", fileExists(c.p(".aider/TK.md"))}, {".aider.conf.yml reads it", strings.Contains(string(raw), ".aider/TK.md")}}
			},
			remove: func(c setupCtx) ([]string, error) {
				a, _ := removeOwned(c.p(".aider/TK.md"))
				b, err := aiderRead(c.p(".aider.conf.yml"), ".aider/TK.md", true)
				return compact(a, b), err
			}},
	}
	return rs
}

// ---- per-tool locations ----

func (c setupCtx) claudeSettings() string {
	if c.global {
		return filepath.Join(c.home, ".claude", "settings.json")
	}
	return c.p(".claude/settings.local.json") // personal, not committed
}

func (c setupCtx) codexDir() string {
	if c.global {
		if d := os.Getenv("CODEX_HOME"); d != "" {
			return d
		}
		return filepath.Join(c.home, ".codex")
	}
	return c.p(".codex")
}

func (c setupCtx) codexAgentsMD() string {
	if c.global {
		return filepath.Join(c.codexDir(), "AGENTS.md")
	}
	return c.p("AGENTS.md")
}

func (c setupCtx) cursorHooks() string {
	if c.global {
		return filepath.Join(c.home, ".cursor", "hooks.json")
	}
	return c.p(".cursor/hooks.json")
}

func (c setupCtx) geminiFile(name string) string {
	if c.global {
		return filepath.Join(c.home, ".gemini", name)
	}
	if name == "GEMINI.md" {
		return c.p("GEMINI.md")
	}
	return c.p(".gemini/" + name)
}

// ---- hook definitions ----

type hookDef struct{ event, matcher, status string }

var (
	codexHooks = []hookDef{
		{"SessionStart", "startup|resume|clear", "Loading tk work brief"},
		{"PostCompact", "manual|auto", "Scheduling tk brief refresh"},
		{"UserPromptSubmit", "", ""},
	}
	cursorHooks = []hookDef{{"sessionStart", "", ""}, {"preCompact", "", ""}, {"postToolUse", "", ""}}
	geminiHooks = []hookDef{{"SessionStart", "", ""}}
)

// hookCommand is how a hook file calls tk: plain "tk" when the tk on PATH is this
// binary (portable for teammates), else this binary's absolute path.
func hookCommand(tool, event string) string {
	exe, _ := os.Executable()
	exe, _ = filepath.EvalSymlinks(exe)
	base := exe
	if p, err := exec.LookPath("tk"); err == nil {
		if rp, _ := filepath.EvalSymlinks(p); rp == exe {
			base = "tk"
		}
	}
	return shellQuote(base) + " hook " + tool + " " + event
}

func isTkHookCommand(cmd, tool string) bool {
	f := strings.Fields(cmd)
	return len(f) >= 3 && f[len(f)-3] == "hook" && f[len(f)-2] == tool
}

// upsertHooks adds tk's hooks to a JSON hooks file, replacing earlier tk entries
// and keeping everyone else's. Codex and Gemini use Claude Code's layout
// ({"hooks": {Event: [{"matcher", "hooks": [{"type","command"}]}]}}); Cursor uses
// {"version": 1, "hooks": {event: [{"command"}]}}.
func upsertHooks(path, tool string, defs []hookDef) (string, error) {
	cfg, err := readJSONObject(path)
	if err != nil {
		return "", err
	}
	stripTkHooks(cfg, tool)
	hooks, _ := cfg["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	for _, d := range defs {
		list, _ := hooks[d.event].([]any)
		cmd := hookCommand(tool, d.event)
		if tool == "cursor" {
			list = append(list, map[string]any{"command": cmd})
		} else {
			h := map[string]any{"type": "command", "command": cmd, "timeout": 10}
			if d.status != "" {
				h["statusMessage"] = d.status
			}
			group := map[string]any{"hooks": []any{h}}
			if d.matcher != "" {
				group["matcher"] = d.matcher
			}
			list = append(list, group)
		}
		hooks[d.event] = list
	}
	cfg["hooks"] = hooks
	if tool == "cursor" {
		if _, ok := cfg["version"]; !ok {
			cfg["version"] = 1
		}
	}
	return writeJSONIfChanged(path, cfg)
}

func removeHooks(path, tool string) (string, error) {
	if !fileExists(path) {
		return "", nil
	}
	cfg, err := readJSONObject(path)
	if err != nil {
		return "", err
	}
	if !stripTkHooks(cfg, tool) {
		return "", nil
	}
	return writeJSONIfChanged(path, cfg)
}

// stripTkHooks removes tk's entries for tool; it reports whether any were found.
func stripTkHooks(cfg map[string]any, tool string) bool {
	hooks, _ := cfg["hooks"].(map[string]any)
	found := false
	for event, v := range hooks {
		list, _ := v.([]any)
		var keep []any
		for _, entry := range list {
			e, _ := entry.(map[string]any)
			if c, _ := e["command"].(string); isTkHookCommand(c, tool) { // Cursor layout
				found = true
				continue
			}
			if inner, ok := e["hooks"].([]any); ok { // Claude/Codex/Gemini layout
				var innerKeep []any
				for _, h := range inner {
					hm, _ := h.(map[string]any)
					if c, _ := hm["command"].(string); isTkHookCommand(c, tool) {
						found = true
						continue
					}
					innerKeep = append(innerKeep, h)
				}
				if len(innerKeep) == 0 {
					continue
				}
				e["hooks"] = innerKeep
			}
			keep = append(keep, entry)
		}
		if len(keep) == 0 {
			delete(hooks, event)
		} else {
			hooks[event] = keep
		}
	}
	return found
}

func hooksPresent(path, tool string, defs []hookDef) bool {
	cfg, err := readJSONObject(path)
	if err != nil {
		return false
	}
	hooks, _ := cfg["hooks"].(map[string]any)
	for _, d := range defs {
		raw, _ := json.Marshal(hooks[d.event])
		if !strings.Contains(string(raw), "hook "+tool+" "+d.event) {
			return false
		}
	}
	return true
}

func claudeHookPresent(path string) bool {
	cfg, err := readJSONObject(path)
	if err != nil {
		return false
	}
	hooks, _ := cfg["hooks"].(map[string]any)
	groups, _ := hooks["SessionStart"].([]any)
	for _, g := range groups {
		if isTkHookGroup(g) {
			return true
		}
	}
	return false
}

// ---- Codex config.toml: [features] hooks = true ----

func enableCodexHooks(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if codexHooksEnabled(path) {
		return "", nil
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(raw) == 0 {
		lines = nil
	}
	out, inFeatures, done := []string{}, false, false
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "[") {
			if inFeatures && !done {
				out, done = append(out, "hooks = true"), true
			}
			inFeatures = t == "[features]"
		}
		if inFeatures && !done && strings.HasPrefix(strings.ReplaceAll(t, " ", ""), "hooks=") {
			out, done = append(out, "hooks = true"), true
			continue
		}
		out = append(out, l)
	}
	if inFeatures && !done {
		out, done = append(out, "hooks = true"), true
	}
	if !done {
		if len(out) > 0 {
			out = append(out, "")
		}
		out = append(out, "[features]", "hooks = true")
	}
	return path, writeBackedUp(path, raw, []byte(strings.Join(out, "\n")+"\n"))
}

func codexHooksEnabled(path string) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	in := false
	for _, l := range strings.Split(string(raw), "\n") {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "[") {
			in = t == "[features]"
			continue
		}
		if in && strings.ReplaceAll(t, " ", "") == "hooks=true" {
			return true
		}
	}
	return false
}

// ---- Aider: .aider.conf.yml read: list ----

func aiderRead(conf, file string, remove bool) (string, error) {
	raw, err := os.ReadFile(conf)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	text := string(raw)
	has := strings.Contains(text, file)
	switch {
	case remove && !has, !remove && has:
		return "", nil
	case remove:
		var keep []string
		for _, l := range strings.Split(text, "\n") {
			if strings.TrimSpace(l) != "- "+file {
				keep = append(keep, l)
			}
		}
		return conf, writeBackedUp(conf, raw, []byte(strings.Join(keep, "\n")))
	case len(raw) == 0:
		return conf, writeBackedUp(conf, raw, []byte("read:\n  - "+file+"\n"))
	}
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) == "read:" && !strings.HasPrefix(l, " ") {
			lines = append(lines[:i+1], append([]string{"  - " + file}, lines[i+1:]...)...)
			return conf, writeBackedUp(conf, raw, []byte(strings.Join(lines, "\n")+"\n"))
		}
		if strings.HasPrefix(l, "read:") {
			return "", fmt.Errorf("%s has a one-line `read:` value; add %s to it by hand", conf, file)
		}
	}
	return conf, writeBackedUp(conf, raw, []byte(strings.TrimRight(text, "\n")+"\nread:\n  - "+file+"\n"))
}

// ---- files ----

func upsertSection(path, body string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	block := tkBegin + "\n<!-- Managed by `tk setup`; edits inside these markers are replaced. -->\n" + body + "\n" + tkEnd
	text := string(raw)
	var next string
	if i, j := strings.Index(text, tkBegin), strings.Index(text, tkEnd); i >= 0 && j > i {
		next = text[:i] + block + text[j+len(tkEnd):]
	} else if strings.TrimSpace(text) == "" {
		next = block + "\n"
	} else {
		next = strings.TrimRight(text, "\n") + "\n\n" + block + "\n"
	}
	if next == text {
		return "", nil
	}
	return path, writeBackedUp(path, raw, []byte(next))
}

func removeSection(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", nil
	}
	text := string(raw)
	i, j := strings.Index(text, tkBegin), strings.Index(text, tkEnd)
	if i < 0 || j < i {
		return "", nil
	}
	next := strings.TrimRight(text[:i], "\n") + text[j+len(tkEnd):]
	if strings.TrimSpace(next) == "" {
		return path, os.Remove(path)
	}
	return path, writeBackedUp(path, raw, []byte(strings.TrimLeft(next, "\n")))
}

func hasSection(path string) bool {
	raw, _ := os.ReadFile(path)
	return strings.Contains(string(raw), tkBegin) && strings.Contains(string(raw), tkEnd)
}

// writeOwned writes a file that belongs to tk entirely (rules files named tk.*).
func writeOwned(path, content string) (string, error) {
	raw, err := os.ReadFile(path)
	if err == nil && string(raw) == content {
		return "", nil
	}
	return path, writeBackedUp(path, raw, []byte(content))
}

func removeOwned(path string) (string, error) {
	if !fileExists(path) {
		return "", nil
	}
	return path, os.Remove(path)
}

func readJSONObject(path string) (map[string]any, error) {
	cfg := map[string]any{}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return cfg, nil
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON; not changing it: %w", path, err)
	}
	return cfg, nil
}

// writeJSONIfChanged writes cfg only when it differs from the file's current
// content; it returns the path when it wrote.
func writeJSONIfChanged(path string, cfg map[string]any) (string, error) {
	old, _ := os.ReadFile(path)
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", err
	}
	out = append(out, '\n')
	if len(old) > 0 {
		var a, b any
		if json.Unmarshal(old, &a) == nil && json.Unmarshal(out, &b) == nil {
			ra, _ := json.Marshal(a)
			rb, _ := json.Marshal(b)
			if string(ra) == string(rb) {
				return "", nil // same content (maybe formatted differently): leave the file alone
			}
		}
	}
	return path, writeBackedUp(path, old, out)
}

// writeBackedUp saves the previous content to <path>.bak (when there was any)
// and writes the new content atomically.
func writeBackedUp(path string, old, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if len(old) > 0 {
		if err := os.WriteFile(path+".bak", old, 0o600); err != nil {
			return err
		}
	}
	tmp := path + ".tk-tmp"
	if err := os.WriteFile(tmp, content, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func one(path string, err error) ([]string, error) { return compact(path), err }

func compact(paths ...string) []string {
	var out []string
	for _, p := range paths {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ---- commands ----

func findRecipe(name string) (recipe, bool) {
	for _, r := range recipes() {
		if r.name == strings.ToLower(name) {
			return r, true
		}
	}
	return recipe{}, false
}

func newSetupCtx(flags map[string]bool) (setupCtx, error) {
	root, err := os.Getwd()
	if err != nil {
		return setupCtx{}, err
	}
	home, _ := os.UserHomeDir()
	return setupCtx{root: root, global: flags["global"], home: home}, nil
}

// detectedBy returns what showed the tool is installed, or "" if nothing did.
// A tool's config folder alone isn't enough: other products create them too
// (Antigravity creates ~/.gemini), so only the CLI or a tool-specific path counts.
func detectedBy(r recipe, home string) string {
	for _, d := range r.detect {
		switch {
		case strings.HasPrefix(d, "~/"):
			if _, err := os.Stat(filepath.Join(home, d[2:])); err == nil {
				return d
			}
		case strings.HasPrefix(d, "/"):
			if _, err := os.Stat(d); err == nil {
				return d
			}
		default:
			if _, err := exec.LookPath(d); err == nil {
				return "`" + d + "` on PATH"
			}
		}
	}
	return ""
}

func detected(r recipe, home string) bool { return detectedBy(r, home) != "" }

func rel(root, p string) string {
	if r, err := filepath.Rel(root, p); err == nil && !strings.HasPrefix(r, "..") {
		return r
	}
	return p
}

// cmdSetup: tk setup --list | tk setup <tool> [--check | --remove] [--global]
func cmdSetup(args []string, flags map[string]bool) error {
	c, err := newSetupCtx(flags)
	if err != nil {
		return err
	}
	if flags["list"] || len(args) == 0 {
		fmt.Printf("%-9s %-19s %-9s %-8s %s\n", "recipe", "tool", "found", "global", "usage")
		for _, r := range recipes() {
			found := ""
			if detected(r, c.home) {
				found = "yes"
			}
			global := ""
			if r.global {
				global = "yes"
			}
			fmt.Printf("%-9s %-19s %-9s %-8s %s\n", r.name, r.label, found, global, r.usage)
		}
		fmt.Println("\ntk setup <recipe> [--check | --remove] [--global];  tk init sets up every tool found.")
		return nil
	}
	r, ok := findRecipe(args[0])
	if !ok {
		return fmt.Errorf("unknown recipe %q (tk setup --list)", args[0])
	}
	if c.global && !r.global {
		return fmt.Errorf("%s has no global setup; run tk setup %s in the project", r.label, r.name)
	}
	switch {
	case flags["check"]:
		missing := 0
		for _, it := range r.check(c) {
			mark := "✓"
			if !it.ok {
				mark, missing = "✗", missing+1
			}
			fmt.Printf("%s %s\n", mark, rel(c.root, it.what))
		}
		if missing > 0 {
			if r.name == "codex" && !c.global && !codexProjectTrusted(c.home, c.root) {
				fmt.Println(codexTrustNote)
			}
			return fmt.Errorf("%s: %d part(s) missing; run tk setup %s (files) or approve in Codex (trust)", r.label, missing, r.name)
		}
		fmt.Printf("%s: set up\n", r.label)
		return nil
	case flags["remove"]:
		changed, err := r.remove(c)
		report(c, r, "removed from", changed)
		return err
	default:
		changed, err := r.install(c)
		report(c, r, "set up in", changed)
		return err
	}
}

func report(c setupCtx, r recipe, verb string, changed []string) {
	if len(changed) == 0 {
		fmt.Printf("%s: nothing to change\n", r.label)
		return
	}
	var names []string
	for _, p := range changed {
		names = append(names, rel(c.root, p))
	}
	sort.Strings(names)
	fmt.Printf("%s %s %s\n", r.label, verb, strings.Join(names, ", "))
	if r.name == "codex" && verb == "set up in" {
		fmt.Println(codexTrustNote)
	}
}

// Codex runs project hooks only after two approvals, which tk must not bypass.
const codexTrustNote = `  Codex needs two approvals before these hooks run: trust this project (Codex asks the first
  time you open it here), then approve the tk hooks with /hooks inside Codex. Check: tk setup codex --check`

// codexProjectTrusted reports whether ~/.codex/config.toml marks root as trusted.
func codexProjectTrusted(home, root string) bool {
	cfgDir := os.Getenv("CODEX_HOME")
	if cfgDir == "" {
		cfgDir = filepath.Join(home, ".codex")
	}
	raw, err := os.ReadFile(filepath.Join(cfgDir, "config.toml"))
	if err != nil {
		return false
	}
	want := map[string]bool{root: true, realPath(root): true}
	current := ""
	for _, l := range strings.Split(string(raw), "\n") {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "[") {
			current = ""
			if strings.HasPrefix(t, `[projects."`) && strings.HasSuffix(t, `"]`) {
				current = strings.TrimSuffix(strings.TrimPrefix(t, `[projects."`), `"]`)
			}
			continue
		}
		if want[current] && strings.ReplaceAll(t, " ", "") == `trust_level="trusted"` {
			return true
		}
	}
	return false
}

// cmdInit sets up every detected agent in this project (Beads' `bd init` default).
// --skip-agents does nothing but print what would be set up.
func cmdInit(flags map[string]bool) error {
	c, err := newSetupCtx(flags)
	if err != nil {
		return err
	}
	var found []recipe
	for _, r := range recipes() {
		if len(r.detect) > 0 && detected(r, c.home) {
			found = append(found, r)
		}
	}
	fmt.Printf("tk init in %s (mode: %s)\n", tildePath(c.root), configuredMode())
	if len(found) == 0 {
		fmt.Println("No coding agents found (Claude Code, Codex, Cursor, Gemini CLI, OpenCode). Add one with: tk setup <recipe>")
	}
	var failed []string
	for _, r := range found {
		if flags["skip-agents"] {
			fmt.Printf("  would set up %s (skipped)\n", r.label)
			continue
		}
		changed, err := r.install(c)
		if err != nil {
			failed = append(failed, r.label+": "+err.Error())
			continue
		}
		state := "already set up"
		if len(changed) > 0 {
			var names []string
			for _, p := range changed {
				names = append(names, rel(c.root, p))
			}
			sort.Strings(names)
			state = strings.Join(names, ", ")
		}
		fmt.Printf("  ✓ %-12s %-22s %s\n", r.label, costNote(r.usage), state)
		if r.name == "codex" && !codexProjectTrusted(c.home, c.root) {
			fmt.Println(codexTrustNote)
		}
	}
	fmt.Println("More agents: tk setup --list.  Background cost sync: tk daemon install.")
	fmt.Println("Restart running agents so they load the new hooks and instructions.")
	if len(failed) > 0 {
		return errors.New(strings.Join(failed, "; "))
	}
	return nil
}

// costNote says in a few words whether tk can count this agent's cost.
func costNote(usage string) string {
	switch {
	case strings.Contains(usage, "beta") && strings.HasPrefix(usage, "captured"):
		return "cost per issue (beta)"
	case strings.HasPrefix(usage, "captured"):
		return "cost per issue"
	}
	return "brief only, no cost data"
}
