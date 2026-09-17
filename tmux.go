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

// session name 固定為 createSession 產的 <user>-<6碼小寫base32>。
var sessionNameRe = regexp.MustCompile(`^[a-zA-Z0-9_-]+-[a-z0-9]{6}$`)

type tmuxManager struct {
	mu       sync.Mutex
	enabled  bool
	binary   string
	attaches map[string]*tmuxAttach // session name → live attach (max 1)
}

type tmuxAttach struct {
	name      string
	owner     string
	client    *WsClient // 建立此 attach 的連線；detach 時據此比對，避免舊連線誤殺同帳號新連線的 attach
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
		logf("[tmux] disabled reason=binary_not_found")
		return m
	}
	m.binary = bin
	m.enabled = true
	logf("[tmux] enabled socket=%s binary=%q os=%s", tmuxSocketName, bin, runtime.GOOS)
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

// requireOwn 是所有「吃 session name」的操作共用的授權關卡：驗證名稱格式合法、且該 session 確實屬於 user。
//
// name 固定是 createSession 產出的 <user>-<6碼>，所以擁有者就是去掉尾端 7 字元。
// 不能只比對 "user-" 前綴：使用者名稱本身允許 "-"，那樣 alice 會連 alice-x 的
// session 一起吃下去。
func (m *tmuxManager) requireOwn(user, name string) error {
	if !sessionNameRe.MatchString(name) {
		return errors.New("invalid session name")
	}
	if name[:len(name)-7] != user {
		return errors.New("forbidden session")
	}
	return nil
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

	// 一條 tmux invocation 內串：set-option ... ; set-environment ; new-session ; set-option(關狀態列)。
	// 這些設定都要在 new-session 生出 shell「之前」跑，shell 才會繼承到。
	// 1) default-terminal=xterm-256color：本 app 外層終端永遠是 xterm.js(忠實 xterm)，讓 tmux 內的程式看到
	//    xterm-256color 才相符；否則 monero-wallet-cli 等吃 GNU readline 的程式，會因 terminfo 與真實行終端
	//    在行尾 auto-margin 的游標算術對不上而跑版、backspace 擦錯位置。
	// 2) terminal-overrides ,*:Tc：告訴 tmux 對外的終端支援 direct/true color，收到 RGB 就原樣轉發、不降成 256。
	// 3) COLORTERM=truecolor：讓 tmux 內的程式願意輸出 24-bit 色。
	//    (用 set-environment -g 而非 new-session -e，相容舊版 tmux；TERM 不能這樣設，會被 default-terminal 蓋掉。)
	// -d(detached) 後給 attach 用，-x -y 指定初始大小避免 attach 後閃一下 resize。
	c := m.cmd(
		"set-option", "-g", "default-terminal", "xterm-256color", ";",
		"set-option", "-ga", "terminal-overrides", ",*:Tc", ";",
		"set-environment", "-g", "COLORTERM", "truecolor", ";",
		"new-session", "-d", "-s", name,
		"-x", fmt.Sprintf("%d", cols), "-y", fmt.Sprintf("%d", rows), ";",
		"set-option", "-t", name, "status", "off",
	)
	if out, err := c.CombinedOutput(); err != nil {
		return "", fmt.Errorf("tmux new-session: %v: %s", err, string(out))
	}
	logf("[tmux] created user=%s session=%s", user, name)
	return name, nil
}

// kill terminates the session.
func (m *tmuxManager) kill(user, name string) error {
	if err := m.requireOwn(user, name); err != nil {
		return err
	}
	return m.killSession(name)
}

// killSession 不做授權檢查，僅供 shutdown 這類內部呼叫使用；外部一律走 kill。
func (m *tmuxManager) killSession(name string) error {
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
	logf("[tmux] killed session=%s", name)
	return nil
}

// attach spawns a fresh `tmux attach` process inside a PTY, returns the
// attach so the caller can detach later.  If a previous attach to the same
// session exists it is detached first.
func (m *tmuxManager) attach(client *WsClient, name string, cols, rows uint16) (*tmuxAttach, error) {
	if !m.enabled {
		return nil, errors.New("tmux disabled")
	}
	if err := m.requireOwn(client.username, name); err != nil {
		return nil, err
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
		client: client,
		binary: m.binary,
		pty:    ptmx,
		cmd:    cmd,
		hub:    client.hub,
	}
	m.attaches[name] = a
	m.mu.Unlock()

	go m.pump(a)
	logf("[tmux] attached user=%s session=%s pid=%d", client.username, name, cmd.Process.Pid) // pid 是 attach 進程的，不是 tmux session 的
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
				logf("[tmux] pump_read_err session=%s err=%v", a.name, err)
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
		logf("[tmux] pump_exit session=%s reason=session_gone", a.name)
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

// detach 關掉 user 自己對 name 的 attach（tmux session 本身保留）。
// 這是清理操作、失敗不需回報 client，因此拒絕時只記 log 不回傳 error。
func (m *tmuxManager) detach(user, name string) {
	if err := m.requireOwn(user, name); err != nil {
		logf("[tmux] detach_rejected user=%s session=%s err=%v", user, name, err)
		return
	}
	m.detachSession(name)
}

// detachSession 不做授權檢查，僅供 shutdown 這類內部呼叫使用；外部一律走 detach。
func (m *tmuxManager) detachSession(name string) {
	m.mu.Lock()
	a, ok := m.attaches[name]
	if ok {
		delete(m.attaches, name)
	}
	m.mu.Unlock()
	if ok {
		a.close()
	}
	logf("[tmux] detached session=%s", name)
}

// detachForClient 只清掉「這條連線自己建立」的 attach。用連線身分（指標）而非 username
// 比對：同帳號 last-connection-wins 換手時，舊連線斷線的清理不可誤殺新連線已建立的 attach。
func (m *tmuxManager) detachForClient(c *WsClient) {
	m.mu.Lock()
	var victims []*tmuxAttach
	for name, a := range m.attaches {
		if a.client == c {
			victims = append(victims, a)
			delete(m.attaches, name)
		}
	}
	m.mu.Unlock()
	for _, v := range victims {
		v.close()
	}
	logf("[tmux] detach_all user=%s count=%d", c.username, len(victims))
}

// lookupAttach 取出 user 自己的 attach。
func (m *tmuxManager) lookupAttach(user, name string) (*tmuxAttach, error) {
	if err := m.requireOwn(user, name); err != nil {
		return nil, err
	}
	m.mu.Lock()
	a, ok := m.attaches[name]
	m.mu.Unlock()
	if !ok {
		return nil, errors.New("not attached")
	}
	return a, nil
}

func (m *tmuxManager) resize(user, name string, cols, rows uint16) error {
	cols, rows = clampSize(cols, rows)
	a, err := m.lookupAttach(user, name)
	if err != nil {
		return err
	}
	return pty.Setsize(a.pty, &pty.Winsize{Cols: cols, Rows: rows})
}

func (m *tmuxManager) write(user, name string, data []byte) error {
	a, err := m.lookupAttach(user, name)
	if err != nil {
		return err
	}
	_, err = a.pty.Write(data)
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
		m.detachSession(name)
		_ = m.killSession(name)
	}
	// Give pump goroutines a moment to drain.
	time.Sleep(50 * time.Millisecond)
	logf("[tmux] shutdown_complete")
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
