package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// tkd as a systemd user service on Linux: ~/.config/systemd/user/tk-tkd.service.
// Enabled services start when the user logs in; --no-login installs it without
// enabling, so it runs only when started by hand (tk daemon start).

const systemdUnit = "tk-tkd.service"

func systemdUnitPath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "systemd", "user", systemdUnit)
}

// systemctl runs `systemctl --user ...`; tests replace it.
var systemctl = func(args ...string) error {
	out, err := exec.Command("systemctl", append([]string{"--user"}, args...)...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl --user %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func renderSystemdUnit(exe string, env map[string]string) string {
	var b strings.Builder
	b.WriteString("[Unit]\nDescription=tkd: tk background sync (cost, idle hand-back, queued writes)\n\n[Service]\n")
	fmt.Fprintf(&b, "ExecStart=%s daemon\n", systemdQuote(exe))
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "Environment=%s\n", systemdQuote(k+"="+env[k]))
	}
	b.WriteString("Restart=on-failure\nRestartSec=5\n\n[Install]\nWantedBy=default.target\n")
	return b.String()
}

// systemdQuote quotes a value for a unit file (double quotes; \ and " escaped).
func systemdQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func installSystemd(exe string, env map[string]string, atLogin bool) error {
	path := systemdUnitPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(renderSystemdUnit(exe, env)), 0o644); err != nil {
		return err
	}
	if err := systemctl("daemon-reload"); err != nil {
		return err
	}
	if atLogin {
		if err := systemctl("enable", "--now", systemdUnit); err != nil {
			return err
		}
	} else {
		_ = systemctl("disable", systemdUnit)
		if err := systemctl("restart", systemdUnit); err != nil {
			return err
		}
	}
	when := "starts at login"
	if !atLogin {
		when = "does not start at login (start it with: tk daemon start)"
	}
	fmt.Printf("tkd installed as a systemd user service (%s); running now; %s\nlogs: journalctl --user -u %s\n", path, when, systemdUnit)
	return nil
}

func uninstallSystemd() error {
	_ = systemctl("disable", "--now", systemdUnit)
	if err := os.Remove(systemdUnitPath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	_ = systemctl("daemon-reload")
	fmt.Println("tkd service removed")
	return nil
}

func systemdInfo() string {
	if !fileExists(systemdUnitPath()) {
		return "not installed (tk daemon install)"
	}
	if fileExists(filepath.Join(filepath.Dir(systemdUnitPath()), "default.target.wants", systemdUnit)) {
		return "installed (systemd), starts at login"
	}
	return "installed (systemd), does not start at login"
}
