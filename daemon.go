package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// tkd: `tk daemon` runs in the background on the developer's machine. It
//   - refreshes the local copy of remote data (no-op in local mode);
//   - syncs the open session's Claude Code usage to the backend in batches;
//   - hands back a session whose agent has gone quiet for too long;
//   - syncs cost once more after a session ends, to catch the last reply.

type daemonConfig struct {
	refresh    time.Duration // cache refresh
	costEvery  time.Duration // cost sync for the open session
	idleAfter  time.Duration // hand back a session with no agent activity for this long
	finalDelay time.Duration // wait after a session ends before the final cost sync
}

func loadDaemonConfig() daemonConfig {
	return daemonConfig{
		refresh:    envDuration("TKD_REFRESH", 15*time.Second),
		costEvery:  envDuration("TKD_COST_INTERVAL", 30*time.Second),
		idleAfter:  envDuration("TKD_IDLE_TIMEOUT", 30*time.Minute),
		finalDelay: envDuration("TKD_FINAL_SYNC_DELAY", 60*time.Second),
	}
}

func envDuration(name string, fallback time.Duration) time.Duration {
	if d, err := time.ParseDuration(os.Getenv(name)); err == nil && d > 0 {
		return d
	}
	return fallback
}

type daemonStatus struct {
	PID       int    `json:"pid"`
	StartedAt string `json:"startedAt"`
	LastTick  string `json:"lastTick"`
	Backend   string `json:"backend"`
	Server    string `json:"server"` // "ok" or the last error
}

func daemonStatusPath() string { return filepath.Join(tkHome(), "daemon.json") }

func cmdDaemon(be backend) error {
	cfg := loadDaemonConfig()
	logger := log.New(os.Stdout, "tkd ", log.LstdFlags)
	logger.Printf("started pid %d | backend %s | refresh every %s | cost every %s | idle hand-back after %s",
		os.Getpid(), be.name(), cfg.refresh, cfg.costEvery, cfg.idleAfter)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	status := daemonStatus{PID: os.Getpid(), StartedAt: nowRFC3339(), Backend: be.name()}
	lastCost := time.Time{}
	ticker := time.NewTicker(cfg.refresh)
	defer ticker.Stop()

	for {
		status.Server = "ok"
		if err := be.refresh(); err != nil {
			status.Server = err.Error()
			logger.Printf("refresh: %v", err)
		}
		if time.Since(lastCost) >= cfg.costEvery {
			lastCost = time.Now()
			all, _ := loadSessions()
			for i := range all {
				tickSession(be, &all[i], cfg, logger)
			}
		}
		flushOutbox(be, logger)
		finishClosedSessions(be, cfg, logger)

		status.LastTick = nowRFC3339()
		if raw, err := json.Marshal(status); err == nil {
			_ = os.WriteFile(daemonStatusPath(), raw, 0o600)
		}
		select {
		case <-ctx.Done():
			logger.Printf("stopping")
			_ = os.Remove(daemonStatusPath())
			return nil
		case <-ticker.C:
		}
	}
}

// tickSession syncs an open session's cost and hands it back if its agent has gone quiet.
func tickSession(be backend, s *session, cfg daemonConfig, logger *log.Logger) {
	line, err := syncCost(be, s, time.Now().Add(time.Minute))
	if err != nil {
		logger.Printf("%s cost: %v", s.Key, err)
		return
	}
	if !strings.Contains(line, "(0 new)") && !strings.HasPrefix(line, "cost: no ") {
		logger.Printf("%s %s", s.Key, line)
	}
	last, err := lastActivity(s)
	if err != nil {
		logger.Printf("%s activity: %v", s.Key, err)
		return
	}
	if idle := time.Since(last); idle >= cfg.idleAfter {
		note := fmt.Sprintf("tkd: no agent activity for %s; handing %s back so it can be picked up again.", idle.Round(time.Minute), s.Key)
		err := withSessionLock(func() error {
			// The developer may have finished this session since it was loaded;
			// hand it back only if it is still open.
			if !sessionOpen(s.SessionID) {
				return nil
			}
			if _, err := be.finish(s, "handoff", note); err != nil {
				return err
			}
			if err := closeSession(*s); err != nil {
				return err
			}
			logger.Printf("%s handed back after %s with no agent activity", s.Key, idle.Round(time.Second))
			return nil
		})
		if err != nil {
			logger.Printf("%s hand-back after %s idle failed: %v", s.Key, idle.Round(time.Second), err)
		}
	}
}

// lastActivity is the time of the latest logged reply in the session, or its start.
func lastActivity(s *session) (time.Time, error) {
	start, err := time.Parse(time.RFC3339, s.StartedAt)
	if err != nil {
		return time.Time{}, err
	}
	msgs, _, err := sessionUsage(s, start, time.Now().Add(time.Minute))
	if err != nil {
		return time.Time{}, err
	}
	if len(msgs) == 0 {
		return start, nil
	}
	return msgs[len(msgs)-1].At, nil
}

// finishClosedSessions does the final cost sync for sessions ended a while ago.
func finishClosedSessions(be backend, cfg daemonConfig, logger *log.Logger) {
	files, _ := filepath.Glob(filepath.Join(tkHome(), "closed", "*.json"))
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var s session
		if json.Unmarshal(raw, &s) != nil || s.SessionID == "" {
			_ = os.Remove(f) // unreadable or from before backends
			continue
		}
		ended, err := time.Parse(time.RFC3339, s.EndedAt)
		if err != nil || time.Since(ended) < cfg.finalDelay {
			continue
		}
		// Replies are timestamped when they start, so the one that finished the
		// session is earlier than EndedAt even though it reached the log later. A
		// wider window would count the developer's next, unrelated replies.
		line, err := syncCost(be, &s, ended.Add(2*time.Second))
		if err != nil {
			logger.Printf("%s final cost sync: %v", s.Key, err)
			continue
		}
		logger.Printf("%s final %s", s.Key, line)
		_ = os.Remove(f)
	}
}

// cmdStatus reports the mode, the daemon, the open session and pending work.
func cmdStatus() error {
	fmt.Println("mode: " + configuredMode())
	if c, err := readOAuth(); err == nil {
		fmt.Printf("Linear app: %s | workspace: %s | human: %s\n", c.AgentName, c.WorkspaceID, c.HumanID)
	} else if !os.IsNotExist(err) {
		fmt.Println("Linear OAuth: credentials need repair")
	}
	var st daemonStatus
	if raw, err := os.ReadFile(daemonStatusPath()); err == nil && json.Unmarshal(raw, &st) == nil {
		age := "?"
		if t, err := time.Parse(time.RFC3339, st.LastTick); err == nil {
			age = time.Since(t).Round(time.Second).String() + " ago"
		}
		fmt.Printf("tkd: running (pid %d), backend %s, last tick %s, status %s\n", st.PID, st.Backend, age, st.Server)
	} else {
		fmt.Println("tkd: not running (start with: tk daemon, or tk daemon start if installed)")
	}
	fmt.Println("service: " + serviceInfo())
	all, err := loadSessions()
	if err != nil {
		return err
	}
	if len(all) == 0 {
		fmt.Println("sessions: none")
	}
	for _, s := range all {
		agent := ""
		if s.ClaudeSessionID != "" {
			agent = " | " + toolLabel(s.AgentTool)
		}
		fmt.Printf("session: %s | since %s | in %s%s\n", s.Key, localTime(s.StartedAt), s.Cwd, agent)
	}
	closed, _ := filepath.Glob(filepath.Join(tkHome(), "closed", "*.json"))
	fmt.Printf("awaiting final cost sync: %d\n", len(closed))
	if n := len(pendingOutbox()); n > 0 {
		fmt.Printf("queued writes waiting for the tracker: %d (tkd sends them)\n", n)
	}
	if failed, _ := filepath.Glob(filepath.Join(outboxDir(), "failed", "*.json")); len(failed) > 0 {
		fmt.Printf("queued writes the tracker rejected: %d (see %s)\n", len(failed), filepath.Join(outboxDir(), "failed"))
	}
	return nil
}
