package config

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// TokenExpiry reads the expiry embedded in a Resy auth token. Resy issues a
// JWT, so the expiry is readable locally without spending a request. The second
// return is false when the token carries no readable expiry, in which case the
// caller should just try the request.
func TokenExpiry(token string) (time.Time, bool) {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 3 {
		return time.Time{}, false
	}

	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return time.Time{}, false
	}

	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp <= 0 {
		return time.Time{}, false
	}
	return time.Unix(claims.Exp, 0), true
}

// CheckTokenValidAt reports whether the auth token is still usable at the given
// moment. Passing the scheduled snipe time catches a token that is valid now
// but will have lapsed by the time the run actually fires.
func CheckTokenValidAt(token string, when time.Time) error {
	expiry, ok := TokenExpiry(token)
	if !ok {
		return nil
	}
	if when.Before(expiry) {
		return nil
	}
	if when.After(time.Now().Add(time.Minute)) {
		return fmt.Errorf(
			"RESY_AUTH_TOKEN expires %s, before the scheduled run at %s.\n%s",
			expiry.Local().Format(time.RFC1123), when.Local().Format(time.RFC1123), refreshHint)
	}
	return fmt.Errorf("RESY_AUTH_TOKEN expired %s.\n%s", expiry.Local().Format(time.RFC1123), refreshHint)
}

const refreshHint = `Refresh it:
  1. Log in at https://resy.com in your browser
  2. Open DevTools -> Network, then click any restaurant
  3. Pick a request to api.resy.com and copy the "x-resy-auth-token" request header
  4. Put the new value in .env, then: set -a && source .env && set +a`
