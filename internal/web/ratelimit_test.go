package web

import (
	"testing"
	"time"
)

func TestRateLimiter(t *testing.T) {
	rl := newRateLimiter(2, time.Minute)
	now := time.Now()

	if !rl.allow("ip", now) || !rl.allow("ip", now) {
		t.Fatal("first two attempts should be allowed")
	}
	if rl.allow("ip", now) {
		t.Fatal("third attempt within the window should be blocked")
	}
	if !rl.allow("other", now) {
		t.Fatal("a different key should not be affected")
	}
	if !rl.allow("ip", now.Add(2*time.Minute)) {
		t.Fatal("attempt after the window should be allowed again")
	}
}
