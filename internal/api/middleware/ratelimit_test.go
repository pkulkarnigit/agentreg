package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRateLimiter_AllowsUpToBurstThenBlocks(t *testing.T) {
	rl := NewRateLimiter(3, time.Hour)
	for i := 0; i < 3; i++ {
		if !rl.Allow("alice") {
			t.Fatalf("request %d: expected allowed within burst", i)
		}
	}
	if rl.Allow("alice") {
		t.Fatal("expected 4th request to be blocked")
	}
}

func TestRateLimiter_IndependentPerKey(t *testing.T) {
	rl := NewRateLimiter(1, time.Hour)
	if !rl.Allow("alice") {
		t.Fatal("expected alice's first request to be allowed")
	}
	if !rl.Allow("bob") {
		t.Fatal("expected bob's first request to be allowed independently of alice's bucket")
	}
	if rl.Allow("alice") {
		t.Fatal("expected alice's second request to be blocked")
	}
}

func TestRateLimiter_RefillsOverTime(t *testing.T) {
	rl := NewRateLimiter(1, 50*time.Millisecond)
	if !rl.Allow("alice") {
		t.Fatal("expected first request to be allowed")
	}
	if rl.Allow("alice") {
		t.Fatal("expected immediate second request to be blocked")
	}
	time.Sleep(60 * time.Millisecond)
	if !rl.Allow("alice") {
		t.Fatal("expected request after refill window to be allowed")
	}
}

func TestRateLimit_Middleware(t *testing.T) {
	rl := NewRateLimiter(1, time.Hour)
	handler := RateLimit(rl, ByIP, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "1.2.3.4:5555"

	rec1 := httptest.NewRecorder()
	handler(rec1, req)
	if rec1.Code != http.StatusOK {
		t.Fatalf("expected first request 200, got %d", rec1.Code)
	}

	rec2 := httptest.NewRecorder()
	handler(rec2, req)
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("expected second request 429, got %d", rec2.Code)
	}

	// A different IP is a different bucket.
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.RemoteAddr = "9.9.9.9:1111"
	rec3 := httptest.NewRecorder()
	handler(rec3, req2)
	if rec3.Code != http.StatusOK {
		t.Fatalf("expected different IP to be allowed, got %d", rec3.Code)
	}
}
