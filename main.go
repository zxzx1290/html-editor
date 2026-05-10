package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pquerna/otp/totp"
)

// ─── Context keys ─────────────────────────────────────────────────────────────

type ctxKey string

const (
	ctxWorkspace ctxKey = "workspace"
	ctxUsername  ctxKey = "username"
	ctxToken     ctxKey = "token"
)

// ─── Config ───────────────────────────────────────────────────────────────────

type UserConfig struct {
	TotpSecret string `json:"totpSecret"`
	Workspace  string `json:"workspace"`
}

type Config struct {
	Host          string                `json:"host"`          // overrides -host flag if flag not set
	Port          int                   `json:"port"`          // overrides -port flag if flag not set
	SessionTTL    int64                 `json:"sessionTTL"`    // seconds; 0 → 86400
	MaxUploadSize int64                 `json:"maxUploadSize"` // bytes; 0 → 50MB
	Users         map[string]UserConfig `json:"users"`
}

// ─── Session ──────────────────────────────────────────────────────────────────

type Session struct {
	Username string
	Expires  time.Time
}

// ─── Rate limiter (in-memory, resets on restart) ──────────────────────────────

type rateLimiter struct {
	mu       sync.Mutex
	attempts map[string][]time.Time
}

func newRateLimiter() *rateLimiter {
	return &rateLimiter{attempts: make(map[string][]time.Time)}
}

func (rl *rateLimiter) isBlocked(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	var recent []time.Time
	for _, t := range rl.attempts[ip] {
		if now.Sub(t) < 5*time.Minute {
			recent = append(recent, t)
		}
	}
	rl.attempts[ip] = recent
	return len(recent) >= 5
}

func (rl *rateLimiter) record(ip string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.attempts[ip] = append(rl.attempts[ip], time.Now())
}

// ─── WebSocket types ──────────────────────────────────────────────────────────

type wsInMsg struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type wsOutMsg struct {
	Type    string `json:"type"`
	Payload any    `json:"payload"`
}

type WsClient struct {
	username  string
	conn      *websocket.Conn
	send      chan []byte
	closeOnce sync.Once
	hub       *Hub
}

// ─── Hub ──────────────────────────────────────────────────────────────────────

type Hub struct {
	mu        sync.RWMutex
	clients   map[string]*WsClient // username → client (one per user)
	openFiles map[string][]string  // fileKey → []username
}

func newHub() *Hub {
	return &Hub{
		clients:   make(map[string]*WsClient),
		openFiles: make(map[string][]string),
	}
}

func fileKey(path, file string) string {
	if path == "" {
		return file
	}
	return path + "/" + file
}

func (h *Hub) register(c *WsClient) bool {
	if c.username == "" {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, exists := h.clients[c.username]; exists {
		return false
	}
	h.clients[c.username] = c
	return true
}

func (h *Hub) unregister(c *WsClient) {
	h.mu.Lock()
	if c.username != "" && h.clients[c.username] == c {
		delete(h.clients, c.username)
		for key, users := range h.openFiles {
			filtered := users[:0]
			for _, u := range users {
				if u != c.username {
					filtered = append(filtered, u)
				}
			}
			if len(filtered) == 0 {
				delete(h.openFiles, key)
			} else {
				h.openFiles[key] = filtered
			}
		}
	}
	h.mu.Unlock()
	c.closeOnce.Do(func() { close(c.send) })
}

// fileOpen records opener and returns list of other users who already have file open.
func (h *Hub) fileOpen(opener, path, file string) []string {
	key := fileKey(path, file)
	h.mu.Lock()
	defer h.mu.Unlock()
	var others []string
	alreadyOpen := false
	for _, u := range h.openFiles[key] {
		if u == opener {
			alreadyOpen = true
		} else {
			others = append(others, u)
		}
	}
	if !alreadyOpen {
		h.openFiles[key] = append(h.openFiles[key], opener)
	}
	return others
}

func (h *Hub) fileClose(username, path, file string) {
	key := fileKey(path, file)
	h.mu.Lock()
	defer h.mu.Unlock()
	users := h.openFiles[key]
	filtered := users[:0]
	for _, u := range users {
		if u != username {
			filtered = append(filtered, u)
		}
	}
	if len(filtered) == 0 {
		delete(h.openFiles, key)
	} else {
		h.openFiles[key] = filtered
	}
}

func (h *Hub) clientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

func (h *Hub) sendTo(username string, msg any) {
	h.mu.RLock()
	c, ok := h.clients[username]
	h.mu.RUnlock()
	if !ok {
		return
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	safelySend(c.send, data)
}

func safelySend(ch chan []byte, data []byte) {
	defer func() { recover() }()
	select {
	case ch <- data:
	default:
	}
}

// ─── Server ───────────────────────────────────────────────────────────────────

func (s *server) maxUploadSize() int64 {
	const defaultMax = 50 * 1024 * 1024
	if s.config != nil && s.config.MaxUploadSize > 0 {
		return s.config.MaxUploadSize
	}
	return defaultMax
}

type fileEntry struct {
	Path  string `json:"path"`
	Name  string `json:"name"`
	IsDir bool   `json:"isDir"`
	Size  int64  `json:"size"`
}

type server struct {
	workspace string
	config    *Config
	sessions  sync.Map
	hub       *Hub
	limiter   *rateLimiter
}

// ─── main ─────────────────────────────────────────────────────────────────────

func main() {
	var (
		port       = flag.Int("port", 8080, "listen port")
		host       = flag.String("host", "127.0.0.1", "listen host")
		workspace  = flag.String("workspace", "./workspace", "workspace directory (used when no --config)")
		configFile = flag.String("config", "", "path to config.json (default: auto-load ./config.json if exists)")
	)
	flag.Parse()
	if *configFile == "" {
		if _, err := os.Stat("config.json"); err == nil {
			*configFile = "config.json"
		}
	}

	abs, err := filepath.Abs(*workspace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid workspace path: %v\n", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "failed to create workspace: %v\n", err)
		os.Exit(1)
	}

	s := &server{
		workspace: abs,
		hub:       newHub(),
		limiter:   newRateLimiter(),
	}

	if *configFile != "" {
		data, err := os.ReadFile(*configFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to read config: %v\n", err)
			os.Exit(1)
		}
		var cfg Config
		if err := json.Unmarshal(data, &cfg); err != nil {
			fmt.Fprintf(os.Stderr, "invalid config JSON: %v\n", err)
			os.Exit(1)
		}
		s.config = &cfg
		logf("[config] %d user(s) loaded\n", len(cfg.Users))

		// Apply host/port from config only when the CLI flag was not explicitly set.
		set := make(map[string]bool)
		flag.Visit(func(f *flag.Flag) { set[f.Name] = true })
		if !set["host"] && cfg.Host != "" {
			*host = cfg.Host
		}
		if !set["port"] && cfg.Port != 0 {
			*port = cfg.Port
		}
	}

	// Background: clean up expired sessions hourly + log server status
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for range t.C {
			now := time.Now()
			var sessionCount int
			s.sessions.Range(func(k, v any) bool {
				if now.After(v.(Session).Expires) {
					s.sessions.Delete(k)
				} else {
					sessionCount++
				}
				return true
			})
			logf("[status] sessions=%d ws_clients=%d\n", sessionCount, s.hub.clientCount())
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/login",        s.handleLogin)
	mux.HandleFunc("/logout",       s.handleLogout)
	mux.HandleFunc("/check",        s.checkSession(s.handleCheck))
	mux.HandleFunc("/extend",       s.checkSession(s.handleExtend))
	mux.HandleFunc("/ws",           s.checkSession(s.handleWs))
	mux.HandleFunc("/api/config",   s.handleApiConfig)
	mux.HandleFunc("/api/files",    s.sessionAndWorkspace(s.handleListFiles))
	mux.HandleFunc("/api/file",     s.sessionAndWorkspace(s.handleFile))
	mux.HandleFunc("/api/upload",   s.sessionAndWorkspace(s.handleUpload))
	mux.HandleFunc("/api/download", s.sessionAndWorkspace(s.handleDownload))
	mux.HandleFunc("/api/mkdir",    s.sessionAndWorkspace(s.handleMkdir))
	mux.HandleFunc("/api/rename",   s.sessionAndWorkspace(s.handleRename))
	mux.Handle("/static/",          http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))
	mux.HandleFunc("/favicon.ico",  func(w http.ResponseWriter, r *http.Request) { http.ServeFile(w, r, filepath.Join("static", "favicon.ico")) })
	mux.HandleFunc("/",             s.checkSession(s.handleIndex))

	addr := fmt.Sprintf("%s:%d", *host, *port)
	if *configFile != "" {
		logf("html-editor listening on http://%s  config=%s\n", addr, *configFile)
	} else {
		logf("html-editor listening on http://%s  workspace=%s\n", addr, abs)
	}
	if err := http.ListenAndServe(addr, mux); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// ─── Middleware ───────────────────────────────────────────────────────────────

// checkSession validates the session cookie and injects ctxUsername + ctxToken.
// Browser routes redirect to /login on failure; API / session routes return 401.
// Passes through without check when auth is not configured.
func (s *server) checkSession(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.config == nil || len(s.config.Users) == 0 {
			next(w, r)
			return
		}
		ip := clientIP(r)
		if s.limiter.isBlocked(ip) {
			logf("[rate-limit] ip=%s blocked\n", ip)
			p := r.URL.Path
			if strings.HasPrefix(p, "/api/") || p == "/check" || p == "/extend" {
				http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
			} else {
				http.Redirect(w, r, "/login?error=blocked", http.StatusFound)
			}
			return
		}
		cookie, err := r.Cookie("editorHash")
		if err != nil {
			s.redirectOrUnauth(w, r)
			return
		}
		username, ok := s.validateSession(cookie.Value)
		if !ok {
			s.limiter.record(ip)
			logf("[session] invalid token ip=%s\n", ip)
			s.redirectOrUnauth(w, r)
			return
		}
		ctx := context.WithValue(r.Context(), ctxUsername, username)
		ctx = context.WithValue(ctx, ctxToken, cookie.Value)
		next(w, r.WithContext(ctx))
	}
}

// redirectOrUnauth sends 401 for API / session endpoints; redirects browser routes.
func (s *server) redirectOrUnauth(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	if strings.HasPrefix(p, "/api/") || p == "/check" || p == "/extend" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
	} else {
		http.Redirect(w, r, "/login", http.StatusFound)
	}
}

// withWorkspaceH resolves the user's workspace directory from config and injects
// ctxWorkspace. Must be called after checkSession (needs ctxUsername in context).
func (s *server) withWorkspaceH(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ws := s.workspace
		if s.config != nil && len(s.config.Users) > 0 {
			username, _ := r.Context().Value(ctxUsername).(string)
			if username == "" {
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}
			userCfg, ok := s.config.Users[username]
			if !ok {
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}
			abs, err := filepath.Abs(userCfg.Workspace)
			if err != nil {
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}
			ws = abs
		}
		ctx := context.WithValue(r.Context(), ctxWorkspace, ws)
		next(w, r.WithContext(ctx))
	}
}

// sessionAndWorkspace = checkSession + withWorkspaceH, for file API routes.
func (s *server) sessionAndWorkspace(next http.HandlerFunc) http.HandlerFunc {
	return s.checkSession(s.withWorkspaceH(next))
}

func workspaceFromCtx(r *http.Request) string {
	ws, _ := r.Context().Value(ctxWorkspace).(string)
	return ws
}

func tokenFromCtx(r *http.Request) string {
	t, _ := r.Context().Value(ctxToken).(string)
	return t
}

// ─── Session helpers ──────────────────────────────────────────────────────────

func (s *server) sessionTTL() time.Duration {
	if s.config != nil && s.config.SessionTTL > 0 {
		return time.Duration(s.config.SessionTTL) * time.Second
	}
	return 24 * time.Hour
}

func (s *server) newSession(username string) string {
	token := strings.ToLower(rand.Text())
	s.sessions.Store(token, Session{Username: username, Expires: time.Now().Add(s.sessionTTL())})
	return token
}

func validToken(token string) bool {
	const tokenLen = 26
	if len(token) != tokenLen {
		return false
	}
	for _, c := range token {
		if !((c >= 'a' && c <= 'z') || (c >= '2' && c <= '7')) {
			return false
		}
	}
	return true
}

func (s *server) validateSession(token string) (string, bool) {
	if !validToken(token) {
		return "", false
	}
	v, ok := s.sessions.Load(token)
	if !ok {
		return "", false
	}
	sess := v.(Session)
	if time.Now().After(sess.Expires) {
		s.sessions.Delete(token)
		return "", false
	}
	return sess.Username, true
}

func (s *server) extendSession(token string) bool {
	v, ok := s.sessions.Load(token)
	if !ok {
		return false
	}
	sess := v.(Session)
	if time.Now().After(sess.Expires) {
		return false
	}
	sess.Expires = time.Now().Add(s.sessionTTL())
	s.sessions.Store(token, sess)
	return true
}

// ─── Auth handlers ────────────────────────────────────────────────────────────

func (s *server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if s.config == nil || len(s.config.Users) == 0 {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	switch r.Method {
	case http.MethodGet:
		if c, err := r.Cookie("editorHash"); err == nil {
			if _, ok := s.validateSession(c.Value); ok {
				http.Redirect(w, r, "/", http.StatusFound)
				return
			}
		}
		http.ServeFile(w, r, filepath.Join("static", "login.html"))
	case http.MethodPost:
		s.processLogin(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func clientIP(r *http.Request) string {
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		return strings.TrimSpace(strings.Split(forwarded, ",")[0])
	}
	ip := r.RemoteAddr
	if i := strings.LastIndex(ip, ":"); i >= 0 {
		ip = ip[:i]
	}
	return ip
}

func (s *server) processLogin(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if s.limiter.isBlocked(ip) {
		logf("[rate-limit] ip=%s blocked\n", ip)
		http.Redirect(w, r, "/login?error=blocked", http.StatusFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/login?error=1", http.StatusFound)
		return
	}
	username := r.FormValue("username")
	code := r.FormValue("code")
	userCfg, ok := s.config.Users[username]
	if !ok || !totp.Validate(code, userCfg.TotpSecret) {
		s.limiter.record(ip)
		logf("[session] invalid credentials ip=%s\n", ip)
		http.Redirect(w, r, "/login?error=1", http.StatusFound)
		return
	}
	token := s.newSession(username)
	secure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
	http.SetCookie(w, &http.Cookie{
		Name: "editorUser", Value: username,
		Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: secure,
	})
	http.SetCookie(w, &http.Cookie{
		Name: "editorHash", Value: token,
		Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: secure,
	})
	logf("[login] user=%s ip=%s\n", username, ip)
	http.Redirect(w, r, "/", http.StatusFound)
}

func (s *server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("editorHash"); err == nil {
		s.sessions.Delete(c.Value)
	}
	username := ""
	if c, err := r.Cookie("editorUser"); err == nil {
		username = c.Value
	}
	reason := r.URL.Query().Get("reason")
	if reason == "" {
		reason = "manual"
	}
	logf("[logout] user=%s ip=%s reason=%s\n", username, clientIP(r), reason)
	http.SetCookie(w, &http.Cookie{Name: "editorUser", Value: "", MaxAge: -1, Path: "/"})
	http.SetCookie(w, &http.Cookie{Name: "editorHash", Value: "", MaxAge: -1, Path: "/"})
	http.Redirect(w, r, "/login", http.StatusFound)
}

func (s *server) handleCheck(w http.ResponseWriter, r *http.Request) {
	if s.config == nil {
		http.NotFound(w, r)
		return
	}
	v, ok := s.sessions.Load(tokenFromCtx(r))
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	ttl := int64(time.Until(v.(Session).Expires).Seconds())
	writeJSON(w, http.StatusOK, map[string]any{"data": ttl})
}

func (s *server) handleExtend(w http.ResponseWriter, r *http.Request) {
	if s.config == nil {
		http.NotFound(w, r)
		return
	}
	if !s.extendSession(tokenFromCtx(r)) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *server) handleApiConfig(w http.ResponseWriter, r *http.Request) {
	sessionCheck := s.config != nil && len(s.config.Users) > 0
	writeJSON(w, http.StatusOK, map[string]any{"sessionCheck": sessionCheck})
}

// ─── WebSocket ────────────────────────────────────────────────────────────────

var wsUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func (s *server) handleWs(w http.ResponseWriter, r *http.Request) {
	username, _ := r.Context().Value(ctxUsername).(string)
	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	client := &WsClient{
		username: username,
		conn:     conn,
		send:     make(chan []byte, 64),
		hub:      s.hub,
	}
	if !s.hub.register(client) {
		s.hub.sendTo(username, wsOutMsg{Type: "error", Payload: "duplicate connect"})
		data, _ := json.Marshal(wsOutMsg{Type: "error", Payload: "duplicate connect"})
		conn.WriteMessage(websocket.TextMessage, data)
		conn.Close()
		return
	}
	logf("[ws] connect user=%s\n", username)
	go client.writePump()
	client.readPump(s)
}

const (
	wsPingInterval = 30 * time.Second
	wsReadDeadline = 70 * time.Second
)

func (c *WsClient) readPump(s *server) {
	defer c.conn.Close()
	defer s.hub.unregister(c)
	defer logf("[ws] disconnect user=%s\n", c.username)

	c.conn.SetReadDeadline(time.Now().Add(wsReadDeadline))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(wsReadDeadline))
		return nil
	})

	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			break
		}
		var msg wsInMsg
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}

		switch msg.Type {
		case "file_on_open":
			var p struct {
				Path string `json:"path"`
				File string `json:"file"`
			}
			if err := json.Unmarshal(msg.Payload, &p); err != nil {
				continue
			}
			others := s.hub.fileOpen(c.username, p.Path, p.File)
			for _, u := range others {
				logf("[same_file_open] %s/%s opener=%s existing=%s\n", p.Path, p.File, c.username, u)
				s.hub.sendTo(c.username, wsOutMsg{Type: "same_file_open", Payload: map[string]string{
					"user": u, "path": p.Path, "file": p.File,
				}})
				s.hub.sendTo(u, wsOutMsg{Type: "same_file_open", Payload: map[string]string{
					"user": c.username, "path": p.Path, "file": p.File,
				}})
			}

		case "file_on_close":
			var p struct {
				Path string `json:"path"`
				File string `json:"file"`
			}
			if err := json.Unmarshal(msg.Payload, &p); err != nil {
				continue
			}
			s.hub.fileClose(c.username, p.Path, p.File)

		case "after_save":
			var p struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal(msg.Payload, &p); err != nil {
				continue
			}
			logf("[ws] after_save user=%s path=%s\n", c.username, p.Path)
		}
	}
}

func (c *WsClient) writePump() {
	ticker := time.NewTicker(wsPingInterval)
	defer ticker.Stop()
	defer c.conn.Close()
	for {
		select {
		case data, ok := <-c.send:
			if !ok {
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, data); err != nil {
				return
			}
		case <-ticker.C:
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}


// ─── File handlers ────────────────────────────────────────────────────────────

func (s *server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, filepath.Join("static", "index.html"))
}

func (s *server) handleListFiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	rel := r.URL.Query().Get("path")
	dir, err := resolvePath(workspaceFromCtx(r), rel)
	if err != nil {
		writeError(w, http.StatusForbidden, "invalid path")
		return
	}
	des, err := os.ReadDir(dir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var entries []fileEntry
	for _, de := range des {
		fullPath := filepath.Join(dir, de.Name())
		info, err := os.Stat(fullPath)
		if err != nil {
			continue
		}
		entRel, err := filepath.Rel(workspaceFromCtx(r), fullPath)
		if err != nil {
			continue
		}
		entries = append(entries, fileEntry{
			Path:  filepath.ToSlash(entRel),
			Name:  de.Name(),
			IsDir: info.IsDir(),
			Size:  info.Size(),
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
	writeJSON(w, http.StatusOK, map[string]any{"files": entries})
}

func (s *server) handleFile(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.readFile(w, r)
	case http.MethodPut:
		s.writeFile(w, r)
	case http.MethodDelete:
		s.deleteFile(w, r)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *server) readFile(w http.ResponseWriter, r *http.Request) {
	rel := r.URL.Query().Get("path")
	if rel == "" {
		writeError(w, http.StatusBadRequest, "missing path")
		return
	}
	abs, err := resolvePath(workspaceFromCtx(r), rel)
	if err != nil {
		writeError(w, http.StatusForbidden, "invalid path")
		return
	}
	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if info.IsDir() {
		writeError(w, http.StatusBadRequest, "is a directory")
		return
	}
	logf("[file-open] %s\n", rel)
	http.ServeFile(w, r, abs)
}

func (s *server) writeFile(w http.ResponseWriter, r *http.Request) {
	rel := r.URL.Query().Get("path")
	if rel == "" {
		writeError(w, http.StatusBadRequest, "missing path")
		return
	}
	abs, err := resolvePath(workspaceFromCtx(r), rel)
	if err != nil {
		writeError(w, http.StatusForbidden, "invalid path")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.maxUploadSize())
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := os.WriteFile(abs, body, 0o644); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	logf("[file-save] %s\n", rel)
	writeOK(w)
}

func (s *server) deleteFile(w http.ResponseWriter, r *http.Request) {
	ws := workspaceFromCtx(r)
	rel := r.URL.Query().Get("path")
	if rel == "" {
		writeError(w, http.StatusBadRequest, "missing path")
		return
	}
	abs, err := resolvePath(ws, rel)
	if err != nil || abs == ws {
		writeError(w, http.StatusForbidden, "invalid path")
		return
	}
	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if info.IsDir() {
		if err := os.RemoveAll(abs); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	} else {
		if err := os.Remove(abs); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	logf("[file-del] %s\n", rel)
	writeOK(w)
}

func (s *server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ws := workspaceFromCtx(r)
	r.Body = http.MaxBytesReader(w, r.Body, s.maxUploadSize())
	if err := r.ParseMultipartForm(s.maxUploadSize()); err != nil {
		writeError(w, http.StatusBadRequest, "upload too large or malformed")
		return
	}
	dir := r.FormValue("path")
	dirAbs, err := resolvePath(ws, dir)
	if err != nil {
		writeError(w, http.StatusForbidden, "invalid path")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "missing file")
		return
	}
	defer file.Close()
	if header.Size > s.maxUploadSize() {
		writeError(w, http.StatusRequestEntityTooLarge, "file too large")
		return
	}
	if err := os.MkdirAll(dirAbs, 0o755); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	dstPath := filepath.Join(dirAbs, filepath.Base(header.Filename))
	dstResolved, err := filepath.Abs(dstPath)
	if err != nil || (dstResolved != ws && !strings.HasPrefix(dstResolved, ws+string(os.PathSeparator))) {
		writeError(w, http.StatusForbidden, "invalid path")
		return
	}
	dst, err := os.Create(dstResolved)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer dst.Close()
	if _, err := io.Copy(dst, file); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	rel, _ := filepath.Rel(ws, dstResolved)
	logf("[file-upload] %s\n", filepath.ToSlash(rel))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path": filepath.ToSlash(rel)})
}

func (s *server) handleDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	rel := r.URL.Query().Get("path")
	if rel == "" {
		writeError(w, http.StatusBadRequest, "missing path")
		return
	}
	abs, err := resolvePath(workspaceFromCtx(r), rel)
	if err != nil {
		writeError(w, http.StatusForbidden, "invalid path")
		return
	}
	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if info.IsDir() {
		writeError(w, http.StatusBadRequest, "is a directory")
		return
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(abs)))
	http.ServeFile(w, r, abs)
}

func (s *server) handleRename(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ws := workspaceFromCtx(r)
	from := r.URL.Query().Get("from")
	to := r.URL.Query().Get("to")
	if from == "" || to == "" {
		writeError(w, http.StatusBadRequest, "missing from or to")
		return
	}
	absFrom, err := resolvePath(ws, from)
	if err != nil || absFrom == ws {
		writeError(w, http.StatusForbidden, "invalid from path")
		return
	}
	absTo, err := resolvePath(ws, to)
	if err != nil || absTo == ws {
		writeError(w, http.StatusForbidden, "invalid to path")
		return
	}
	if _, err := os.Stat(absTo); err == nil {
		writeError(w, http.StatusConflict, "destination already exists")
		return
	}
	if err := os.Rename(absFrom, absTo); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	logf("[file-rename] %s -> %s\n", from, to)
	writeOK(w)
}

func (s *server) handleMkdir(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	rel := r.URL.Query().Get("path")
	if rel == "" {
		writeError(w, http.StatusBadRequest, "missing path")
		return
	}
	abs, err := resolvePath(workspaceFromCtx(r), rel)
	if err != nil {
		writeError(w, http.StatusForbidden, "invalid path")
		return
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	logf("[file-mkdir] %s\n", rel)
	writeOK(w)
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func logf(format string, args ...any) {
	fmt.Printf(time.Now().Format("2006/01/02 15:04:05")+" "+format, args...)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func writeOK(w http.ResponseWriter) {
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func resolvePath(workspace, rel string) (string, error) {
	if rel == "" {
		return workspace, nil
	}
	cleaned := filepath.Clean("/" + rel)
	cleaned = strings.TrimPrefix(cleaned, "/")
	abs := filepath.Join(workspace, cleaned)
	absResolved, err := filepath.Abs(abs)
	if err != nil {
		return "", err
	}
	if absResolved != workspace && !strings.HasPrefix(absResolved, workspace+string(os.PathSeparator)) {
		return "", errors.New("path escapes workspace")
	}
	return absResolved, nil
}
