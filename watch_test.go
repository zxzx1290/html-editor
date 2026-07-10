package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 驗證 updateWatchDirs 記下初始 mtime、且不會在註冊當下誤報，
// 接著目錄 mtime 改變時 pollWatchDirs 會推 dir_changed。
func TestWatchDirsDetectChange(t *testing.T) {
	dir := t.TempDir()
	s := &server{config: &Config{Users: map[string]UserConfig{
		"u": {Workspace: dir},
	}}, hub: newHub()}
	c := &WsClient{username: "u", send: make(chan []byte, 8), hub: s.hub}
	s.hub.register(c)

	// 監看根目錄；此時記下當前 mtime。
	s.updateWatchDirs(c, []string{""})

	// 註冊當下不應誤報。
	s.pollWatchDirs(c, map[string]time.Time{})
	select {
	case <-c.send:
		t.Fatal("unexpected dir_changed on unchanged dir")
	default:
	}

	// 明確把目錄 mtime 往後推，模擬 CLI 新增/刪除子項造成的變動（避免同一時鐘刻度導致偵測不到）。
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(dir), future, future); err != nil {
		t.Fatal(err)
	}

	s.pollWatchDirs(c, map[string]time.Time{})
	select {
	case data := <-c.send:
		var m wsOutMsg
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatal(err)
		}
		if m.Type != "dir_changed" {
			t.Fatalf("got type %q, want dir_changed", m.Type)
		}
	default:
		t.Fatal("expected dir_changed after mtime bump")
	}
}
