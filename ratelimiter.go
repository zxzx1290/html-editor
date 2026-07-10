package main

import (
	"sync"
	"time"
)

// ─── Rate limiter (in-memory, resets on restart) ──────────────────────────────
//
// 滑動視窗計數 + 逾額封鎖，key 為任意字串（登入用 IP、WS 連線用 username）。

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
