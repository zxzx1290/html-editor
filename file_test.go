package main

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func withWorkspace(r *http.Request, ws string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), ctxWorkspace, ws))
}

// 上傳檔名 ".." 時 filepath.Base 會原樣回傳，Join 後即跳出 workspace（dir 為根時）。
func TestUploadRejectsDotDotFilename(t *testing.T) {
	ws := t.TempDir()
	s := &server{config: &Config{}}

	for _, name := range []string{"..", "."} {
		body := &bytes.Buffer{}
		mw := multipart.NewWriter(body)
		fw, err := mw.CreateFormFile("file", name)
		if err != nil {
			t.Fatal(err)
		}
		fw.Write([]byte("x"))
		mw.WriteField("path", "")
		mw.Close()

		r := httptest.NewRequest(http.MethodPost, "/api/upload", body)
		r.Header.Set("Content-Type", mw.FormDataContentType())
		w := httptest.NewRecorder()
		s.handleUpload(w, withWorkspace(r, ws))

		if w.Code != http.StatusBadRequest {
			t.Errorf("filename=%q: 期望 400，得到 %d (%s)", name, w.Code, w.Body.String())
		}
	}
}

func TestUploadAcceptsNormalFilename(t *testing.T) {
	ws := t.TempDir()
	s := &server{config: &Config{}}

	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	fw, _ := mw.CreateFormFile("file", "note.txt")
	fw.Write([]byte("hi"))
	mw.WriteField("path", "")
	mw.Close()

	r := httptest.NewRequest(http.MethodPost, "/api/upload", body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	s.handleUpload(w, withWorkspace(r, ws))

	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，得到 %d (%s)", w.Code, w.Body.String())
	}
	if got, err := os.ReadFile(filepath.Join(ws, "note.txt")); err != nil || string(got) != "hi" {
		t.Fatalf("檔案內容不符: %q %v", got, err)
	}
}

// 開檔大小由前端的確認框把關，後端不設上限：大檔照樣讀得回來。
func TestReadFileNoSizeLimit(t *testing.T) {
	ws := t.TempDir()
	big := bytes.Repeat([]byte("a"), 1<<20)
	if err := os.WriteFile(filepath.Join(ws, "big.txt"), big, 0o644); err != nil {
		t.Fatal(err)
	}
	s := &server{config: &Config{}}

	w := httptest.NewRecorder()
	s.readFile(w, withWorkspace(httptest.NewRequest(http.MethodGet, "/api/file?path=big.txt", nil), ws))
	if w.Code != http.StatusOK || w.Body.Len() != len(big) {
		t.Errorf("期望 200 且完整回傳 %d bytes，得到 %d (%d bytes)", len(big), w.Code, w.Body.Len())
	}
}
