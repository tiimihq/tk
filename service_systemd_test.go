package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSystemdService(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	var calls []string
	old := systemctl
	systemctl = func(args ...string) error {
		calls = append(calls, strings.Join(args, " "))
		if len(args) > 1 && args[0] == "enable" { // what systemctl enable does on disk
			wants := filepath.Join(cfg, "systemd", "user", "default.target.wants")
			_ = os.MkdirAll(wants, 0o755)
			_ = os.WriteFile(filepath.Join(wants, systemdUnit), nil, 0o644)
		}
		return nil
	}
	t.Cleanup(func() { systemctl = old })

	exe := `/opt/my tools/tk"x`
	captureStdout(t, func() {
		if err := installSystemd(exe, map[string]string{"TK_HOME": "/home/u/.tk"}, true); err != nil {
			t.Fatal(err)
		}
	})
	unit, _ := os.ReadFile(systemdUnitPath())
	u := string(unit)
	if !strings.Contains(u, `ExecStart="/opt/my tools/tk\"x" daemon`) || !strings.Contains(u, `Environment="TK_HOME=/home/u/.tk"`) ||
		!strings.Contains(u, "WantedBy=default.target") || !strings.Contains(u, "Restart=on-failure") {
		t.Fatalf("unit file:\n%s", u)
	}
	if got := strings.Join(calls, "; "); got != "daemon-reload; enable --now "+systemdUnit {
		t.Fatalf("systemctl calls = %s", got)
	}
	if info := systemdInfo(); info != "installed (systemd), starts at login" {
		t.Fatalf("info = %q", info)
	}

	calls = nil
	captureStdout(t, func() { _ = uninstallSystemd() })
	if fileExists(systemdUnitPath()) || calls[0] != "disable --now "+systemdUnit {
		t.Fatalf("uninstall: calls %v", calls)
	}

	calls = nil
	captureStdout(t, func() { _ = installSystemd("/usr/bin/tk", nil, false) })
	if got := strings.Join(calls, "; "); got != "daemon-reload; disable "+systemdUnit+"; restart "+systemdUnit {
		t.Fatalf("--no-login calls = %s", got)
	}
}
