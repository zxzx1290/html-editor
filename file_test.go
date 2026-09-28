package main

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
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

// readFile 要回 ETag，writeFile 帶對的 If-Match 要能寫入並回新 ETag；
// 帶舊 ETag（檔案已被別人改過）要回 412 且原檔案內容不能被動到。
// ETag 是內容雜湊，所以 mtime 變了但內容沒變（touch、git checkout 寫回同樣內容）不該算衝突。
func TestWriteFileIfMatch(t *testing.T) {
	ws := t.TempDir()
	abs := filepath.Join(ws, "note.txt")
	if err := os.WriteFile(abs, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &server{config: &Config{}}

	readETag := func() string {
		w := httptest.NewRecorder()
		s.readFile(w, withWorkspace(httptest.NewRequest(http.MethodGet, "/api/file?path=note.txt", nil), ws))
		if w.Code != http.StatusOK {
			t.Fatalf("readFile 失敗: code=%d (%s)", w.Code, w.Body.String())
		}
		return w.Header().Get("ETag")
	}
	// 前端是從 PUT 回應的 JSON 取新 ETag（見 index.html 的 putFile），測試照同樣方式讀
	etagOf := func(rec *httptest.ResponseRecorder) string {
		var resp struct {
			ETag string `json:"etag"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("PUT 回應不是 JSON: %v (%s)", err, rec.Body.String())
		}
		return resp.ETag
	}
	put := func(body, ifMatch string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPut, "/api/file?path=note.txt", bytes.NewBufferString(body))
		if ifMatch != "" {
			r.Header.Set("If-Match", ifMatch)
		}
		rec := httptest.NewRecorder()
		s.writeFile(rec, withWorkspace(r, ws))
		return rec
	}

	// 開檔：拿到 v1 的 ETag
	etag := readETag()
	if etag == "" {
		t.Fatal("readFile 沒回 ETag")
	}

	// mtime 被動到但內容沒變 → 指紋不該變（換成 mtime+size 就會在這裡誤報衝突）
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(abs, old, old); err != nil {
		t.Fatal(err)
	}
	if again := readETag(); again != etag {
		t.Fatalf("內容沒變時 ETag 不該變: %q → %q", etag, again)
	}

	// 帶對的 ETag → 寫入成功，且回新的 ETag（v1/v2 長度相同，mtime+size 在同一秒內會漏判這種改動）
	rec := put("v2", etag)
	if rec.Code != http.StatusOK {
		t.Fatalf("帶正確 ETag 應寫入成功，得到 %d (%s)", rec.Code, rec.Body.String())
	}
	newETag := etagOf(rec)
	if newETag == "" || newETag == etag {
		t.Fatalf("寫入後應回不同的新 ETag: old=%q new=%q", etag, newETag)
	}
	if got, _ := os.ReadFile(abs); string(got) != "v2" {
		t.Fatalf("內容應為 v2，得到 %q", got)
	}

	// 再用舊 ETag 存（模擬他人已改過）→ 412，且檔案不被覆寫
	rec = put("rejected", etag)
	if rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("帶過期 ETag 應回 412，得到 %d (%s)", rec.Code, rec.Body.String())
	}
	if got, _ := os.ReadFile(abs); string(got) != "v2" {
		t.Fatalf("412 不該動到檔案，內容變成 %q", got)
	}

	// 存檔回傳的 ETag 要能直接當下一次的 If-Match（前端就是這樣接手的，不會再抓一次檔案），
	// 而且要跟重新開檔拿到的一致。
	if got := readETag(); got != newETag {
		t.Fatalf("PUT 回的 ETag 應與重新 GET 一致: put=%q get=%q", newETag, got)
	}
	rec = put("v3", newETag)
	if rec.Code != http.StatusOK {
		t.Fatalf("帶前次存檔回傳的 ETag 應寫入成功，得到 %d (%s)", rec.Code, rec.Body.String())
	}
	newETag = etagOf(rec)

	// 不帶 If-Match → 維持原本的無條件覆寫
	if rec = put("v4", ""); rec.Code != http.StatusOK {
		t.Fatalf("不帶 If-Match 應照舊寫入，得到 %d (%s)", rec.Code, rec.Body.String())
	}
	if got, _ := os.ReadFile(abs); string(got) != "v4" {
		t.Fatalf("內容應為 v4，得到 %q", got)
	}

	// If-Match 指向已被刪除的檔案 → 412，不該悄悄重建
	if err := os.Remove(abs); err != nil {
		t.Fatal(err)
	}
	if rec = put("v5", newETag); rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("檔案已刪除應回 412，得到 %d (%s)", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(abs); !os.IsNotExist(err) {
		t.Fatalf("412 不該重建檔案: %v", err)
	}
}
