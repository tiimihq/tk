package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"golang.org/x/term"
)

// Secrets (API keys, tokens, OAuth credentials) live in the OS secret store:
// the macOS Keychain, or the Secret Service on Linux (via secret-tool). Where
// neither is available they fall back to owner-only files in ~/.tk/secrets.
// Lookup order for a secret: its environment variables, the store, then the
// legacy file tk used before (still read so existing setups keep working;
// `tk secret import` moves those into the store).
//
// Values never appear on a command line (other processes can read those): the
// Keychain gets them through `security -i` on stdin, secret-tool on stdin.

const secretService = "tk"

// knownSecrets: name -> legacy file in ~/.tk and what it is.
var knownSecrets = map[string]struct{ legacy, what string }{
	"linear":       {"linear_key", "Linear personal API key"},
	"linear-oauth": {"linear_oauth.json", "Linear app login (tk login linear)"},
	"github":       {"github_token", "GitHub token"},
	"github-oauth": {"github_oauth.json", "GitHub app login (tk login github)"},
	"jira":         {"jira_token", "Jira API token"},
	"paperclip":    {"agent_token.json", "Paperclip agent key"},
}

type secretStore interface {
	kind() string
	get(name string) (string, bool, error)
	set(name, value string) error
	del(name string) error
}

func activeSecretStore() secretStore {
	switch strings.ToLower(os.Getenv("TK_SECRET_STORE")) {
	case "file":
		return fileSecrets{}
	case "keychain":
		return keychainSecrets{}
	case "secret-tool":
		return secretToolSecrets{}
	}
	if runtime.GOOS == "darwin" && fileExists("/usr/bin/security") {
		return keychainSecrets{}
	}
	if runtime.GOOS == "linux" && os.Getenv("DBUS_SESSION_BUS_ADDRESS") != "" {
		if _, err := exec.LookPath("secret-tool"); err == nil {
			return secretToolSecrets{}
		}
	}
	return fileSecrets{}
}

// getSecret returns the secret from the environment, the store, or the legacy file.
func getSecret(name string, envs ...string) string {
	for _, e := range envs {
		if v := strings.TrimSpace(os.Getenv(e)); v != "" {
			return v
		}
	}
	if v, ok, err := activeSecretStore().get(name); err == nil && ok {
		return v
	}
	if k, ok := knownSecrets[name]; ok {
		if raw, err := os.ReadFile(filepath.Join(tkHome(), k.legacy)); err == nil {
			return strings.TrimSpace(string(raw))
		}
	}
	return ""
}

// setSecret saves to the store; if the store fails it falls back to a file and says so.
func setSecret(name, value string) error {
	st := activeSecretStore()
	if err := st.set(name, value); err != nil {
		if _, isFile := st.(fileSecrets); isFile {
			return err
		}
		fmt.Fprintf(os.Stderr, "tk: %s unavailable (%v); saving %s to an owner-only file instead\n", st.kind(), err, name)
		return fileSecrets{}.set(name, value)
	}
	return nil
}

// deleteSecret removes the secret from the store, the fallback file and the legacy file.
func deleteSecret(name string) error {
	err := activeSecretStore().del(name)
	_ = fileSecrets{}.del(name)
	if k, ok := knownSecrets[name]; ok {
		_ = os.Remove(filepath.Join(tkHome(), k.legacy))
	}
	return err
}

// ---- macOS Keychain ----

type keychainSecrets struct{}

func (keychainSecrets) kind() string { return "macOS Keychain" }

// Values are stored base64-encoded with a prefix, so any bytes survive the
// `security -i` command line and are recognisably tk's.
const keychainPrefix = "tk1:"

func (keychainSecrets) get(name string) (string, bool, error) {
	out, err := exec.Command("/usr/bin/security", "find-generic-password", "-s", secretService, "-a", name, "-w").Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 44 { // item not found
			return "", false, nil
		}
		return "", false, fmt.Errorf("keychain: %w", err)
	}
	v := strings.TrimRight(string(out), "\n")
	if strings.HasPrefix(v, keychainPrefix) {
		b, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(v, keychainPrefix))
		if err != nil {
			return "", false, fmt.Errorf("keychain item %s is corrupt", name)
		}
		v = string(b)
	}
	return v, true, nil
}

func (keychainSecrets) set(name, value string) error {
	if !safeSecretName(name) {
		return fmt.Errorf("bad secret name %q", name)
	}
	cmd := exec.Command("/usr/bin/security", "-i")
	enc := keychainPrefix + base64.StdEncoding.EncodeToString([]byte(value))
	cmd.Stdin = strings.NewReader(fmt.Sprintf("add-generic-password -U -s %s -a %s -l %q -w %s\n", secretService, name, "tk "+name, enc))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if out, err := cmd.Output(); err != nil || strings.Contains(string(out)+stderr.String(), "rror") {
		return fmt.Errorf("keychain: %v %s", err, strings.TrimSpace(stderr.String()))
	}
	if got, ok, err := (keychainSecrets{}).get(name); err != nil || !ok || got != value { // read back
		return fmt.Errorf("keychain: saved value didn't read back")
	}
	return nil
}

func (keychainSecrets) del(name string) error {
	err := exec.Command("/usr/bin/security", "delete-generic-password", "-s", secretService, "-a", name).Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 44 {
		return nil
	}
	return err
}

func safeSecretName(n string) bool {
	if n == "" {
		return false
	}
	for _, r := range n {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

// ---- Linux Secret Service (secret-tool, from libsecret) ----

type secretToolSecrets struct{}

func (secretToolSecrets) kind() string { return "Secret Service (secret-tool)" }

func (secretToolSecrets) get(name string) (string, bool, error) {
	out, err := exec.Command("secret-tool", "lookup", "service", secretService, "account", name).Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(out) == 0 {
			return "", false, nil
		}
		return "", false, err
	}
	return strings.TrimRight(string(out), "\n"), true, nil
}

func (secretToolSecrets) set(name, value string) error {
	cmd := exec.Command("secret-tool", "store", "--label", "tk "+name, "service", secretService, "account", name)
	cmd.Stdin = strings.NewReader(value)
	return cmd.Run()
}

func (secretToolSecrets) del(name string) error {
	return exec.Command("secret-tool", "clear", "service", secretService, "account", name).Run()
}

// ---- owner-only files (fallback) ----

type fileSecrets struct{}

func (fileSecrets) kind() string            { return "owner-only files (~/.tk/secrets)" }
func (fileSecrets) path(name string) string { return filepath.Join(tkHome(), "secrets", name) }

func (f fileSecrets) get(name string) (string, bool, error) {
	raw, err := os.ReadFile(f.path(name))
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return string(raw), true, nil
}

func (f fileSecrets) set(name, value string) error {
	if !safeSecretName(name) {
		return fmt.Errorf("bad secret name %q", name)
	}
	if err := os.MkdirAll(filepath.Dir(f.path(name)), 0o700); err != nil {
		return err
	}
	tmp := f.path(name) + ".tmp"
	if err := os.WriteFile(tmp, []byte(value), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, f.path(name))
}

func (f fileSecrets) del(name string) error {
	err := os.Remove(f.path(name))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// ---- tk secret ----

func cmdSecret(args []string) error {
	usage := errors.New("usage: tk secret set <name> | delete <name> | list | import   (names: " + strings.Join(secretNames(), ", ") + ")")
	if len(args) == 0 {
		return usage
	}
	switch args[0] {
	case "list":
		st := activeSecretStore()
		fmt.Println("store: " + st.kind())
		for _, n := range secretNames() {
			where := ""
			if _, ok, _ := st.get(n); ok {
				where = st.kind()
			} else if fileExists(filepath.Join(tkHome(), knownSecrets[n].legacy)) {
				where = "legacy file ~/.tk/" + knownSecrets[n].legacy + " (tk secret import moves it)"
			} else if _, ok, _ := (fileSecrets{}).get(n); ok {
				where = "owner-only file"
			}
			if where != "" {
				fmt.Printf("  %-13s %-38s %s\n", n, knownSecrets[n].what, where)
			}
		}
		return nil
	case "set":
		if len(args) < 2 {
			return usage
		}
		name := args[1]
		if _, ok := knownSecrets[name]; !ok {
			return fmt.Errorf("unknown secret %q (%s)", name, strings.Join(secretNames(), ", "))
		}
		value, err := readSecretValue("Paste " + knownSecrets[name].what + " (input hidden): ")
		if err != nil {
			return err
		}
		if value == "" {
			return errors.New("nothing entered; not saved")
		}
		if err := setSecret(name, value); err != nil {
			return err
		}
		fmt.Printf("%s saved to %s\n", name, activeSecretStore().kind())
		return nil
	case "delete":
		if len(args) < 2 {
			return usage
		}
		if err := deleteSecret(args[1]); err != nil {
			return err
		}
		fmt.Printf("%s deleted\n", args[1])
		return nil
	case "import":
		return importLegacySecrets()
	}
	return usage
}

func secretNames() []string {
	var n []string
	for k := range knownSecrets {
		n = append(n, k)
	}
	sort.Strings(n)
	return n
}

// readSecretValue reads one line without echo from a terminal, or from piped stdin.
func readSecretValue(prompt string) (string, error) {
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		fmt.Fprint(os.Stderr, prompt)
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		return strings.TrimSpace(string(b)), err
	}
	line, err := bufio.NewReader(io.LimitReader(os.Stdin, 64<<10)).ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// importLegacySecrets moves secrets from the old ~/.tk files into the store,
// deleting each file only after the store returns the same value.
func importLegacySecrets() error {
	st := activeSecretStore()
	if _, isFile := st.(fileSecrets); isFile {
		return errors.New("no OS secret store here (macOS Keychain or secret-tool); the files stay where they are")
	}
	moved := 0
	for _, n := range secretNames() {
		legacy := filepath.Join(tkHome(), knownSecrets[n].legacy)
		raw, err := os.ReadFile(legacy)
		if err != nil {
			continue
		}
		v := strings.TrimSpace(string(raw))
		if err := st.set(n, v); err != nil {
			return fmt.Errorf("%s: %w (file kept)", n, err)
		}
		if got, ok, err := st.get(n); err != nil || !ok || got != v {
			return fmt.Errorf("%s: %s didn't return the saved value; file kept", n, st.kind())
		}
		if err := os.Remove(legacy); err != nil {
			return err
		}
		fmt.Printf("  %-13s moved from ~/.tk/%s to %s\n", n, knownSecrets[n].legacy, st.kind())
		moved++
	}
	if moved == 0 {
		fmt.Println("no legacy secret files to move")
	}
	return nil
}
