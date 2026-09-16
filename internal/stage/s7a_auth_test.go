package stage

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"avatars/internal/events"
	"avatars/internal/llm"
	"avatars/internal/localhttp"
)

func TestS7A_StageAuth(t *testing.T) {
	store := events.NewStore()
	store.Append(events.Envelope{EventID: "evt-1", Sequence: 1, RunID: "run-1", TaskID: "task-1", Type: "run.started", EmittedAt: time.Now().UTC(), Payload: map[string]any{"input": "demo"}})
	server, err := NewServer(store, nil, llm.ProviderStatus{}, "")
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	server = server.WithAuthToken("stage-secret")
	h := server.Handler()

	unauth := httptest.NewRequest(http.MethodGet, "/api/events", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, unauth)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: got %d", rec.Code)
	}

	wrong := httptest.NewRequest(http.MethodGet, "/api/operator", nil)
	wrong.Header.Set(localhttp.HeaderName, "nope")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, wrong)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: got %d", rec.Code)
	}

	ok := httptest.NewRequest(http.MethodGet, "/api/events", nil)
	ok.Header.Set(localhttp.HeaderName, "stage-secret")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, ok)
	if rec.Code != http.StatusOK {
		t.Fatalf("authed events: got %d", rec.Code)
	}
	var history []events.Envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &history); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("expected 1 event, got %d", len(history))
	}
}
