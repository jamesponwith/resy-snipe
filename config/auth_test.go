package config

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"
)

// jwtWithExp builds a token whose payload carries the given expiry.
func jwtWithExp(exp int64) string {
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, exp)))
	return "header." + payload + ".signature"
}

func TestTokenExpiryReadsExpClaim(t *testing.T) {
	want := time.Now().Add(48 * time.Hour).Truncate(time.Second)

	got, ok := TokenExpiry(jwtWithExp(want.Unix()))
	if !ok {
		t.Fatal("TokenExpiry() ok = false, want true")
	}
	if !got.Equal(want) {
		t.Fatalf("TokenExpiry() = %s, want %s", got, want)
	}
}

func TestTokenExpiryIgnoresNonJWT(t *testing.T) {
	for _, token := range []string{"", "not-a-jwt", "two.parts", "a.!!!notbase64!!!.c", "a." + base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"x"}`)) + ".c"} {
		if _, ok := TokenExpiry(token); ok {
			t.Fatalf("TokenExpiry(%q) ok = true, want false", token)
		}
	}
}

func TestCheckTokenValidAtAcceptsLiveToken(t *testing.T) {
	token := jwtWithExp(time.Now().Add(24 * time.Hour).Unix())

	if err := CheckTokenValidAt(token, time.Now()); err != nil {
		t.Fatalf("CheckTokenValidAt() error = %v, want nil", err)
	}
}

func TestCheckTokenValidAtRejectsExpiredToken(t *testing.T) {
	token := jwtWithExp(time.Now().Add(-24 * time.Hour).Unix())

	err := CheckTokenValidAt(token, time.Now())
	if err == nil {
		t.Fatal("CheckTokenValidAt() error = nil, want an expiry error")
	}
	if !strings.Contains(err.Error(), "expired") {
		t.Fatalf("error %q should say the token expired", err)
	}
}

// A token that works now but lapses before the scheduled drop is the failure
// worth catching early, since the run would otherwise die at midnight.
func TestCheckTokenValidAtRejectsTokenThatLapsesBeforeTheRun(t *testing.T) {
	token := jwtWithExp(time.Now().Add(2 * time.Hour).Unix())
	scheduled := time.Now().Add(72 * time.Hour)

	if err := CheckTokenValidAt(token, time.Now()); err != nil {
		t.Fatalf("token should be valid right now, got %v", err)
	}

	err := CheckTokenValidAt(token, scheduled)
	if err == nil {
		t.Fatal("CheckTokenValidAt() error = nil, want a pre-run expiry error")
	}
	if !strings.Contains(err.Error(), "before the scheduled run") {
		t.Fatalf("error %q should explain it lapses before the run", err)
	}
}

// Without a readable expiry the tool should not block the run.
func TestCheckTokenValidAtAllowsOpaqueToken(t *testing.T) {
	if err := CheckTokenValidAt("opaque-token", time.Now()); err != nil {
		t.Fatalf("CheckTokenValidAt() error = %v, want nil for an opaque token", err)
	}
}
