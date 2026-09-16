package runtime

import (
	"strings"
	"testing"
)

func TestDropModulePackageConflicts(t *testing.T) {
	kept, dropped := dropModulePackageConflicts([]builderCodeFile{
		{Path: "app/models.py", Content: "class Room: pass\n"},
		{Path: "app/models/__init__.py", Content: "# models package\n"},
		{Path: "app/auth.py", Content: "def api_key_auth(): pass\n"},
		{Path: "app/auth/__init__.py", Content: "# auth package\n"},
		{Path: "app/main.py", Content: "app = None\n"},
	})
	paths := map[string]bool{}
	for _, f := range kept {
		paths[f.Path] = true
	}
	if !paths["app/models.py"] || !paths["app/auth.py"] || !paths["app/main.py"] {
		t.Fatalf("modules should be kept: %v", paths)
	}
	if paths["app/models/__init__.py"] || paths["app/auth/__init__.py"] {
		t.Fatalf("package inits should be dropped: kept=%v dropped=%v", paths, dropped)
	}
}

func TestAuthLooksFailOpen_DefaultDevKey(t *testing.T) {
	py := `keys_env = os.environ.get("API_KEYS", "dev-key")`
	if !authLooksFailOpen(strings.ToLower(py)) {
		t.Fatal("environ.get with default dev-key must count as fail-open")
	}
	strict := `keys_env = os.environ.get("SLOTBOOK_API_KEY")
if not keys_env:
    raise HTTPException(401)`
	if authLooksFailOpen(strings.ToLower(strict)) {
		t.Fatal("strict getenv without default must not count as fail-open")
	}
}

func TestAuthLooksFailOpen_EmptyConfiguredKeyCompare(t *testing.T) {
	// Q1 / clipvault: key != s.apiKey allows when both are "".
	bad := `
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("X-API-Key")
		if key != s.apiKey {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		next(w, r)
	}
}`
	if !authLooksFailOpen(strings.ToLower(bad)) {
		t.Fatal("empty-configured-key compare must count as fail-open")
	}
	good := `
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.apiKey == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		key := r.Header.Get("X-API-Key")
		if key != s.apiKey {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		next(w, r)
	}
}`
	if authLooksFailOpen(strings.ToLower(good)) {
		t.Fatal("reject-empty-configured-key before compare must not count as fail-open")
	}
}

func TestExtractPromptSpecGaps_NestedRouteAndFields(t *testing.T) {
	prompt := `POST /rooms/{id}/slots → 201
SQLite rooms: id, name, capacity, created_at
slots: starts_at, ends_at, booker_name, note
`
	code := `POST /slots
room_id start_time end_time
`
	gaps := extractPromptSpecGaps(prompt, code)
	joined := strings.Join(gaps, ";")
	for _, field := range []string{"capacity", "booker_name", "starts_at"} {
		if !strings.Contains(joined, field) {
			t.Fatalf("expected schema field %s gap, got %v", field, gaps)
		}
	}
}

func TestIsImportLayoutFailure(t *testing.T) {
	if !isImportLayoutFailure(`ImportError: cannot import name 'api_key_auth' from 'app.auth'`) {
		t.Fatal("expected import layout failure")
	}
	if isImportLayoutFailure(`assertion failed: want 200`) {
		t.Fatal("test assertion must not be import layout")
	}
}
