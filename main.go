package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
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
	Host                 string                `json:"host"`
	Port                 int                   `json:"port"`
	SessionTTL           int64                 `json:"sessionTTL"`           // seconds; 0 → 86400
	MaxUploadSize        int64                 `json:"maxUploadSize"`        // bytes; 0 → 50MB
	Title                string                `json:"title"`                // app display name; default "HTML Editor"
	RateLimitWindow      int64                 `json:"rateLimitWindow"`      // seconds; 0 → 300
	RateLimitMaxAttempts int                   `json:"rateLimitMaxAttempts"` // 0 → 5
	RateLimitBanDuration int64                 `json:"rateLimitBanDuration"` // seconds; 0 → same as window
	JwtSecret            string                `json:"jwtSecret"`            // JWT signing secret; random if empty
	Users                map[string]UserConfig `json:"users"`
}

// ─── JWT claims ───────────────────────────────────────────────────────────────

type jwtClaims struct {
	Username string `json:"sub"`
	jwt.RegisteredClaims
}

// ─── Rate limiter (in-memory, resets on restart) ──────────────────────────────

type rateLimiter struct {
	mu          sync.Mutex
	attempts    map[string][]time.Time
	bans        map[string]time.Time
	window      time.Duration
	maxAttempts int
	banDuration time.Duration
}

func newRateLimiter(window time.Duration, maxAttempts int, banDuration time.Duration) *rateLimiter {
	return &rateLimiter{
		attempts:    make(map[string][]time.Time),
		bans:        make(map[string]time.Time),
		window:      window,
		maxAttempts: maxAttempts,
		banDuration: banDuration,
	}
}

func (rl *rateLimiter) isBlocked(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	if expiry, ok := rl.bans[ip]; ok {
		if now.Before(expiry) {
			return true
		}
		delete(rl.bans, ip)
		delete(rl.attempts, ip)
	}
	var recent []time.Time
	for _, t := range rl.attempts[ip] {
		if now.Sub(t) < rl.window {
			recent = append(recent, t)
		}
	}
	rl.attempts[ip] = recent
	return len(recent) >= rl.maxAttempts
}

func (rl *rateLimiter) record(ip string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	rl.attempts[ip] = append(rl.attempts[ip], now)
	var recent int
	for _, t := range rl.attempts[ip] {
		if now.Sub(t) < rl.window {
			recent++
		}
	}
	if recent >= rl.maxAttempts {
		rl.bans[ip] = now.Add(rl.banDuration)
	}
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
	wasRegistered := false
	var closedKeys []string
	h.mu.Lock()
	if c.username != "" && h.clients[c.username] == c {
		wasRegistered = true
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
				logf("[ws] unregister_clear_file user=%s key=%s\n", c.username, key)
			} else {
				h.openFiles[key] = filtered
			}
			closedKeys = append(closedKeys, key)
		}
	}
	h.mu.Unlock()
	c.closeOnce.Do(func() { close(c.send) })
	if wasRegistered {
		for _, key := range closedKeys {
			// key format: "path/file" or just "file"
			path, file := "", key
			if i := strings.LastIndex(key, "/"); i >= 0 {
				path, file = key[:i], key[i+1:]
			}
			h.broadcast(wsOutMsg{Type: "file_closed", Payload: map[string]string{
				"user": c.username, "path": path, "file": file,
			}}, c.username)
		}
		h.broadcast(wsOutMsg{Type: "user_offline", Payload: map[string]string{"user": c.username}}, c.username)
	}
}

func (h *Hub) broadcast(msg any, excludeUsername string) {
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for username, c := range h.clients {
		if username != excludeUsername {
			safelySend(c.send, data)
		}
	}
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
	if s.config.MaxUploadSize > 0 {
		return s.config.MaxUploadSize
	}
	return defaultMax
}

func (s *server) appTitle() string {
	if s.config.Title != "" {
		return s.config.Title
	}
	return "HTML Editor"
}

func (s *server) serveWithTitle(w http.ResponseWriter, r *http.Request, file string) {
	data, err := os.ReadFile(file)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	out := strings.ReplaceAll(string(data), "{{APP_TITLE}}", s.appTitle())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(out))
}

type fileEntry struct {
	Path  string `json:"path"`
	Name  string `json:"name"`
	IsDir bool   `json:"isDir"`
	Size  int64  `json:"size"`
}

type server struct {
	config    *Config
	jwtSecret []byte
	hub       *Hub
	limiter   *rateLimiter
}

// ─── main ─────────────────────────────────────────────────────────────────────

func main() {
	data, err := os.ReadFile("config.json")
	if err != nil {
		fmt.Fprintln(os.Stderr, "config.json not found:", err)
		os.Exit(1)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		fmt.Fprintln(os.Stderr, "invalid config.json:", err)
		os.Exit(1)
	}
	if len(cfg.Users) == 0 {
		fmt.Fprintln(os.Stderr, "config.json must define at least one user")
		os.Exit(1)
	}

	for username, userCfg := range cfg.Users {
		abs, err := filepath.Abs(userCfg.Workspace)
		if err != nil {
			fmt.Fprintf(os.Stderr, "invalid workspace for user %s: %v\n", username, err)
			os.Exit(1)
		}
		if err := os.MkdirAll(abs, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "failed to create workspace for user %s: %v\n", username, err)
			os.Exit(1)
		}
	}

	host := "127.0.0.1"
	port := 8080
	if cfg.Host != "" {
		host = cfg.Host
	}
	if cfg.Port != 0 {
		port = cfg.Port
	}

	rlWindow := time.Duration(cfg.RateLimitWindow) * time.Second
	if rlWindow <= 0 {
		rlWindow = 5 * time.Minute
	}
	rlMax := cfg.RateLimitMaxAttempts
	if rlMax <= 0 {
		rlMax = 5
	}
	rlBan := time.Duration(cfg.RateLimitBanDuration) * time.Second
	if rlBan <= 0 {
		rlBan = rlWindow
	}

	if cfg.JwtSecret == "" {
		fmt.Fprintln(os.Stderr, "config.json must set jwtSecret")
		os.Exit(1)
	}
	secret := []byte(cfg.JwtSecret)

	s := &server{
		config:    &cfg,
		jwtSecret: secret,
		hub:       newHub(),
		limiter:   newRateLimiter(rlWindow, rlMax, rlBan),
	}
	logf("[config] %d user(s) loaded\n", len(cfg.Users))

	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for range t.C {
			logf("[status] ws_clients=%d\n", s.hub.clientCount())
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/login",        s.handleLogin)
	mux.HandleFunc("/logout",       s.handleLogout)
	mux.HandleFunc("/check",        s.checkSession(s.handleCheck))
	mux.HandleFunc("/ws",           s.checkSession(s.handleWs))
	mux.HandleFunc("/api/config",   s.handleApiConfig)
	mux.HandleFunc("/api/files",    s.sessionAndWorkspace(s.handleListFiles))
	mux.HandleFunc("/api/file",     s.sessionAndWorkspace(s.handleFile))
	mux.HandleFunc("/api/upload",   s.sessionAndWorkspace(s.handleUpload))
	mux.HandleFunc("/api/download", s.sessionAndWorkspace(s.handleDownload))
	mux.HandleFunc("/api/mkdir",    s.sessionAndWorkspace(s.handleMkdir))
	mux.HandleFunc("/api/rename",   s.sessionAndWorkspace(s.handleRename))
	mux.HandleFunc("/api/copy",     s.sessionAndWorkspace(s.handleCopy))
	mux.Handle("/static/",          http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))
	mux.HandleFunc("/favicon.ico",  func(w http.ResponseWriter, r *http.Request) { http.ServeFile(w, r, filepath.Join("static", "favicon.ico")) })
	mux.HandleFunc("/",             s.checkSession(s.handleIndex))

	addr := fmt.Sprintf("%s:%d", host, port)
	logf("html-editor listening on http://%s\n", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// ─── Middleware ───────────────────────────────────────────────────────────────

// checkSession validates the session cookie and injects ctxUsername + ctxToken.
// Browser routes redirect to /login on failure; API / session routes return 401.
func (s *server) checkSession(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r)
		if s.limiter.isBlocked(ip) {
			logf("[rate-limit] ip=%s blocked\n", ip)
			p := r.URL.Path
			if strings.HasPrefix(p, "/api/") || p == "/check" {
				http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
			} else {
				http.Redirect(w, r, "/login?reason=blocked", http.StatusFound)
			}
			return
		}
		cookie, err := r.Cookie("editorToken")
		if err != nil {
			s.redirectOrUnauth(w, r, "")
			return
		}
		username, ok := s.validateSession(cookie.Value)
		if !ok {
			s.limiter.record(ip)
			logf("[session] invalid token ip=%s\n", ip)
			s.redirectOrUnauth(w, r, "expired")
			return
		}
		ctx := context.WithValue(r.Context(), ctxUsername, username)
		ctx = context.WithValue(ctx, ctxToken, cookie.Value)
		next(w, r.WithContext(ctx))
	}
}

// redirectOrUnauth sends 401 for API / session endpoints; redirects browser routes.
// reason is appended as ?reason=<reason> when non-empty.
func (s *server) redirectOrUnauth(w http.ResponseWriter, r *http.Request, reason string) {
	p := r.URL.Path
	if strings.HasPrefix(p, "/api/") || p == "/check" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
	} else {
		target := "/login"
		if reason != "" {
			target += "?reason=" + reason
		}
		http.Redirect(w, r, target, http.StatusFound)
	}
}

// withWorkspaceH resolves the user's workspace directory from config and injects
// ctxWorkspace. Must be called after checkSession (needs ctxUsername in context).
func (s *server) withWorkspaceH(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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
		ctx := context.WithValue(r.Context(), ctxWorkspace, abs)
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

func usernameFromCtx(r *http.Request) string {
	u, _ := r.Context().Value(ctxUsername).(string)
	return u
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
	claims := jwtClaims{
		Username: username,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(s.sessionTTL())),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, _ := token.SignedString(s.jwtSecret)
	return signed
}

func (s *server) validateSession(tokenStr string) (string, bool) {
	var claims jwtClaims
	token, err := jwt.ParseWithClaims(tokenStr, &claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return s.jwtSecret, nil
	})
	if err != nil || !token.Valid {
		return "", false
	}
	if _, ok := s.config.Users[claims.Username]; !ok {
		return "", false
	}
	return claims.Username, true
}

// extendSession validates the token and returns a new JWT with a fresh expiry.
func (s *server) extendSession(tokenStr string) (string, bool) {
	username, ok := s.validateSession(tokenStr)
	if !ok {
		return "", false
	}
	fmt.Printf("[session] extending session for user=%s\n", username)
	return s.newSession(username), true
}

// ─── Auth handlers ────────────────────────────────────────────────────────────

func (s *server) handleLogin(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if c, err := r.Cookie("editorToken"); err == nil {
			if _, ok := s.validateSession(c.Value); ok {
				http.Redirect(w, r, "/", http.StatusFound)
				return
			}
		}
		s.serveWithTitle(w, r, filepath.Join("static", "login.html"))
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
		http.Redirect(w, r, "/login?reason=blocked", http.StatusFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/login?reason=invalid", http.StatusFound)
		return
	}
	username := r.FormValue("username")
	code := r.FormValue("code")
	userCfg, ok := s.config.Users[username]
	if !ok || !totp.Validate(code, userCfg.TotpSecret) {
		s.limiter.record(ip)
		logf("[session] invalid credentials ip=%s\n", ip)
		http.Redirect(w, r, "/login?reason=invalid", http.StatusFound)
		return
	}
	token := s.newSession(username)
	secure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
	http.SetCookie(w, &http.Cookie{
		Name: "editorToken", Value: token, MaxAge: int(s.sessionTTL().Seconds()),
		Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: secure,
	})
	logf("[login] user=%s ip=%s\n", username, ip)
	http.Redirect(w, r, "/", http.StatusFound)
}

func (s *server) handleLogout(w http.ResponseWriter, r *http.Request) {
	username := ""
	if c, err := r.Cookie("editorToken"); err == nil {
		if u, ok := s.validateSession(c.Value); ok {
			username = u
		}
	}
	reason := r.URL.Query().Get("reason")
	switch reason {
	case "duplicate_connect", "kick":
	default:
		reason = "manual"
	}
	logf("[logout] user=%s ip=%s reason=%s\n", username, clientIP(r), reason)
	http.SetCookie(w, &http.Cookie{Name: "editorToken", Value: "", MaxAge: -1, Path: "/"})
	loginURL := "/login"
	if reason != "manual" {
		loginURL = "/login?reason=" + reason
	}
	http.Redirect(w, r, loginURL, http.StatusFound)
}

func (s *server) handleCheck(w http.ResponseWriter, r *http.Request) {
	var claims jwtClaims
	token, err := jwt.ParseWithClaims(tokenFromCtx(r), &claims, func(t *jwt.Token) (any, error) {
		return s.jwtSecret, nil
	})
	if err != nil || !token.Valid || claims.ExpiresAt == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	ttl := int64(time.Until(claims.ExpiresAt.Time).Seconds())
	extended := false
	if ttl < int64(s.sessionTTL()/2/time.Second) {
		if newToken, ok := s.extendSession(tokenFromCtx(r)); ok {
			secure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
			http.SetCookie(w, &http.Cookie{
				Name: "editorToken", Value: newToken, MaxAge: int(s.sessionTTL().Seconds()),
				Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: secure,
			})
			ttl = int64(s.sessionTTL() / time.Second)
			extended = true
			logf("[session] extended user=%s\n", claims.Username)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": ttl, "extended": extended})
}

func (s *server) handleApiConfig(w http.ResponseWriter, r *http.Request) {
	resp := map[string]any{"sessionCheck": true}
	if c, err := r.Cookie("editorToken"); err == nil {
		if username, ok := s.validateSession(c.Value); ok {
			resp["username"] = username
		}
	}
	writeJSON(w, http.StatusOK, resp)
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
	s.hub.broadcast(wsOutMsg{Type: "user_online", Payload: map[string]string{"user": username}}, username)
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
			if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseNoStatusReceived) {
				logf("[ws] read_error user=%s err=%v\n", c.username, err)
			}
			break
		}
		var msg wsInMsg
		if err := json.Unmarshal(data, &msg); err != nil {
			logf("[ws] bad_json user=%s err=%v\n", c.username, err)
			continue
		}

		switch msg.Type {
		case "file_on_open":
			var p struct {
				Path string `json:"path"`
				File string `json:"file"`
			}
			if err := json.Unmarshal(msg.Payload, &p); err != nil {
				logf("[ws] file_on_open bad_payload user=%s err=%v\n", c.username, err)
				continue
			}
			logf("[ws] file_on_open user=%s path=%s file=%s\n", c.username, p.Path, p.File)
			others := s.hub.fileOpen(c.username, p.Path, p.File)
			for _, u := range others {
				logf("[ws] same_file_open path=%s file=%s opener=%s existing=%s\n", p.Path, p.File, c.username, u)
				s.hub.sendTo(c.username, wsOutMsg{Type: "same_file_open", Payload: map[string]string{
					"user": u, "path": p.Path, "file": p.File,
				}})
				s.hub.sendTo(u, wsOutMsg{Type: "same_file_open", Payload: map[string]string{
					"user": c.username, "path": p.Path, "file": p.File,
				}})
			}
			s.hub.broadcast(wsOutMsg{Type: "file_opened", Payload: map[string]string{
				"user": c.username, "path": p.Path, "file": p.File,
			}}, c.username)

		case "file_on_close":
			var p struct {
				Path string `json:"path"`
				File string `json:"file"`
			}
			if err := json.Unmarshal(msg.Payload, &p); err != nil {
				logf("[ws] file_on_close bad_payload user=%s err=%v\n", c.username, err)
				continue
			}
			logf("[ws] file_on_close user=%s path=%s file=%s\n", c.username, p.Path, p.File)
			s.hub.fileClose(c.username, p.Path, p.File)
			s.hub.broadcast(wsOutMsg{Type: "file_closed", Payload: map[string]string{
				"user": c.username, "path": p.Path, "file": p.File,
			}}, c.username)

		case "after_save":
			var p struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal(msg.Payload, &p); err != nil {
				logf("[ws] after_save bad_payload user=%s err=%v\n", c.username, err)
				continue
			}
			logf("[ws] after_save user=%s path=%s\n", c.username, p.Path)

		default:
			logf("[ws] unknown_type user=%s type=%s\n", c.username, msg.Type)
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
	s.serveWithTitle(w, r, filepath.Join("static", "index.html"))
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
	logf("[file-open] user=%s %s\n", usernameFromCtx(r), rel)
	data, err := os.ReadFile(abs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
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
	logf("[file-save] user=%s %s\n", usernameFromCtx(r), rel)
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
	logf("[file-del] user=%s %s\n", usernameFromCtx(r), rel)
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
	logf("[file-upload] user=%s %s\n", usernameFromCtx(r), filepath.ToSlash(rel))
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
	if r.URL.Query().Get("auto") == "1" {
		absTo = availableDest(absTo)
	} else if _, err := os.Stat(absTo); err == nil {
		writeError(w, http.StatusConflict, "destination already exists")
		return
	}
	if err := os.Rename(absFrom, absTo); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	relTo, _ := filepath.Rel(ws, absTo)
	relTo = filepath.ToSlash(relTo)
	logf("[file-rename] user=%s %s -> %s\n", usernameFromCtx(r), from, relTo)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "to": relTo})
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
	logf("[file-mkdir] user=%s %s\n", usernameFromCtx(r), rel)
	writeOK(w)
}

func (s *server) handleCopy(w http.ResponseWriter, r *http.Request) {
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
	srcClean := filepath.Clean(absFrom)
	dstClean := filepath.Clean(absTo)
	if dstClean == srcClean || strings.HasPrefix(dstClean+string(filepath.Separator), srcClean+string(filepath.Separator)) {
		// dst is inside src — redirect to a sibling of src with auto-incremented name.
		// availableDest(srcClean) is safe: srcClean is already within ws, so its
		// sibling (.1, .2 …) stays within ws and can never escape the workspace.
		absTo = availableDest(srcClean)
	} else {
		absTo = availableDest(absTo)
	}
	if err := copyPath(absFrom, absTo); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	relTo, _ := filepath.Rel(ws, absTo)
	relTo = filepath.ToSlash(relTo)
	logf("[file-copy] user=%s %s -> %s\n", usernameFromCtx(r), from, relTo)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path": relTo})
}

func copyPath(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return copyDir(src, dst)
	}
	return copyFile(src, dst)
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func copyDir(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		s := filepath.Join(src, entry.Name())
		d := filepath.Join(dst, entry.Name())
		if entry.IsDir() {
			if err := copyDir(s, d); err != nil {
				return err
			}
		} else {
			if err := copyFile(s, d); err != nil {
				return err
			}
		}
	}
	return nil
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

// availableDest returns abs if it doesn't exist, otherwise inserts an incrementing
// counter before the extension: note.txt → note.1.txt → note.2.txt …
func availableDest(abs string) string {
	if _, err := os.Stat(abs); os.IsNotExist(err) {
		return abs
	}
	ext := filepath.Ext(abs)
	base := strings.TrimSuffix(abs, ext)
	for i := 1; ; i++ {
		candidate := fmt.Sprintf("%s.%d%s", base, i, ext)
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
}

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
