package auth

import (
	"testing"
	"time"
)

func TestLoginLockout_LocksAfterMaxFailures(t *testing.T) {
	l := NewLoginLockout(3, time.Hour)
	for i := 0; i < 2; i++ {
		l.RecordFailure("alice")
		if l.Locked("alice") {
			t.Fatalf("should not be locked after %d failures", i+1)
		}
	}
	l.RecordFailure("alice")
	if !l.Locked("alice") {
		t.Fatal("expected lockout after 3rd failure")
	}
}

func TestLoginLockout_IndependentPerUsername(t *testing.T) {
	l := NewLoginLockout(1, time.Hour)
	l.RecordFailure("alice")
	if !l.Locked("alice") {
		t.Fatal("expected alice locked")
	}
	if l.Locked("bob") {
		t.Fatal("expected bob unaffected by alice's failures")
	}
}

func TestLoginLockout_ExpiresOutsideWindow(t *testing.T) {
	l := NewLoginLockout(1, 30*time.Millisecond)
	l.RecordFailure("alice")
	if !l.Locked("alice") {
		t.Fatal("expected locked immediately after failure")
	}
	time.Sleep(40 * time.Millisecond)
	if l.Locked("alice") {
		t.Fatal("expected lockout to expire once failure ages out of window")
	}
}

func TestLoginLockout_ResetClearsHistory(t *testing.T) {
	l := NewLoginLockout(1, time.Hour)
	l.RecordFailure("alice")
	if !l.Locked("alice") {
		t.Fatal("expected locked after failure")
	}
	l.Reset("alice")
	if l.Locked("alice") {
		t.Fatal("expected Reset to clear lockout")
	}
}
