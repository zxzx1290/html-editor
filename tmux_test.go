//go:build linux || darwin

package main

import "testing"

// requireOwn 是 attach / kill / write / resize 共用的授權關卡。
func TestRequireOwn(t *testing.T) {
	m := &tmuxManager{}
	cases := []struct {
		user, name string
		ok         bool
	}{
		{"alice", "alice-abc123", true},
		{"bob", "alice-abc123", false},     // 別人的 session
		{"alice", "alice-x-abc123", false}, // 使用者名稱含 "-" 時不可被前綴吃掉
		{"alice-x", "alice-x-abc123", true},
		{"alice", "alice-ABC123", false}, // 格式不符（尾碼須小寫）
		{"alice", "alice", false},
		{"alice", "../../etc/passwd", false},
		{"", "-abc123", false},
	}
	for _, c := range cases {
		err := m.requireOwn(c.user, c.name)
		if (err == nil) != c.ok {
			t.Errorf("requireOwn(%q, %q) = %v, 期望 ok=%v", c.user, c.name, err, c.ok)
		}
	}
}

// detach 是清理操作、沒有回傳值，所以驗證「被拒絕時對方的 attach 仍在」。
func TestDetachRejectsOtherUser(t *testing.T) {
	m := &tmuxManager{attaches: map[string]*tmuxAttach{
		"alice-abc123": {name: "alice-abc123", owner: "alice"},
	}}
	m.detach("bob", "alice-abc123")
	if _, ok := m.attaches["alice-abc123"]; !ok {
		t.Fatal("bob 不該 detach 掉 alice 的 attach")
	}
}

// 別人的 session 名不該能拿來操作對方的 PTY（term_input / term_resize 的授權點）。
func TestLookupAttachOwnership(t *testing.T) {
	m := &tmuxManager{attaches: map[string]*tmuxAttach{
		"alice-abc123": {name: "alice-abc123", owner: "alice"},
	}}
	if _, err := m.lookupAttach("bob", "alice-abc123"); err == nil {
		t.Fatal("bob 不該拿得到 alice 的 attach")
	}
	if _, err := m.lookupAttach("alice", "alice-zzzzzz"); err == nil {
		t.Fatal("不存在的 session 應回錯誤")
	}
	if _, err := m.lookupAttach("alice", "alice-abc123"); err != nil {
		t.Fatalf("owner 自己應該可以操作: %v", err)
	}
}
