package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pkulkarni/apreg/internal/api/middleware/limitertest"
)

func TestRateLimiter_Conformance(t *testing.T) {
	limitertest.RunConformanceSuite(t, func(t *testing.T, burst int, per time.Duration) limitertest.Limiter {
		return NewRateLimiter(burst, per)
	})
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
