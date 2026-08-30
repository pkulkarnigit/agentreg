package auth

import "testing"

func TestPasswordHashing(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !CheckPassword(hash, "correct horse battery staple") {
		t.Fatal("expected password to check out")
	}
	if CheckPassword(hash, "wrong password") {
		t.Fatal("expected wrong password to fail")
	}
}

func TestTokenRoundTrip(t *testing.T) {
	tok, err := NewAPIToken()
	if err != nil {
		t.Fatalf("NewAPIToken: %v", err)
	}
	if tok == "" {
		t.Fatal("expected non-empty token")
	}
	h1 := HashToken(tok)
	h2 := HashToken(tok)
	if h1 != h2 {
		t.Fatal("expected deterministic hash")
	}
}

func TestValidUsername(t *testing.T) {
	cases := map[string]bool{
		"alice":     true,
		"alice-bob": true,
		"a":         true,
		"-alice":    false,
		"alice-":    false,
		"Alice":     false,
		"alice_bob": false,
		"":          false,
	}
	for name, want := range cases {
		if got := ValidUsername(name); got != want {
			t.Errorf("ValidUsername(%q) = %v, want %v", name, got, want)
		}
	}
}
