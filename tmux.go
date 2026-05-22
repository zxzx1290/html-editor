//go:build linux || darwin

package main

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
)

const tmuxSocketName = "html-editor"

const (
	maxTermCols = 1000
	maxTermRows = 1000
)

var sessionNameRe = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

type tmuxManager struct {
	mu       sync.Mutex
	enabled  bool
	binary   string
	attaches map[string]*tmuxAttach // session name → live attach (max 1)
}

type tmuxAttach struct {
	name      string
	owner     string
	binary    string
	pty       *os.File
	cmd       *exec.Cmd
	hub       *Hub
	closeOnce sync.Once
}

func newTmuxManager() *tmuxManager {
	m := &tmuxManager{attaches: make(map[string]*tmuxAttach)}
	bin, err := exec.LookPath("tmux")
	if err != nil {
		logf("[tmux] disabled (tmux binary not found in PATH)\n")
		return m
	}
	m.binary = bin
	m.enabled = true
	logf("[tmux] enabled socket=%s binary=%s os=%s\n", tmuxSocketName, bin, runtime.GOOS)
	return m
}

func (m *tmuxManager) cmd(args ...string) *exec.Cmd {
	full := append([]string{"-L", tmuxSocketName}, args...)
	return exec.Command(m.binary, full...)
}

func (m *tmuxManager) listSessions(user string) ([]string, error) {
	if !m.enabled {
		return nil, errors.New("tmux disabled")
	}
	c := m.cmd("list-sessions", "-F", "#S")
	out, err := c.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			stderr := string(ee.Stderr)
			if strings.Contains(stderr, "no server running") || strings.Contains(stderr, "error connecting") {
				return nil, nil
			}
		}
		return nil, err
	}
	prefix := user + "-"
	var result []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, prefix) {
			result = append(result, line)
		}
	}
	return result, nil
}

func (m *tmuxManager) sessionExists(name string) bool {
	if !m.enabled {
		return false
	}
	c := m.cmd("has-session", "-t", name)
	return c.Run() == nil
}

func (m *tmuxManager) ownsSession(user, name string) bool {
	if !sessionNameRe.MatchString(name) {
		return false
	}
	return strings.HasPrefix(name, user+"-")
}

func clampSize(cols, rows uint16) (uint16, uint16) {
	if cols == 0 {
		cols = 80
	}
	if rows == 0 {
		rows = 24
	}
	if cols > maxTermCols {
		cols = maxTermCols
	}
	if rows > maxTermRows {
		rows = maxTermRows
	}
	return cols, rows
}

// createSession runs `tmux new-session -d -s NAME` so the session persists
// even when no client is attached.
func (m *tmuxManager) createSession(user string, cols, rows uint16) (string, error) {
	if !m.enabled {
		return "", errors.New("tmux disabled")
	}
	if !isValidUsernamePart(user) {
		return "", errors.New("invalid username for tmux")
	}
	cols, rows = clampSize(cols, rows)

	// tmux session name
	name := user + "-" + strings.ToLower(rand.Text())[:6]

	// 一條 tmux invocation 內串：new-session ; set-option（關狀態列）; 這樣 session 就不會閃一下狀態列然後消失了。
	// -d(detached) 後給 attach 用，-x -y 指定初始大小避免 attach 後閃一下 resize。
	c := m.cmd("new-session", "-d", "-s", name,
		"-x", fmt.Sprintf("%d", cols), "-y", fmt.Sprintf("%d", rows),
		"/bin/bash",
		";", "set-option", "-t", name, "status", "off")
	if out, err := c.CombinedOutput(); err != nil {
		return "", fmt.Errorf("tmux new-session: %v: %s", err, string(out))
	}
	logf("[tmux] created user=%s session=%s\n", user, name)
	return name, nil
}

// kill terminates the session.
func (m *tmuxManager) kill(name string) error {
	if !m.enabled {
		return errors.New("tmux disabled")
	}
	if !sessionNameRe.MatchString(name) {
		return errors.New("invalid session name")
	}
	c := m.cmd("kill-session", "-t", name)
	if out, err := c.CombinedOutput(); err != nil {
		return fmt.Errorf("tmux kill-session: %v: %s", err, string(out))
	}
	logf("[tmux] killed session=%s\n", name)
	return nil
}

// attach spawns a fresh `tmux attach` process inside a PTY, returns the
// attach so the caller can detach later.  If a previous attach to the same
// session exists it is detached first.
func (m *tmuxManager) attach(client *WsClient, name string, cols, rows uint16) (*tmuxAttach, error) {
	if !m.enabled {
		return nil, errors.New("tmux disabled")
	}
	if !sessionNameRe.MatchString(name) {
		return nil, errors.New("invalid session name")
	}
	if !m.ownsSession(client.username, name) {
		return nil, errors.New("forbidden session")
	}
	if !m.sessionExists(name) {
		return nil, errors.New("session not found")
	}
	cols, rows = clampSize(cols, rows)

	m.mu.Lock()
	if old, ok := m.attaches[name]; ok {
		m.mu.Unlock()
		old.close()
		m.mu.Lock()
	}

	// -d (detach others), -E (don't run update-environment), -x exit on detach
	cmd := exec.Command(m.binary, "-L", tmuxSocketName, "attach", "-d", "-t", name)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: cols, Rows: rows})
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	a := &tmuxAttach{
		name:   name,
		owner:  client.username,
		binary: m.binary,
		pty:    ptmx,
		cmd:    cmd,
		hub:    client.hub,
	}
	m.attaches[name] = a
	m.mu.Unlock()

	go m.pump(a)
	logf("[tmux] attached user=%s session=%s pid=%d\n", client.username, name, cmd.Process.Pid) // pid 是 attach 進程的，不是 tmux session 的
	return a, nil
}

func (m *tmuxManager) pump(a *tmuxAttach) {
	buf := make([]byte, 4096)
	for {
		n, err := a.pty.Read(buf)
		if n > 0 {
			data := base64.StdEncoding.EncodeToString(buf[:n])
			a.hub.sendTo(a.owner, wsOutMsg{
				Type:    "term_output",
				Payload: map[string]string{"name": a.name, "data": data},
			})
		}
		if err != nil {
			// EOF 與 fs.ErrClosed 都是 PTY 正常關閉（exit / detach / kill），不必 log
			if err != io.EOF && !errors.Is(err, os.ErrClosed) {
				logf("[tmux] pump_read_err session=%s err=%v\n", a.name, err)
			}
			break
		}
	}
	// PTY closed; tmux attach process exited.  Notify the client.
	m.mu.Lock()
	if m.attaches[a.name] == a {
		delete(m.attaches, a.name)
	}
	m.mu.Unlock()
	a.closeOnce.Do(func() {
		_ = a.pty.Close()
	})
	// If the underlying session is gone (e.g. user typed `exit`), tell the client.
	if !m.sessionExists(a.name) {
		a.hub.sendTo(a.owner, wsOutMsg{
			Type:    "term_closed",
			Payload: map[string]string{"name": a.name},
		})
		logf("[tmux] pump_exit session=%s reason=session_gone\n", a.name)
	}
}

func (a *tmuxAttach) close() {
	a.closeOnce.Do(func() {
		if a.binary != "" {
			_ = exec.Command(a.binary, "detach-client", "-t", a.name).Run()
		}
		_ = a.pty.Close()
		if a.cmd != nil && a.cmd.Process != nil {
			go func(c *exec.Cmd) {
				_, _ = c.Process.Wait()
			}(a.cmd)
		}
	})
}

func (m *tmuxManager) detach(name string) {
	m.mu.Lock()
	a, ok := m.attaches[name]
	if ok {
		delete(m.attaches, name)
	}
	m.mu.Unlock()
	if ok {
		a.close()
	}
	logf("[tmux] detached session=%s\n", name)
}

func (m *tmuxManager) detachAllForUser(user string) {
	m.mu.Lock()
	var victims []*tmuxAttach
	for name, a := range m.attaches {
		if a.owner == user {
			victims = append(victims, a)
			delete(m.attaches, name)
		}
	}
	m.mu.Unlock()
	for _, v := range victims {
		v.close()
	}
	logf("[tmux] detached all sessions for user=%s count=%d\n", user, len(victims))
}

func (m *tmuxManager) resize(name string, cols, rows uint16) error {
	cols, rows = clampSize(cols, rows)
	m.mu.Lock()
	a, ok := m.attaches[name]
	m.mu.Unlock()
	if !ok {
		return errors.New("not attached")
	}
	return pty.Setsize(a.pty, &pty.Winsize{Cols: cols, Rows: rows})
}

func (m *tmuxManager) write(name string, data []byte) error {
	m.mu.Lock()
	a, ok := m.attaches[name]
	m.mu.Unlock()
	if !ok {
		return errors.New("not attached")
	}
	_, err := a.pty.Write(data)
	return err
}

func (m *tmuxManager) shutdown() {
	m.mu.Lock()
	names := make([]string, 0, len(m.attaches))
	for name := range m.attaches {
		names = append(names, name)
	}
	m.mu.Unlock()
	for _, name := range names {
		m.detach(name)
		_ = m.kill(name)
	}
	// Give pump goroutines a moment to drain.
	time.Sleep(50 * time.Millisecond)
	logf("[tmux] shutdown complete\n")
}

func isValidUsernamePart(u string) bool {
	if u == "" {
		return false
	}
	for _, r := range u {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '-':
		default:
			return false
		}
	}
	return true
}
