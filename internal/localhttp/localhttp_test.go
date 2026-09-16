package localhttp

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNormalizeAddr_Loopback(t *testing.T) {
	cases := map[string]string{
		"":             "127.0.0.1:5000",
		":5000":        "127.0.0.1:5000",
		"0.0.0.0:5100": "127.0.0.1:5100",
		"[::]:5000":    "127.0.0.1:5000",
		"127.0.0.1:9":  "127.0.0.1:9",
	}
	for in, want := range cases {
		if got := NormalizeAddr(in); got != want {
			t.Errorf("NormalizeAddr(%q)=%q want %q", in, got, want)
		}
	}
}

func TestS7A_MiddlewareAuth(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := Middleware("secret-token", ok)

	unauth := httptest.NewRequest(http.MethodGet, "/api/events", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, unauth)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: got %d", rec.Code)
	}

	wrong := httptest.NewRequest(http.MethodGet, "/api/events", nil)
	wrong.Header.Set(HeaderName, "nope")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, wrong)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: got %d", rec.Code)
	}

	good := httptest.NewRequest(http.MethodGet, "/api/events", nil)
	good.Header.Set(HeaderName, "secret-token")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, good)
	if rec.Code != http.StatusOK {
		t.Fatalf("header token: got %d", rec.Code)
	}

	bearer := httptest.NewRequest(http.MethodGet, "/api/events", nil)
	bearer.Header.Set("Authorization", "Bearer secret-token")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, bearer)
	if rec.Code != http.StatusOK {
		t.Fatalf("bearer: got %d", rec.Code)
	}

	query := httptest.NewRequest(http.MethodGet, "/api/events?token=secret-token", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, query)
	if rec.Code != http.StatusOK {
		t.Fatalf("query token: got %d", rec.Code)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) == 0 || cookies[0].Name != CookieName {
		t.Fatal("expected avatars_token cookie after query auth")
	}

	cookied := httptest.NewRequest(http.MethodPost, "/api/stage", nil)
	cookied.AddCookie(cookies[0])
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, cookied)
	if rec.Code != http.StatusOK {
		t.Fatalf("cookie token: got %d", rec.Code)
	}
}
