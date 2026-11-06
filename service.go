package main

import (
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// tkd as a macOS login service (launchd user agent). Starting at login is optional:
// `tk daemon install --no-login` installs it without RunAtLoad.

const serviceLabel = "dev.tk.tkd"

func servicePlistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", serviceLabel+".plist")
}

func launchDomain() string { return "gui/" + strconv.Itoa(os.Getuid()) }

func requireMacOS() error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("installing tkd as a service works on macOS and Linux; on %s run `tk daemon` directly (or from your own startup task)", runtime.GOOS)
	}
	return nil
}

// cmdDaemonService handles `tk daemon install|uninstall|start|stop`.
func cmdDaemonService(sub string, flags map[string]bool) error {
	if runtime.GOOS == "linux" {
		return linuxDaemonService(sub, flags)
	}
	if err := requireMacOS(); err != nil {
		return err
	}
	switch sub {
	case "install":
		return installService(!flags["no-login"])
	case "uninstall":
		return uninstallService()
	case "start":
		return launchctl("kickstart", launchDomain()+"/"+serviceLabel)
	case "stop":
		return launchctl("kill", "SIGTERM", launchDomain()+"/"+serviceLabel)
	default:
		return fmt.Errorf("usage: tk daemon [install [--no-login] | uninstall | start | stop]")
	}
}

func linuxDaemonService(sub string, flags map[string]bool) error {
	switch sub {
	case "install":
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		exe, _ = filepath.EvalSymlinks(exe)
		env, err := serviceEnv()
		if err != nil {
			return err
		}
		return installSystemd(exe, env, !flags["no-login"])
	case "uninstall":
		return uninstallSystemd()
	case "start":
		return systemctl("start", systemdUnit)
	case "stop":
		return systemctl("stop", systemdUnit)
	}
	return fmt.Errorf("usage: tk daemon [install [--no-login] | uninstall | start | stop]")
}

// serviceEnv is the environment the background service needs.
func serviceEnv() (map[string]string, error) {
	env := map[string]string{"TK_HOME": tkHome()}
	if configuredMode() == "paperclip" {
		tokenFile, err := stableTokenFile()
		if err != nil {
			return nil, err
		}
		env["TK_TOKEN_FILE"] = tokenFile
		env["TK_API_URL"] = paperclipURL()
		if p := bridgePlugin(); p != "" {
			env["TK_BRIDGE_PLUGIN"] = p
		}
	}
	return env, nil
}

func installService(atLogin bool) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, _ = filepath.EvalSymlinks(exe)
	logDir := filepath.Join(tkHome(), "logs")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return err
	}
	env, err := serviceEnv()
	if err != nil {
		return err
	}
	plist := renderPlist(exe, env, filepath.Join(logDir, "tkd.log"), atLogin)

	path := servicePlistPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	_ = launchctl("bootout", launchDomain()+"/"+serviceLabel) // replace an existing install; ignore "not loaded"
	if err := os.WriteFile(path, []byte(plist), 0o644); err != nil {
		return err
	}
	if err := launchctl("bootstrap", launchDomain(), path); err != nil {
		return err
	}
	if err := launchctl("kickstart", launchDomain()+"/"+serviceLabel); err != nil {
		return err
	}
	when := "starts at login"
	if !atLogin {
		when = "does not start at login (start it with: tk daemon start)"
	}
	fmt.Printf("tkd installed as %s; running now; %s\nlog: %s\n", serviceLabel, when, filepath.Join(logDir, "tkd.log"))
	return nil
}

func uninstallService() error {
	_ = launchctl("bootout", launchDomain()+"/"+serviceLabel)
	if err := os.Remove(servicePlistPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	fmt.Println("tkd service removed")
	return nil
}

// stableTokenFile copies the agent key to ~/.tk/agent_token.json (owner-only) so the
// service doesn't depend on a temporary path or a shell environment variable.
func stableTokenFile() (string, error) {
	token, err := loadToken()
	if err != nil {
		return "", err
	}
	dest := filepath.Join(tkHome(), "agent_token.json")
	if err := os.MkdirAll(tkHome(), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(dest, []byte(token+"\n"), 0o600); err != nil {
		return "", err
	}
	return dest, nil
}

func renderPlist(exe string, env map[string]string, logFile string, atLogin bool) string {
	esc := func(s string) string {
		var b strings.Builder
		_ = xml.EscapeText(&b, []byte(s))
		return b.String()
	}
	var envXML strings.Builder
	for k, v := range env {
		fmt.Fprintf(&envXML, "\t\t<key>%s</key>\n\t\t<string>%s</string>\n", esc(k), esc(v))
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>daemon</string>
	</array>
	<key>EnvironmentVariables</key>
	<dict>
%s	</dict>
	<key>RunAtLoad</key>
	<%t/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
	<key>ProcessType</key>
	<string>Background</string>
	<key>StandardOutPath</key>
	<string>%s</string>
	<key>StandardErrorPath</key>
	<string>%s</string>
</dict>
</plist>
`, serviceLabel, esc(exe), envXML.String(), atLogin, esc(logFile), esc(logFile))
}

func launchctl(args ...string) error {
	out, err := exec.Command("launchctl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// serviceInfo describes the installed service for tk status.
func serviceInfo() string {
	if runtime.GOOS == "linux" {
		return systemdInfo()
	}
	raw, err := os.ReadFile(servicePlistPath())
	if err != nil {
		return "not installed (tk daemon install)"
	}
	if strings.Contains(string(raw), "<key>RunAtLoad</key>\n\t<true/>") {
		return "installed, starts at login"
	}
	return "installed, does not start at login"
}
