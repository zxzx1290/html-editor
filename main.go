package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"syscall"
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

// dummyTotpSecret is used to keep response timing constant when the supplied
// username does not exist, preventing user enumeration via timing analysis.
// It is a valid base32 string so totp.Validate runs the full HMAC path.
const dummyTotpSecret = "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP"

// ─── Config ───────────────────────────────────────────────────────────────────

type UserConfig struct {
	TotpSecret string `json:"totpSecret"`
	Workspace  string `json:"workspace"`
	Terminal   bool   `json:"terminal"`
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
	TrustProxy           bool                  `json:"trustProxy"`           // trust X-Forwarded-* headers; only enable when behind a trusted reverse proxy
	Users                map[string]UserConfig `json:"users"`
	LoginNotify          *LoginNotifyConfig    `json:"loginNotify"` // optional outbound HTTP notification on login success/failure
}

// LoginNotifyConfig describes an outbound HTTP request fired on login events.
// Empty URL disables notifications entirely.
//
// Template variables usable in Form values: {username} {ip} {event} {reason} {time}
//   - {event}:  "success" | "failure"
//   - {reason}: "" | "invalid" | "replay" | "blocked"
//
// Body selection:
//   - Form set            → application/x-www-form-urlencoded (e.g. Mailgun)
//   - Form empty + POST    → JSON body {event,username,ip,reason,time}
//   - Form empty + GET     → same fields appended as query string
type LoginNotifyConfig struct {
	URL            string            `json:"url"`
	Method         string            `json:"method"`         // "POST" (default) | "GET"
	NotifySuccess  bool              `json:"notifySuccess"`  // send on successful login
	NotifyFailure  bool              `json:"notifyFailure"`  // send on failed login
	TimeoutSeconds int               `json:"timeoutSeconds"` // request timeout; 0 → 5
	BasicAuth      string            `json:"basicAuth"`      // "user:pass" → Authorization: Basic
	Headers        map[string]string `json:"headers"`        // extra request headers
	Form           map[string]string `json:"form"`           // urlencoded form fields (template-expanded)
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

// record logs a failed attempt and returns true only when this attempt is the
// one that newly triggers a ban (the transition into the blocked state), so
// callers can notify exactly once rather than on every subsequent blocked hit.
func (rl *rateLimiter) record(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	alreadyBanned := false
	if expiry, ok := rl.bans[ip]; ok && now.Before(expiry) {
		alreadyBanned = true
	}
	rl.attempts[ip] = append(rl.attempts[ip], now)
	var recent int
	for _, t := range rl.attempts[ip] {
		if now.Sub(t) < rl.window {
			recent++
		}
	}
	if recent >= rl.maxAttempts {
		rl.bans[ip] = now.Add(rl.banDuration)
		return !alreadyBanned
	}
	return false
}

// gc removes expired bans and stale attempts so the maps do not grow unbounded
// for IPs that never return.
func (rl *rateLimiter) gc() {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	for ip, expiry := range rl.bans {
		if now.After(expiry) {
			delete(rl.bans, ip)
		}
	}
	for ip, ts := range rl.attempts {
		var recent []time.Time
		for _, t := range ts {
			if now.Sub(t) < rl.window {
				recent = append(recent, t)
			}
		}
		if len(recent) == 0 {
			delete(rl.attempts, ip)
		} else {
			rl.attempts[ip] = recent
		}
	}
}

// ─── TOTP replay guard ───────────────────────────────────────────────────────

type totpReplay struct {
	mu     sync.Mutex
	used   map[string]time.Time
	window time.Duration
}

func newTotpReplay(window time.Duration) *totpReplay {
	return &totpReplay{
		used:   make(map[string]time.Time),
		window: window,
	}
}

// checkAndRecord returns true if (username, code) has not been used within the window.
// On success it records the pair so subsequent calls within the window will return false.
func (t *totpReplay) checkAndRecord(username, code string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	for k, exp := range t.used {
		if now.After(exp) {
			delete(t.used, k)
		}
	}
	key := username + ":" + code
	if _, exists := t.used[key]; exists {
		return false
	}
	t.used[key] = now.Add(t.window)
	return true
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
			inList := false
			filtered := users[:0]
			for _, u := range users {
				if u == c.username {
					// 標記用戶在此文件的打開列表中，稍後廣播 file_closed 消息
					inList = true
				} else {
					// 保留其他用戶
					filtered = append(filtered, u)
				}
			}
			if len(filtered) == 0 {
				delete(h.openFiles, key)
			} else {
				h.openFiles[key] = filtered
			}
			if inList {
				closedKeys = append(closedKeys, key)
				logf("[ws] unregister_clear_file user=%s file=%s\n", c.username, key)
			}
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
	Path       string `json:"path"`
	Name       string `json:"name"`
	IsDir      bool   `json:"isDir"`
	Size       int64  `json:"size"`
	IsSymlink  bool   `json:"isSymlink,omitempty"`
	LinkTarget string `json:"linkTarget,omitempty"`
}

type server struct {
	config         *Config
	jwtSecret      []byte
	hub            *Hub
	limiter        *rateLimiter
	totpReplay     *totpReplay
	tmux           *tmuxManager
	searchInFlight sync.Map // map[username]struct{}：每位使用者同時只能 1 個搜尋
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
	if len(cfg.JwtSecret) < 32 {
		fmt.Fprintln(os.Stderr, "config.json: jwtSecret must be at least 32 bytes")
		os.Exit(1)
	}
	secret := []byte(cfg.JwtSecret)

	s := &server{
		config:     &cfg,
		jwtSecret:  secret,
		hub:        newHub(),
		limiter:    newRateLimiter(rlWindow, rlMax, rlBan),
		totpReplay: newTotpReplay(90 * time.Second),
		tmux:       newTmuxManager(),
	}
	logf("[config] %d user(s) loaded\n", len(cfg.Users))

	// if notification is enabled, log it on startup
	if cfg.LoginNotify != nil && cfg.LoginNotify.URL != "" {
		logf("[config] login notifications enabled\n")
	}

	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for range t.C {
			logf("[status] ws_clients=%d\n", s.hub.clientCount())
		}
	}()

	go func() {
		t := time.NewTicker(10 * time.Minute)
		defer t.Stop()
		for range t.C {
			s.limiter.gc()
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
	mux.HandleFunc("/api/search",   s.sessionAndWorkspace(s.handleSearch))
	mux.Handle("/static/",          http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))
	mux.HandleFunc("/favicon.ico",  func(w http.ResponseWriter, r *http.Request) { http.ServeFile(w, r, filepath.Join("static", "favicon.ico")) })
	mux.HandleFunc("/",             s.checkSession(s.handleIndex))

	addr := fmt.Sprintf("%s:%d", host, port)
	logf("[server] starting on %s\n", addr)
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}()

	// 等待中斷訊號，收到後優雅關閉：先停 HTTP server，再收掉 tmux attach。
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	logf("html-editor shutting down...\n")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err == nil {
		logf("[http] shutdown complete\n")
	} else {
		fmt.Fprintln(os.Stderr, "http shutdown:", err)
	}
	s.tmux.shutdown()
}

// ─── Middleware ───────────────────────────────────────────────────────────────

// checkSession validates the session cookie and injects ctxUsername + ctxToken.
// Browser routes redirect to /login on failure; API / session routes return 401.
func (s *server) checkSession(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := s.clientIP(r)
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
	logf("[session] extending session for user=%s\n", username)
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

func (s *server) clientIP(r *http.Request) string {
	if s.config.TrustProxy {
		if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
			return strings.TrimSpace(strings.Split(forwarded, ",")[0])
		}
	}
	ip := r.RemoteAddr
	if i := strings.LastIndex(ip, ":"); i >= 0 {
		ip = ip[:i]
	}
	return ip
}

func (s *server) isSecureRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if s.config.TrustProxy && r.Header.Get("X-Forwarded-Proto") == "https" {
		return true
	}
	return false
}

// 帳號只允許 A-Za-z0-9，長度 1-16，避免惡意字元流入通知信件與 log
var validUsernameRe = regexp.MustCompile(`^[A-Za-z0-9]{1,16}$`)

func (s *server) processLogin(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if s.limiter.isBlocked(ip) {
		logf("[login] ip=%s blocked\n", ip)
		http.Redirect(w, r, "/login?reason=blocked", http.StatusFound)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := r.ParseForm(); err != nil {
		logf("[login] failed to parse form ip=%s\n", ip)
		http.Redirect(w, r, "/login?reason=invalid", http.StatusFound)
		return
	}
	username := r.FormValue("username")
	code := r.FormValue("code")
	if !validUsernameRe.MatchString(username) {
		// 格式不合的帳號不可能是合法使用者，直接擋下並計入速率限制，
		// 避免惡意字元（換行、標頭注入等）流入通知信件與 log
		s.limiter.record(ip)
		logf("[login] invalid username format ip=%s\n", ip)
		http.Redirect(w, r, "/login?reason=invalid", http.StatusFound)
		return
	}
	userCfg, ok := s.config.Users[username]
	// 不論 username 是否存在都跑一次 totp.Validate，避免從回應時間枚舉合法帳號
	secret := dummyTotpSecret
	if ok {
		secret = userCfg.TotpSecret
	}
	valid := totp.Validate(code, secret)
	if !ok || !valid {
		justBanned := s.limiter.record(ip)
		logf("[login] invalid credentials ip=%s\n", ip)
		if justBanned {
			s.notifyLogin("failure", username, ip, "blocked")
		} else {
			s.notifyLogin("failure", username, ip, "invalid")
		}
		http.Redirect(w, r, "/login?reason=invalid", http.StatusFound)
		return
	}
	if !s.totpReplay.checkAndRecord(username, code) {
		justBanned := s.limiter.record(ip)
		logf("[login] totp replay user=%s ip=%s\n", username, ip)
		if justBanned {
			s.notifyLogin("failure", username, ip, "blocked")
		} else {
			s.notifyLogin("failure", username, ip, "replay")
		}
		http.Redirect(w, r, "/login?reason=invalid", http.StatusFound)
		return
	}
	token := s.newSession(username)
	http.SetCookie(w, &http.Cookie{
		Name: "editorToken", Value: token, MaxAge: int(s.sessionTTL().Seconds()),
		Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: s.isSecureRequest(r),
	})
	logf("[login] user=%s ip=%s\n", username, ip)
	s.notifyLogin("success", username, ip, "")
	http.Redirect(w, r, "/", http.StatusFound)
}

// notifyLogin fires an outbound HTTP notification for a login event, if enabled.
// It returns immediately; the request runs in a background goroutine so a slow
// or failing endpoint never blocks or breaks the login flow.
func (s *server) notifyLogin(event, username, ip, reason string) {
	cfg := s.config.LoginNotify
	if cfg == nil || cfg.URL == "" {
		return
	}
	if event == "success" && !cfg.NotifySuccess {
		return
	}
	if event == "failure" && !cfg.NotifyFailure {
		return
	}
	go s.sendLoginNotify(cfg, event, username, ip, reason)
}

func notifyExpand(tmpl string, vars map[string]string) string {
	for k, v := range vars {
		tmpl = strings.ReplaceAll(tmpl, "{"+k+"}", v)
	}
	return tmpl
}

func (s *server) sendLoginNotify(cfg *LoginNotifyConfig, event, username, ip, reason string) {
	vars := map[string]string{
		"event":    event,
		"username": username,
		"ip":       ip,
		"reason":   reason,
		"time":     time.Now().Format(time.RFC3339),
	}
	method := strings.ToUpper(strings.TrimSpace(cfg.Method))
	if method == "" {
		method = http.MethodPost
	}
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	var (
		req *http.Request
		err error
	)
	switch {
	case len(cfg.Form) > 0:
		form := url.Values{}
		for k, v := range cfg.Form {
			form.Set(k, notifyExpand(v, vars))
		}
		req, err = http.NewRequest(method, cfg.URL, strings.NewReader(form.Encode()))
		if err == nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
	case method == http.MethodGet:
		u, perr := url.Parse(cfg.URL)
		if perr != nil {
			logf("[notify] bad url: %v\n", perr)
			return
		}
		q := u.Query()
		for k, v := range vars {
			q.Set(k, v)
		}
		u.RawQuery = q.Encode()
		req, err = http.NewRequest(method, u.String(), nil)
	default:
		body, _ := json.Marshal(vars)
		req, err = http.NewRequest(method, cfg.URL, strings.NewReader(string(body)))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
		}
	}
	if err != nil {
		logf("[notify] build request failed: %v\n", err)
		return
	}

	for k, v := range cfg.Headers {
		req.Header.Set(k, v)
	}
	if cfg.BasicAuth != "" {
		if i := strings.IndexByte(cfg.BasicAuth, ':'); i >= 0 {
			req.SetBasicAuth(cfg.BasicAuth[:i], cfg.BasicAuth[i+1:])
		}
	}

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		logf("[notify] send failed event=%s user=%s: %v\n", event, username, err)
		return
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 400 {
		logf("[notify] non-2xx event=%s user=%s status=%d\n", event, username, resp.StatusCode)
		return
	}
	logf("[notify] sent event=%s user=%s status=%d\n", event, username, resp.StatusCode)
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
	logf("[logout] user=%s ip=%s reason=%s\n", username, s.clientIP(r), reason)
	http.SetCookie(w, &http.Cookie{
		Name: "editorToken", Value: "", MaxAge: -1, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: s.isSecureRequest(r),
	})
	loginURL := "/login"
	if reason != "manual" {
		loginURL = "/login?reason=" + reason
	}
	http.Redirect(w, r, loginURL, http.StatusFound)
}

func (s *server) handleCheck(w http.ResponseWriter, r *http.Request) {
	var claims jwtClaims
	token, err := jwt.ParseWithClaims(tokenFromCtx(r), &claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method")
		}
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
			http.SetCookie(w, &http.Cookie{
				Name: "editorToken", Value: newToken, MaxAge: int(s.sessionTTL().Seconds()),
				Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: s.isSecureRequest(r),
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
			resp["terminal"] = s.config.Users[username].Terminal && s.tmux != nil && s.tmux.enabled
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// ─── WebSocket ────────────────────────────────────────────────────────────────

func (s *server) checkTermPermission(c *WsClient) bool {
	if !s.config.Users[c.username].Terminal || s.tmux == nil || !s.tmux.enabled {
		s.hub.sendTo(c.username, wsOutMsg{Type: "error", Payload: "terminal not allowed"})
		return false
	}
	return true
}

var wsUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return false
		}
		u, err := url.Parse(origin)
		if err != nil {
			return false
		}
		return strings.EqualFold(u.Host, r.Host)
	},
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
	defer s.tmux.detachAllForUser(c.username)
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

		case "term_list":
			if !s.checkTermPermission(c) {
				continue
			}
			names, err := s.tmux.listSessions(c.username)
			if err != nil {
				logf("[ws] term_list err user=%s err=%v\n", c.username, err)
				s.hub.sendTo(c.username, wsOutMsg{Type: "error", Payload: "term_list: " + err.Error()})
				continue
			}
			items := make([]map[string]string, 0, len(names))
			for _, n := range names {
				items = append(items, map[string]string{"name": n})
			}
			s.hub.sendTo(c.username, wsOutMsg{Type: "term_sessions", Payload: items})

		case "term_open":
			if !s.checkTermPermission(c) {
				continue
			}
			var p struct {
				Cols uint16 `json:"cols"`
				Rows uint16 `json:"rows"`
			}
			_ = json.Unmarshal(msg.Payload, &p)
			name, err := s.tmux.createSession(c.username, p.Cols, p.Rows)
			if err != nil {
				logf("[ws] term_open create_err user=%s err=%v\n", c.username, err)
				s.hub.sendTo(c.username, wsOutMsg{Type: "error", Payload: "term_open: " + err.Error()})
				continue
			}
			if _, err := s.tmux.attach(c, name, p.Cols, p.Rows); err != nil {
				logf("[ws] term_open attach_err user=%s name=%s err=%v\n", c.username, name, err)
				_ = s.tmux.kill(name)
				s.hub.sendTo(c.username, wsOutMsg{Type: "error", Payload: "term_open attach: " + err.Error()})
				continue
			}
			logf("[ws] term_open user=%s name=%s\n", c.username, name)
			s.hub.sendTo(c.username, wsOutMsg{Type: "term_opened", Payload: map[string]string{"name": name}})

		case "term_attach":
			if !s.checkTermPermission(c) {
				continue
			}
			var p struct {
				Name string `json:"name"`
				Cols uint16 `json:"cols"`
				Rows uint16 `json:"rows"`
			}
			if err := json.Unmarshal(msg.Payload, &p); err != nil {
				continue
			}
			if _, err := s.tmux.attach(c, p.Name, p.Cols, p.Rows); err != nil {
				logf("[ws] term_attach err user=%s name=%s err=%v\n", c.username, p.Name, err)
				s.hub.sendTo(c.username, wsOutMsg{Type: "error", Payload: "term_attach: " + err.Error()})
				continue
			}
			logf("[ws] term_attach user=%s name=%s\n", c.username, p.Name)
			s.hub.sendTo(c.username, wsOutMsg{Type: "term_opened", Payload: map[string]string{"name": p.Name}})

		case "term_input":
			if !s.checkTermPermission(c) {
				continue
			}
			var p struct {
				Name string `json:"name"`
				Data string `json:"data"`
			}
			if err := json.Unmarshal(msg.Payload, &p); err != nil {
				continue
			}
			raw, err := base64.StdEncoding.DecodeString(p.Data)
			if err != nil {
				continue
			}
			if err := s.tmux.write(p.Name, raw); err != nil {
				logf("[ws] term_input err user=%s name=%s err=%v\n", c.username, p.Name, err)
			}

		case "term_resize":
			if !s.checkTermPermission(c) {
				continue
			}
			var p struct {
				Name string `json:"name"`
				Cols uint16 `json:"cols"`
				Rows uint16 `json:"rows"`
			}
			if err := json.Unmarshal(msg.Payload, &p); err != nil {
				continue
			}
			if err := s.tmux.resize(p.Name, p.Cols, p.Rows); err != nil {
				logf("[ws] term_resize err user=%s name=%s err=%v\n", c.username, p.Name, err)
			}

		case "term_detach":
			if !s.checkTermPermission(c) {
				continue
			}
			var p struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(msg.Payload, &p); err != nil {
				continue
			}
			s.tmux.detach(p.Name)
			logf("[ws] term_detach user=%s name=%s\n", c.username, p.Name)

		case "term_kill":
			if !s.checkTermPermission(c) {
				continue
			}
			var p struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(msg.Payload, &p); err != nil {
				continue
			}
			if !strings.HasPrefix(p.Name, c.username+"-") {
				s.hub.sendTo(c.username, wsOutMsg{Type: "error", Payload: "term_kill: forbidden session"})
				continue
			}
			s.tmux.detach(p.Name)
			if err := s.tmux.kill(p.Name); err != nil {
				logf("[ws] term_kill err user=%s name=%s err=%v\n", c.username, p.Name, err)
				s.hub.sendTo(c.username, wsOutMsg{Type: "error", Payload: "term_kill: " + err.Error()})
				continue
			}
			logf("[ws] term_kill user=%s name=%s\n", c.username, p.Name)
			s.hub.sendTo(c.username, wsOutMsg{Type: "term_closed", Payload: map[string]string{"name": p.Name}})

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
		// 偵測 symlink；Windows 的 directory junction 在 Go 中會被回報為
		// ModeIrregular，所以兩種 reparse point 都用 os.Readlink 探測。
		var (
			isSymlink  bool
			linkTarget string
		)
		if de.Type()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			if t, err := os.Readlink(fullPath); err == nil {
				isSymlink = true
				linkTarget = filepath.ToSlash(t)
			}
		}
		entries = append(entries, fileEntry{
			Path:       filepath.ToSlash(entRel),
			Name:       de.Name(),
			IsDir:      info.IsDir(),
			Size:       info.Size(),
			IsSymlink:  isSymlink,
			LinkTarget: linkTarget,
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

// ─── Search ───────────────────────────────────────────────────────────────────

const (
	searchTimeout     = 30 * time.Second
	searchMaxFileSize = 5 * 1024 * 1024
	searchMaxLineLen  = 500
	searchMaxPerFile  = 200
	searchMaxTotal    = 1000
	searchScanBufSize = 1024 * 1024
)

// 可搜尋的文字檔副檔名（不含前綴 "."，小寫比對）。
var searchTextExts = []string{
	"txt", "md", "markdown",
	"html", "htm", "vue", "php", "phtml",
	"js", "mjs", "cjs", "jsx", "ts", "tsx",
	"css", "scss", "sass", "less",
	"json", "json5", "yaml", "yml", "toml",
	"xml", "ini", "conf", "cfg", "env",
	"go", "py", "rb", "java", "kt", "swift",
	"rs", "c", "h", "cpp", "hpp", "cs",
	"sh", "bash", "zsh", "ps1", "bat", "cmd",
	"sql", "log", "csv", "tsv",
	"gitignore", "gitattributes", "dockerfile", "makefile",
}

// 無副檔名但檔名本身視為文字檔的特例。
var searchTextBareNames = []string{"Dockerfile", "Makefile", "README"}

// 搜尋時要跳過、不遞迴進入的資料夾名稱（除了 "." 開頭的隱藏資料夾）。
var searchSkipDirs = []string{"node_modules", "vendor", "dist"}

func searchableFile(name string) bool {
	if slices.Contains(searchTextBareNames, name) {
		return true
	}
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))
	return ext != "" && slices.Contains(searchTextExts, ext)
}

type searchRequest struct {
	Path  string `json:"path"`
	Query string `json:"q"`
}

type searchMatch struct {
	Line int    `json:"line"`
	Text string `json:"text"`
}

// NDJSON 事件型別
type searchFileEvent struct {
	Type    string        `json:"type"` // "file"
	Path    string        `json:"path"`
	Matches []searchMatch `json:"matches"`
}

type searchDoneEvent struct {
	Type         string `json:"type"` // "done"
	FilesScanned int    `json:"files_scanned"`
	FilesMatched int    `json:"files_matched"`
	TotalMatches int    `json:"total_matches"`
	ElapsedMs    int64  `json:"elapsed_ms"`
	Truncated    bool   `json:"truncated"`
	Timeout      bool   `json:"timeout"`
}

func (s *server) handleSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	user := usernameFromCtx(r)
	if _, busy := s.searchInFlight.LoadOrStore(user, struct{}{}); busy {
		writeError(w, http.StatusTooManyRequests, "another search is in progress")
		return
	}
	defer s.searchInFlight.Delete(user)

	var req searchRequest
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	keyword := strings.TrimSpace(req.Query)
	if keyword == "" {
		writeError(w, http.StatusBadRequest, "missing query")
		return
	}
	re, err := regexp.Compile(keyword)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid regex: "+err.Error())
		return
	}

	ws := workspaceFromCtx(r)
	dir, err := resolvePath(ws, req.Path)
	if err != nil {
		writeError(w, http.StatusForbidden, "invalid path")
		return
	}
	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !info.IsDir() {
		writeError(w, http.StatusBadRequest, "not a directory")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), searchTimeout)
	defer cancel()

	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no") // 防止 nginx 等反向代理緩衝
	w.WriteHeader(http.StatusOK)

	root := filepath.ToSlash(strings.TrimPrefix(strings.TrimPrefix(dir, ws), string(os.PathSeparator)))
	enc := json.NewEncoder(w)
	emit := func(v any) bool {
		if err := enc.Encode(v); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	start := time.Now()
	totalMatches := 0
	filesScanned := 0
	filesMatched := 0
	truncated := false

	onFile := func(path string) bool {
		filesScanned++
		matches, hitCap := scanFileForKeyword(ctx, path, re, searchMaxTotal-totalMatches)
		if hitCap && len(matches) == 0 {
			truncated = true
			return true
		}
		if len(matches) > 0 {
			rel, _ := filepath.Rel(ws, path)
			if !emit(searchFileEvent{Type: "file", Path: filepath.ToSlash(rel), Matches: matches}) {
				return true
			}
			totalMatches += len(matches)
			filesMatched++
		}
		if totalMatches >= searchMaxTotal {
			truncated = true
			return true
		}
		return false
	}

	visited := make(map[string]struct{})
	walkErr := searchWalk(ctx, dir, visited, onFile)

	timedOut := ctx.Err() != nil
	if timedOut {
		truncated = true
	}
	if walkErr != nil && !errors.Is(walkErr, errSearchStop) {
		// walk 失敗：仍嘗試把 done 訊息送出去（讓 client 顯示部分結果 + 截斷標記）
		truncated = true
	}

	emit(searchDoneEvent{
		Type:         "done",
		FilesScanned: filesScanned,
		FilesMatched: filesMatched,
		TotalMatches: totalMatches,
		ElapsedMs:    time.Since(start).Milliseconds(),
		Truncated:    truncated,
		Timeout:      timedOut,
	})

	logf("[search] user=%s root=%s q=%q matches=%d files_matched=%d files_scanned=%d elapsed=%dms truncated=%v\n",
		user, root, keyword, totalMatches, filesMatched, filesScanned, time.Since(start).Milliseconds(), truncated)
}

// scanFileForKeyword 逐行掃描檔案，用 regex 比對，回傳命中的行（line+text）。
// remaining 是還能再收幾筆 match（受 searchMaxTotal 控制）；
// 若因 remaining<=0 而無法繼續，hitCap=true。
func scanFileForKeyword(ctx context.Context, path string, re *regexp.Regexp, remaining int) (matches []searchMatch, hitCap bool) {
	if remaining <= 0 {
		return nil, true
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), searchScanBufSize)

	lineNum := 0
	for scanner.Scan() {
		lineNum++
		if lineNum%256 == 0 {
			select {
			case <-ctx.Done():
				return matches, false
			default:
			}
		}
		line := scanner.Text()
		if !re.MatchString(line) {
			continue
		}
		text := line
		if len(text) > searchMaxLineLen {
			text = text[:searchMaxLineLen]
		}
		matches = append(matches, searchMatch{Line: lineNum, Text: text})
		if len(matches) >= searchMaxPerFile {
			return matches, false
		}
		if len(matches) >= remaining {
			return matches, false
		}
	}
	return matches, false
}

// errSearchStop 為 searchWalk 內部用來提前終止整個 walk 的 sentinel。
var errSearchStop = errors.New("search stop")

// searchWalk 是支援 symlink 追蹤的目錄遍歷。
// 與 filepath.WalkDir 不同之處：
//   - 會跟著資料夾 symlink 進去掃內部檔案；
//   - 會把指向一般檔案的 symlink 當成可搜尋檔案；
//   - 透過 filepath.EvalSymlinks 解出真實路徑做迴圈偵測，避免 symlink 互指造成無窮遞迴或重複掃描。
//
// onFile 對「通過 searchable + size 檢查」的檔案呼叫；回傳 true 代表要立刻停止整個 walk。
func searchWalk(ctx context.Context, dir string, visited map[string]struct{}, onFile func(path string) bool) error {
	select {
	case <-ctx.Done():
		return errSearchStop
	default:
	}

	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil
	}
	if _, seen := visited[real]; seen {
		return nil
	}
	visited[real] = struct{}{}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	for _, d := range entries {
		select {
		case <-ctx.Done():
			return errSearchStop
		default:
		}
		name := d.Name()
		full := filepath.Join(dir, name)
		typ := d.Type()

		isDir := d.IsDir()
		isRegular := typ.IsRegular()
		var size int64 = -1

		if typ&os.ModeSymlink != 0 {
			info, err := os.Stat(full)
			if err != nil {
				continue
			}
			isDir = info.IsDir()
			isRegular = info.Mode().IsRegular()
			size = info.Size()
		}

		if isDir {
			if strings.HasPrefix(name, ".") || slices.Contains(searchSkipDirs, name) {
				continue
			}
			if err := searchWalk(ctx, full, visited, onFile); err != nil {
				return err
			}
			continue
		}
		if !isRegular || !searchableFile(name) {
			continue
		}
		if size < 0 {
			info, err := d.Info()
			if err != nil {
				continue
			}
			size = info.Size()
		}
		if size > searchMaxFileSize {
			continue
		}
		if onFile(full) {
			return errSearchStop
		}
	}
	return nil
}
