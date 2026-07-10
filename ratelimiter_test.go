package main

import (
	"testing"
	"time"
)

// 門檻與封鎖轉換：未達門檻不封鎖，達門檻的那一次 record 回 true（僅此一次），
// 之後維持封鎖但 record 回 false（避免重複通知），且不同 key 互不影響。
func TestRateLimiterBanTransition(t *testing.T) {
	rl := newRateLimiter(time.Minute, 3, time.Minute)

	if rl.record("a") || rl.record("a") {
		t.Fatal("banned before reaching maxAttempts")
	}
	if rl.isBlocked("a") {
		t.Fatal("blocked before reaching maxAttempts")
	}
	if !rl.record("a") { // 第 3 次 = 轉入封鎖
		t.Fatal("3rd record should be the ban transition (true)")
	}
	if !rl.isBlocked("a") {
		t.Fatal("should be blocked after maxAttempts")
	}
	if rl.record("a") { // 已封鎖，不應再回 true
		t.Fatal("record should return false while already banned")
	}
	if rl.isBlocked("b") {
		t.Fatal("unrelated key should not be blocked")
	}
}

// 封鎖到期後自動解除，gc 清掉過期 ban 與視窗外的 stale attempts，map 不會無限長大。
func TestRateLimiterExpiryAndGC(t *testing.T) {
	rl := newRateLimiter(20*time.Millisecond, 1, 30*time.Millisecond)

	rl.record("x") // maxAttempts=1 → 立即封鎖
	if !rl.isBlocked("x") {
		t.Fatal("should be blocked immediately at maxAttempts=1")
	}
	time.Sleep(60 * time.Millisecond) // 過 ban 與 window
	if rl.isBlocked("x") {
		t.Fatal("ban should have expired")
	}

	rl.record("y")
	time.Sleep(60 * time.Millisecond)
	rl.gc()

	rl.mu.Lock()
	defer rl.mu.Unlock()
	if len(rl.attempts) != 0 || len(rl.bans) != 0 {
		t.Fatalf("gc left stale entries: attempts=%d bans=%d", len(rl.attempts), len(rl.bans))
	}
}
