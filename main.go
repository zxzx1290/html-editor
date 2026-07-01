package main

import (
	"archive/zip"
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"mime"
	"net"
	"net/http"
	"net/smtp"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
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
	WatchPollInterval    int64                 `json:"watchPollInterval"`    // tree view 目錄輪詢間隔 seconds; 0 → 3
	MaxUploadSize        int64                 `json:"maxUploadSize"`        // bytes; 0 → 50MB
	Title                string                `json:"title"`                // app display name; default "HTML Editor"
	RateLimitWindow      int64                 `json:"rateLimitWindow"`      // seconds; 0 → 300
	RateLimitMaxAttempts int                   `json:"rateLimitMaxAttempts"` // 0 → 5
	RateLimitBanDuration int64                 `json:"rateLimitBanDuration"` // seconds; 0 → same as window
	JwtSecret            string                `json:"jwtSecret"`            // JWT signing secret; random if empty
	TrustProxy           bool                  `json:"trustProxy"`           // trust X-Forwarded-* headers; only enable when behind a trusted reverse proxy
	LogMode              string                `json:"logMode"`              // "fmt"（stdout，預設）或 "syslog"（本機 syslog）
	LogTag               string                `json:"logTag"`               // syslog tag；留空則預設 "html-editor"
	Users                map[string]UserConfig `json:"users"`
	LoginNotify          *LoginNotifyConfig    `json:"loginNotify"` // optional SMTP email notification on login success/failure
}

// LoginNotifyConfig describes an SMTP email sent on login events.
// Empty Host disables notifications entirely.
//
// Template variables usable in Subject / Body: {username} {ip} {event} {reason} {time}
//   - {event}:  "success" | "failure"
//   - {reason}: "" | "invalid" | "replay" | "blocked"
type LoginNotifyConfig struct {
	Host           string `json:"host"`           // SMTP server host; empty disables notifications
	Port           int    `json:"port"`           // SMTP port; 0 → 587
	Username       string `json:"username"`       // SMTP auth username; empty → no auth
	Password       string `json:"password"`       // SMTP auth password
	TLS            string `json:"tls"`            // "starttls" (default) | "tls" (implicit, e.g. port 465) | "none"
	From           string `json:"from"`           // sender address (template-expanded)
	To             string `json:"to"`             // recipient address(es), comma/space separated
	Subject        string `json:"subject"`        // subject template
	Body           string `json:"body"`           // plain-text body template
	NotifySuccess  bool   `json:"notifySuccess"`  // send on successful login
	NotifyFailure  bool   `json:"notifyFailure"`  // send on failed login
	TimeoutSeconds int    `json:"timeoutSeconds"` // connection/IO timeout; 0 → 5
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

	// watchDirs 是前端展開中的目錄（相對 workspace 路徑 → 上次見到的 mtime）。
	// 前端每次展開/收合都全量上報覆蓋，poll goroutine 據此比對 mtime，變動就推
	// dir_changed。斷線時整個 client 被 unregister 丟棄，這份狀態隨之消失，重連
	// 由前端重新全量上報，不需額外清理。
	watchMu   sync.Mutex
	watchDirs map[string]time.Time
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

// watchDirCount 回傳所有 client 監看中的目錄總數，供 status log 觀察。
func (h *Hub) watchDirCount() int {
	h.mu.RLock()
	clients := make([]*WsClient, 0, len(h.clients))
	for _, c := range h.clients {
		clients = append(clients, c)
	}
	h.mu.RUnlock()
	total := 0
	for _, c := range clients {
		c.watchMu.Lock()
		total += len(c.watchDirs)
		c.watchMu.Unlock()
	}
	return total
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

	// 依 config 設定 log 輸出（fmt → stdout、syslog → 本機 syslog，tag=html-editor）。
	if err := initLogging(cfg.LogMode, cfg.LogTag); err != nil {
		logf("[log] fallback to stdout: %v\n", err)
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
	if cfg.LoginNotify != nil && cfg.LoginNotify.Host != "" {
		logf("[config] login notifications enabled\n")
	}

	// goroutines 數量可協助觀察 syscall 卡住時的洩漏狀況：
	// runWithTimeout 超時後，卡在 syscall 的 goroutine 不會被回收，
	// 若這個數字長期上升、即使閒置也下不來，代表底層 FS 有問題。
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for range t.C {
			logf("[status] ws_clients=%d watch_dirs=%d goroutines=%d\n", s.hub.clientCount(), s.hub.watchDirCount(), runtime.NumGoroutine())
		}
	}()

	// rate limiter gc，清理過期 bans 與 stale attempts，避免 map 無限增長。
	go func() {
		t := time.NewTicker(10 * time.Minute)
		defer t.Stop()
		for range t.C {
			s.limiter.gc()
		}
	}()

	// 輪詢每個連線中 client 展開中的目錄，mtime 有變就推 dir_changed，
	// 讓前端 tree view 即時反映 CLI 等外部檔案系統變動。
	watchInterval := time.Duration(cfg.WatchPollInterval) * time.Second
	if watchInterval <= 0 {
		watchInterval = watchPollInterval
	}
	go func() {
		t := time.NewTicker(watchInterval)
		defer t.Stop()
		for range t.C {
			s.hub.mu.RLock()
			clients := make([]*WsClient, 0, len(s.hub.clients))
			for _, c := range s.hub.clients {
				clients = append(clients, c)
			}
			s.hub.mu.RUnlock()
			// seen 是本 tick 共享的 stat 快取
			seen := make(map[string]time.Time)
			for _, c := range clients {
				s.pollWatchDirs(c, seen)
			}
			// len(seen) = 本輪實際 stat 的唯一目錄數（去重後）。
			if len(seen) > 0 {
				// logf("[watch] tick observed %d unique dirs across %d clients\n", len(seen), len(clients))
			}
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

// notifyLogin sends an SMTP email for a login event, if enabled.
// It returns immediately; the mail is sent in a background goroutine so a slow
// or failing mail server never blocks or breaks the login flow.
func (s *server) notifyLogin(event, username, ip, reason string) {
	cfg := s.config.LoginNotify
	if cfg == nil || cfg.Host == "" {
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

	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	port := cfg.Port
	if port == 0 {
		port = 587
	}
	mode := strings.ToLower(strings.TrimSpace(cfg.TLS))
	if mode == "" {
		mode = "starttls"
	}

	from := notifyExpand(cfg.From, vars)
	rcpts := notifySplitAddrs(notifyExpand(cfg.To, vars))
	if from == "" || len(rcpts) == 0 {
		logf("[notify] missing from/to; skip event=%s user=%s\n", event, username)
		return
	}

	subject := notifyExpand(cfg.Subject, vars)
	// Body is HTML, so escape the (user-controlled) substituted values to
	// prevent markup injection from a crafted username/ip.
	htmlVars := make(map[string]string, len(vars))
	for k, v := range vars {
		htmlVars[k] = html.EscapeString(v)
	}
	body := notifyExpand(cfg.Body, htmlVars)
	msg := notifyBuildMail(from, cfg.To, subject, body)

	var auth smtp.Auth
	if cfg.Username != "" {
		auth = smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)
	}

	if err := notifySMTPSend(net.JoinHostPort(cfg.Host, strconv.Itoa(port)), cfg.Host, mode, timeout, auth, from, rcpts, msg); err != nil {
		logf("[notify] send failed event=%s user=%s: %v\n", event, username, err)
		return
	}
	logf("[notify] sent event=%s user=%s to=%s\n", event, username, strings.Join(rcpts, ","))
}

// notifySplitAddrs splits a comma/semicolon/space separated address list.
func notifySplitAddrs(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ';' || r == ' '
	})
}

// notifyBuildMail assembles an RFC 5322 HTML UTF-8 message. The Subject is
// MIME word-encoded so non-ASCII (e.g. Chinese) usernames survive transit.
func notifyBuildMail(from, to, subject, body string) []byte {
	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + to + "\r\n")
	b.WriteString("Subject: " + mime.QEncoding.Encode("utf-8", subject) + "\r\n")
	b.WriteString("Date: " + time.Now().Format(time.RFC1123Z) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/html; charset=\"utf-8\"\r\n")
	b.WriteString("\r\n")
	b.WriteString(body)
	return []byte(b.String())
}

// notifySMTPSend delivers msg over SMTP. mode selects "tls" (implicit TLS),
// "starttls" (upgrade after connect, when offered), or "none" (plaintext).
func notifySMTPSend(addr, host, mode string, timeout time.Duration, auth smtp.Auth, from string, to []string, msg []byte) error {
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return err
	}
	conn.SetDeadline(time.Now().Add(timeout))
	if mode == "tls" {
		conn = tls.Client(conn, &tls.Config{ServerName: host})
	}

	c, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return err
	}
	defer c.Close()

	if mode == "starttls" {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(&tls.Config{ServerName: host}); err != nil {
				return err
			}
		}
	}
	if auth != nil {
		if err := c.Auth(auth); err != nil {
			return err
		}
	}
	if err := c.Mail(from); err != nil {
		return err
	}
	for _, addr := range to {
		if err := c.Rcpt(addr); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
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
	resp := map[string]any{}
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
	wsPingInterval    = 30 * time.Second
	wsReadDeadline    = 70 * time.Second
	watchPollInterval = 3 * time.Second // tree view 目錄變動輪詢預設間隔（config 未設時）
	maxWatchDirs      = 500             // 單一 client 最多監看的目錄數，防濫用
)

// userWorkspace 回傳指定使用者的 workspace 絕對路徑。
func (s *server) userWorkspace(username string) (string, bool) {
	uc, ok := s.config.Users[username]
	if !ok {
		return "", false
	}
	abs, err := filepath.Abs(uc.Workspace)
	if err != nil {
		return "", false
	}
	return abs, true
}

// updateWatchDirs 以前端上報的完整清單覆蓋 client 的監看目錄。沿用仍在清單中
// 的舊 mtime，對新目錄先 stat 一次記下當前 mtime（避免註冊當下就誤報變動）。
func (s *server) updateWatchDirs(c *WsClient, paths []string) {
	if len(paths) > maxWatchDirs {
		paths = paths[:maxWatchDirs]
	}
	ws, ok := s.userWorkspace(c.username)

	c.watchMu.Lock()
	old := c.watchDirs
	c.watchMu.Unlock()

	next := make(map[string]time.Time, len(paths))
	for _, rel := range paths {
		if mt, exists := old[rel]; exists {
			next[rel] = mt
			continue
		}
		var mt time.Time
		if ok {
			if abs, err := resolvePath(ws, rel); err == nil {
				if info, err := os.Stat(abs); err == nil {
					mt = info.ModTime()
				}
			}
		}
		next[rel] = mt
	}

	c.watchMu.Lock()
	c.watchDirs = next
	c.watchMu.Unlock()
}

// pollWatchDirs 對 client 監看中的每個目錄 stat 一次，mtime 有變就推 dir_changed。
// 目錄 mtime 反映子項的增/刪/改名（正是 CLI 等外部變動），在 FUSE / 網路掛載上也可靠（stat 會走到後端），這是 fsnotify/inotify 在那些檔案系統上收不到的。
// seen 是本 tick 共享的 stat 快取（絕對路徑 → 觀測到的 mtime）；同一 abs 一輪只 stat 一次，多 client 監看同一目錄時省去重複 stat。key 用 abs 而非 rel，因各 user workspace 不同、同一 rel 可能指向不同實體目錄。stat 失敗記零值當哨兵，同輪不重試。
func (s *server) pollWatchDirs(c *WsClient, seen map[string]time.Time) {
	ws, ok := s.userWorkspace(c.username)
	if !ok {
		return
	}
	c.watchMu.Lock()
	paths := make([]string, 0, len(c.watchDirs))
	for p := range c.watchDirs {
		paths = append(paths, p)
	}
	c.watchMu.Unlock()

	for _, rel := range paths {
		abs, err := resolvePath(ws, rel)
		if err != nil {
			continue
		}
		mt, cached := seen[abs]
		if !cached {
			// 底層 FS 卡住時 os.Stat 可能不回來；用 runWithTimeout 包住，讓卡住的目錄只拖到自己、不會癱瘓整個 poll 迴圈（卡住的 goroutine 仍會殘留到 syscall 自然回傳，與本檔其他 FS 操作同一個已知上限）。失敗時 mt 維持零值，寫進快取當哨兵。
			runWithTimeout(context.Background(), fileOpQuickTimeout, func() error {
				info, ferr := os.Stat(abs)
				if ferr == nil {
					mt = info.ModTime()
				}
				return ferr
			})
			seen[abs] = mt
		}
		if mt.IsZero() { // stat 失敗（真實目錄 mtime 不可能是零值）
			logf("[watch] stat_failed user=%s path=%s\n", c.username, rel)
			continue
		}
		c.watchMu.Lock()
		prev, exists := c.watchDirs[rel]
		changed := exists && !mt.Equal(prev)
		if changed {
			c.watchDirs[rel] = mt
		}
		c.watchMu.Unlock()
		if changed {
			s.hub.sendTo(c.username, wsOutMsg{Type: "dir_changed", Payload: map[string]string{"path": rel}})
		}
	}
}

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

		case "watch_dirs":
			var p struct {
				Paths []string `json:"paths"`
			}
			if err := json.Unmarshal(msg.Payload, &p); err != nil {
				logf("[ws] watch_dirs bad_payload user=%s err=%v\n", c.username, err)
				continue
			}
			s.updateWatchDirs(c, p.Paths)

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
	ws := workspaceFromCtx(r)
	dir, err := resolvePath(ws, rel)
	if err != nil {
		writeError(w, http.StatusForbidden, "invalid path")
		return
	}
	// ReadDir 與整個 Stat 迴圈一起包 timeout：底層 FS 卡住時任何一次
	// Stat 都可能 hang 住，逐個包反而失去保險意義。
	var entries []fileEntry
	err = runWithTimeout(r.Context(), fileOpQuickTimeout, func() error {
		des, ferr := os.ReadDir(dir)
		if ferr != nil {
			return ferr
		}
		for _, de := range des {
			fullPath := filepath.Join(dir, de.Name())
			info, ferr := os.Stat(fullPath)
			if ferr != nil {
				continue
			}
			entRel, ferr := filepath.Rel(ws, fullPath)
			if ferr != nil {
				continue
			}
			// 偵測 symlink；Windows 的 directory junction 在 Go 中會被回報為
			// ModeIrregular，所以兩種 reparse point 都用 os.Readlink 探測。
			var (
				isSymlink  bool
				linkTarget string
			)
			if de.Type()&(os.ModeSymlink|os.ModeIrregular) != 0 {
				if t, ferr := os.Readlink(fullPath); ferr == nil {
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
		return nil
	})
	if errors.Is(err, context.DeadlineExceeded) {
		writeTimeoutError(w, "list files")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
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
	// Stat 與 ReadFile 都可能卡在 I/O，一起包 timeout。
	// 編輯器情境下檔案預期都不大，20 秒對正常磁碟 / 網路綽綽有餘；
	// 真正卡死時也會在這裡被截掉。
	var (
		info os.FileInfo
		data []byte
	)
	err = runWithTimeout(r.Context(), fileOpIOTimeout, func() error {
		var ferr error
		info, ferr = os.Stat(abs)
		if ferr != nil {
			return ferr
		}
		if info.IsDir() {
			return nil
		}
		data, ferr = os.ReadFile(abs)
		return ferr
	})
	if errors.Is(err, context.DeadlineExceeded) {
		writeTimeoutError(w, "read file")
		return
	}
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
	// body 已收完再開始落盤；MkdirAll + WriteFile 都可能因底層 FS 卡住，一起包 timeout。
	err = runWithTimeout(r.Context(), fileOpIOTimeout, func() error {
		if ferr := os.MkdirAll(filepath.Dir(abs), 0o755); ferr != nil {
			return ferr
		}
		return os.WriteFile(abs, body, 0o644)
	})
	if errors.Is(err, context.DeadlineExceeded) {
		writeTimeoutError(w, "write file")
		return
	}
	if err != nil {
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
	// Stat + Remove/RemoveAll 都包 timeout：RemoveAll 會遍歷整棵樹，
	// 在不穩的 FS 上很容易卡住，這是最該保險的操作之一。
	err = runWithTimeout(r.Context(), fileOpDeleteTimeout, func() error {
		info, ferr := os.Stat(abs)
		if ferr != nil {
			return ferr
		}
		if info.IsDir() {
			return os.RemoveAll(abs)
		}
		return os.Remove(abs)
	})
	if errors.Is(err, context.DeadlineExceeded) {
		writeTimeoutError(w, "delete")
		return
	}
	if err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
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
		s.downloadDirAsZip(w, r, abs)
		return
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(abs)))
	http.ServeFile(w, r, abs)
}

// 目錄 zip 下載的硬上限：避免不小心點到超大目錄把 server CPU/頻寬吃滿。
// 超過任一條件就在送出 header 前以 413 拒絕，不會留下半個壞掉的 zip 給 client。
const (
	zipMaxTotalSize = 500 * 1024 * 1024 // 500MB 未壓縮總和
	zipMaxFileCount = 10000
)

// 預掃時用的 sentinel error，讓 WalkDir 中斷後上層能對應到正確的 413 訊息。
var (
	errZipTooManyFiles = errors.New("zip: too many files")
	errZipTooLarge     = errors.New("zip: total size too large")
)

// downloadDirAsZip 把目錄串流壓成 zip 回給 client。
//
// 流程：先 WalkDir 預掃一次計算檔案數與未壓縮總和，任一超過硬上限就回 413；
// 通過後才寫 header、開 zip.Writer 邊壓邊送。Symlink / Windows junction 一律
// 跳過，避免跑出 workspace 外、避免循環連結造成無限走訪。
//
// 不額外套 runWithTimeout：大目錄壓縮本來就慢，逾時應由 client 取消連線
// （r.Context() 會被 net/http 在 client 關閉時 cancel）驅動，而不是 server
// 端硬截斷。
func (s *server) downloadDirAsZip(w http.ResponseWriter, r *http.Request, dir string) {
	ctx := r.Context()
	var totalSize int64
	var fileCount int
	walkErr := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.Type()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, ferr := d.Info()
		if ferr != nil {
			return ferr
		}
		fileCount++
		if fileCount > zipMaxFileCount {
			return errZipTooManyFiles
		}
		totalSize += info.Size()
		if totalSize > zipMaxTotalSize {
			return errZipTooLarge
		}
		return nil
	})
	switch {
	case errors.Is(walkErr, errZipTooManyFiles):
		logf("[zip] reject user=%s path=%s reason=too_many_files\n", usernameFromCtx(r), filepath.Base(dir))
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("too many files (limit %d)", zipMaxFileCount))
		return
	case errors.Is(walkErr, errZipTooLarge):
		logf("[zip] reject user=%s path=%s reason=too_large\n", usernameFromCtx(r), filepath.Base(dir))
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("total size exceeds limit %d bytes", zipMaxTotalSize))
		return
	case errors.Is(walkErr, context.Canceled), errors.Is(walkErr, context.DeadlineExceeded):
		return
	case walkErr != nil:
		writeError(w, http.StatusInternalServerError, walkErr.Error())
		return
	}

	name := filepath.Base(dir) + ".zip"
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))

	zw := zip.NewWriter(w)
	// 算 zip 相對路徑時用 dir 的父層當基準，這樣 zip 內最外層就是目錄本身
	// （例如下載 foo/bar 時 zip 解出來會得到 bar/...）。
	baseParent := filepath.Dir(dir)
	writeErr := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.Type()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		relPath, rerr := filepath.Rel(baseParent, p)
		if rerr != nil {
			return rerr
		}
		zipName := filepath.ToSlash(relPath)
		info, ferr := d.Info()
		if ferr != nil {
			return ferr
		}
		if d.IsDir() {
			_, werr := zw.CreateHeader(&zip.FileHeader{
				Name:     zipName + "/",
				Method:   zip.Store,
				Modified: info.ModTime(),
			})
			return werr
		}
		fh, ferr := zip.FileInfoHeader(info)
		if ferr != nil {
			return ferr
		}
		fh.Name = zipName
		fh.Method = zip.Deflate
		writer, werr := zw.CreateHeader(fh)
		if werr != nil {
			return werr
		}
		src, oerr := os.Open(p)
		if oerr != nil {
			return oerr
		}
		_, cerr := io.Copy(writer, src)
		src.Close()
		return cerr
	})
	// header 已送出，中途失敗只能中斷連線；client 端會拿到一個壞掉的 zip。
	if writeErr != nil {
		logf("[zip] err user=%s path=%s err=%v\n", usernameFromCtx(r), filepath.Base(dir), writeErr)
		return
	}
	if err := zw.Close(); err != nil {
		logf("[zip] err user=%s path=%s err=%v\n", usernameFromCtx(r), filepath.Base(dir), err)
		return
	}
	logf("[zip] ok user=%s path=%s files=%d size=%d\n", usernameFromCtx(r), filepath.Base(dir), fileCount, totalSize)
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
	// 防止把目錄移動到自身的子目錄（os.Rename 在此情境會回難懂的 OS 錯誤）。
	// 只擋嚴格子孫路徑：dst==src 的 no-op（如剪下檔案貼回原資料夾）交給後續邏輯處理。
	srcClean := filepath.Clean(absFrom)
	dstClean := filepath.Clean(absTo)
	if dstClean != srcClean && strings.HasPrefix(dstClean+string(filepath.Separator), srcClean+string(filepath.Separator)) {
		writeError(w, http.StatusBadRequest, "無法將目錄移動到自身的子目錄")
		return
	}
	// auto=1：目的地已存在時不報衝突，改自動加 .1/.2… 後綴取得可用名稱（用於剪下/貼上的搬移）。
	// auto=0（預設）：目的地已存在則回 409 衝突（用於重新命名，避免覆蓋既有檔案）。
	auto := r.URL.Query().Get("auto") == "1"
	// availableDest 內部會跑多次 Stat、Rename 也是 metadata-only，
	// 一起包 quick timeout；底層 FS 卡死時不會留下半完成狀態的請求。
	var conflict bool
	err = runWithTimeout(r.Context(), fileOpQuickTimeout, func() error {
		if auto {
			absTo = availableDest(absTo)
		} else if _, ferr := os.Stat(absTo); ferr == nil {
			conflict = true
			return nil
		}
		return os.Rename(absFrom, absTo)
	})
	if errors.Is(err, context.DeadlineExceeded) {
		writeTimeoutError(w, "rename")
		return
	}
	if conflict {
		writeError(w, http.StatusConflict, "destination already exists")
		return
	}
	if err != nil {
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
	// MkdirAll 在不穩的 FS 上同樣會卡，包個 quick timeout 兜底。
	err = runWithTimeout(r.Context(), fileOpQuickTimeout, func() error {
		return os.MkdirAll(abs, 0o755)
	})
	if errors.Is(err, context.DeadlineExceeded) {
		writeTimeoutError(w, "mkdir")
		return
	}
	if err != nil {
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

// log 輸出 sink，由 initLogging 依 config logMode 設定（見 logsetup_unix.go / logsetup_other.go）。
// 預設 stdout 並自帶時間戳；syslog 模式改寫到本機 syslog（時間戳由 syslog 提供，故關閉自帶時間戳）。
const (
	logModeFmt    = "fmt"
	logModeSyslog = "syslog"
)

var (
	logWriter io.Writer = os.Stdout
	logMode             = logModeFmt
)

func logf(format string, args ...any) {
	if logMode == logModeFmt {
		fmt.Fprintf(logWriter, time.Now().Format("2006/01/02 15:04:05")+" "+format, args...)
	} else {
		fmt.Fprintf(logWriter, format, args...)
	}
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

// ─── 檔案操作 timeout 保險 ────────────────────────────────────────────────────
//
// 底層 FS 異常時（SFTP/網路掛載斷線、磁碟壞軌、外接碟拔除、防毒軟體掃描
// 中、kernel I/O 卡住等），os.Stat、os.ReadDir 等 syscall 可能永遠不回來。
// 為避免 handler goroutine 與 HTTP 請求一起卡死，所有「預期很快完成」的
// metadata 操作都用 runWithTimeout 包起來，超時就回 504。
//
// 限制：Go 沒辦法中斷正在執行 syscall 的 goroutine，因此真正卡住的
// goroutine（與其占用的 OS thread）會殘留到 syscall 自然回傳為止。
// 我們換取的是 HTTP 請求立即釋放、client 不會一起被卡住，server 連線
// 池也不會被吃完。
const (
	fileOpQuickTimeout  = 8 * time.Second  // list / mkdir / rename：純 metadata，健康狀況下秒回
	fileOpIOTimeout     = 20 * time.Second // read / write：編輯器存取文字檔的場景，預期都是小檔
	fileOpDeleteTimeout = 30 * time.Second // delete：RemoveAll 大資料夾要逐檔，給寬一點
)

// runWithTimeout 在 d 時間內執行 fn；逾時時回傳 ctx.Err()（DeadlineExceeded）。
// fn 內若卡在 blocking syscall，goroutine 會洩漏到 syscall 自然回來為止，
// 但呼叫端會立即返回，不會把 HTTP 請求一起拖住。
func runWithTimeout(ctx context.Context, d time.Duration, fn func() error) error {
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// writeTimeoutError 統一回 504，並標示是哪個操作逾時，方便前端與 log 追蹤。
func writeTimeoutError(w http.ResponseWriter, op string) {
	writeError(w, http.StatusGatewayTimeout, fmt.Sprintf("file operation timed out: %s", op))
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
