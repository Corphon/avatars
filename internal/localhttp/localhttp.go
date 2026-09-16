// Package localhttp provides localhost bind normalization and shared-token
// gates for avatars HTTP surfaces (stage, gallery, MCP).
package localhttp

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
)

const (
	StageTokenEnv = "AVATARS_STAGE_TOKEN"
	MCPTokenEnv   = "AVATARS_MCP_TOKEN"
	HeaderName    = "X-Avatars-Token"
	CookieName    = "avatars_token"
)

// NormalizeAddr forces a loopback host. Bare ":port", 0.0.0.0, and :: bind
// 127.0.0.1 instead of all interfaces.
func NormalizeAddr(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "127.0.0.1:5000"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		if strings.HasPrefix(addr, ":") {
			return net.JoinHostPort("127.0.0.1", strings.TrimPrefix(addr, ":"))
		}
		return "127.0.0.1:5000"
	}
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

// ResolveToken returns the first non-empty env value, or a random session token.
func ResolveToken(envKeys ...string) string {
	for _, key := range envKeys {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "avatars-dev-token"
	}
	return hex.EncodeToString(buf[:])
}

// TokenFromRequest reads X-Avatars-Token, Authorization Bearer, or ?token=.
func TokenFromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	if h := strings.TrimSpace(r.Header.Get(HeaderName)); h != "" {
		return h
	}
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(auth) >= 7 && strings.EqualFold(auth[:7], "bearer ") {
		return strings.TrimSpace(auth[7:])
	}
	if r.URL != nil {
		if q := strings.TrimSpace(r.URL.Query().Get("token")); q != "" {
			return q
		}
	}
	if c, err := r.Cookie(CookieName); err == nil {
		return strings.TrimSpace(c.Value)
	}
	return ""
}

// Authorize reports whether the request presents the expected token.
func Authorize(r *http.Request, token string) bool {
	token = strings.TrimSpace(token)
	if token == "" {
		return false
	}
	got := TokenFromRequest(r)
	return got != "" && got == token
}

// Middleware rejects requests that do not present the shared token.
func Middleware(token string, next http.Handler) http.Handler {
	token = strings.TrimSpace(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if q := TokenFromRequest(r); q != "" && q == token && r.URL != nil && r.URL.Query().Get("token") != "" {
			http.SetCookie(w, &http.Cookie{
				Name:     CookieName,
				Value:    q,
				Path:     "/",
				HttpOnly: true,
				SameSite: http.SameSiteStrictMode,
			})
		}
		if !Authorize(r, token) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="avatars"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// LogListen prints bind address and how to pass the token.
func LogListen(prefix, addr, token string) {
	fmt.Fprintf(os.Stderr, "%s listening on http://%s (token required via %s, Authorization: Bearer, or ?token=)\n", prefix, addr, HeaderName)
	if strings.TrimSpace(os.Getenv(StageTokenEnv)) == "" && strings.TrimSpace(os.Getenv(MCPTokenEnv)) == "" {
		fmt.Fprintf(os.Stderr, "%s session token: %s\n", prefix, token)
	}
}
