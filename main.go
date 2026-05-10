package main

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// maxUploadSize is 50 MB，單次上傳的檔案大小上限。
const maxUploadSize = 50 * 1024 * 1024

type server struct {
	workspace string
	username  string
	password  string
	authOn    bool
}

type fileEntry struct {
	Path  string `json:"path"`
	Name  string `json:"name"`
	IsDir bool   `json:"isDir"`
	Size  int64  `json:"size"`
}

func main() {
	var (
		port      = flag.Int("port", 8080, "listen port")
		host      = flag.String("host", "127.0.0.1", "listen host")
		workspace = flag.String("workspace", "./workspace", "workspace directory")
		username  = flag.String("username", "admin", "basic auth username")
		password  = flag.String("password", "", "basic auth password (enables auth if set)")
	)
	flag.Parse()

	abs, err := filepath.Abs(*workspace)
	if err != nil {
		log.Fatalf("invalid workspace path: %v", err)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		log.Fatalf("failed to create workspace: %v", err)
	}

	s := &server{
		workspace: abs,
		username:  *username,
		password:  *password,
		authOn:    *password != "",
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/files", s.handleListFiles)
	mux.HandleFunc("/api/file", s.handleFile)
	mux.HandleFunc("/api/upload", s.handleUpload)
	mux.HandleFunc("/api/download", s.handleDownload)
	mux.HandleFunc("/api/mkdir", s.handleMkdir)
	mux.HandleFunc("/api/rename", s.handleRename)
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))
	mux.HandleFunc("/", s.handleIndex)

	addr := fmt.Sprintf("%s:%d", *host, *port)
	log.Printf("html-editor listening on http://%s (workspace=%s, auth=%v)", addr, abs, s.authOn)
	if err := http.ListenAndServe(addr, s.withAuth(mux)); err != nil {
		log.Fatal(err)
	}
}

func (s *server) withAuth(h http.Handler) http.Handler {
	if !s.authOn {
		return h
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok ||
			subtle.ConstantTimeCompare([]byte(u), []byte(s.username)) != 1 ||
			subtle.ConstantTimeCompare([]byte(p), []byte(s.password)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="html-editor"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	})
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

func (s *server) resolvePath(rel string) (string, error) {
	if rel == "" {
		return s.workspace, nil
	}
	cleaned := filepath.Clean("/" + rel)
	cleaned = strings.TrimPrefix(cleaned, "/")
	abs := filepath.Join(s.workspace, cleaned)
	absResolved, err := filepath.Abs(abs)
	if err != nil {
		return "", err
	}
	if absResolved != s.workspace && !strings.HasPrefix(absResolved, s.workspace+string(os.PathSeparator)) {
		return "", errors.New("path escapes workspace")
	}
	return absResolved, nil
}

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
	dir, err := s.resolvePath(rel)
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
		info, err := os.Stat(fullPath) // follows symlinks
		if err != nil {
			continue // broken symlink，略過
		}
		entRel, err := filepath.Rel(s.workspace, fullPath)
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
	abs, err := s.resolvePath(rel)
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
	fmt.Printf("[open] %s\n", rel)
	ct := mime.TypeByExtension(strings.ToLower(filepath.Ext(rel)))
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	http.ServeFile(w, r, abs)
}

func (s *server) writeFile(w http.ResponseWriter, r *http.Request) {
	rel := r.URL.Query().Get("path")
	if rel == "" {
		writeError(w, http.StatusBadRequest, "missing path")
		return
	}
	abs, err := s.resolvePath(rel)
	if err != nil {
		writeError(w, http.StatusForbidden, "invalid path")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
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
	fmt.Printf("[save] %s\n", rel)
	writeOK(w)
}

func (s *server) deleteFile(w http.ResponseWriter, r *http.Request) {
	rel := r.URL.Query().Get("path")
	if rel == "" {
		writeError(w, http.StatusBadRequest, "missing path")
		return
	}
	abs, err := s.resolvePath(rel)
	if err != nil || abs == s.workspace {
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
	fmt.Printf("[del] %s\n", rel)
	writeOK(w)
}

func (s *server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
	if err := r.ParseMultipartForm(maxUploadSize); err != nil {
		writeError(w, http.StatusBadRequest, "upload too large or malformed")
		return
	}
	dir := r.FormValue("path")
	dirAbs, err := s.resolvePath(dir)
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

	if header.Size > maxUploadSize {
		writeError(w, http.StatusRequestEntityTooLarge, "file too large")
		return
	}

	if err := os.MkdirAll(dirAbs, 0o755); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	dstPath := filepath.Join(dirAbs, filepath.Base(header.Filename))
	dstResolved, err := filepath.Abs(dstPath)
	if err != nil || (dstResolved != s.workspace && !strings.HasPrefix(dstResolved, s.workspace+string(os.PathSeparator))) {
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
	rel, _ := filepath.Rel(s.workspace, dstResolved)
	fmt.Printf("[upload] %s\n", filepath.ToSlash(rel))
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
	abs, err := s.resolvePath(rel)
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
	from := r.URL.Query().Get("from")
	to := r.URL.Query().Get("to")
	if from == "" || to == "" {
		writeError(w, http.StatusBadRequest, "missing from or to")
		return
	}
	absFrom, err := s.resolvePath(from)
	if err != nil || absFrom == s.workspace {
		writeError(w, http.StatusForbidden, "invalid from path")
		return
	}
	absTo, err := s.resolvePath(to)
	if err != nil || absTo == s.workspace {
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
	fmt.Printf("[rename] %s -> %s\n", from, to)
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
	abs, err := s.resolvePath(rel)
	if err != nil {
		writeError(w, http.StatusForbidden, "invalid path")
		return
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	fmt.Printf("[mkdir] %s\n", rel)
	writeOK(w)
}
