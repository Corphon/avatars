package arch

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInferRegistrationPoints_PythonFlask(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "app.py")
	if err := os.WriteFile(p, []byte("from flask import Flask\napp = Flask(__name__)\n@app.get(\"/health\")\ndef health():\n    return \"ok\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	scan, err := ScanProject(dir)
	if err != nil {
		t.Fatal(err)
	}
	rps := InferRegistrationPoints(dir, scan)
	found := false
	for _, rp := range rps {
		if rp.ChangeType == "add-route" && len(rp.Files) > 0 {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected python add-route, got %+v", rps)
	}
}

func TestInferRegistrationPoints_NilScan(t *testing.T) {
	if InferRegistrationPoints(".", nil) != nil {
		t.Fatal("nil scan should return nil")
	}
}

func TestInferRegistrationPoints_GoRoutes(t *testing.T) {
	dir := t.TempDir()
	mustWrite := func(rel, body string) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("cmd/server/main.go", `package main
func main() {
  http.HandleFunc("/health", health)
  http.HandleFunc("/tasks", tasks)
  mux.Handle("/api/", api)
}
`)
	mustWrite("internal/middleware/auth.go", `package middleware
func Wrap(next http.Handler) http.Handler { r.Use(auth); return next }
`)

	scan, err := ScanProject(dir)
	if err != nil {
		t.Fatal(err)
	}
	rps := InferRegistrationPoints(dir, scan)
	if len(rps) == 0 {
		t.Fatal("expected inferred registration points")
	}
	foundRoute := false
	for _, rp := range rps {
		if rp.ChangeType == "add-route" && len(rp.Files) > 0 {
			foundRoute = true
		}
	}
	if !foundRoute {
		t.Fatalf("expected add-route, got %+v", rps)
	}
}

func TestMergeRegPoints_FillsEmpty(t *testing.T) {
	existing := []RegistrationPoint{{ChangeType: "add-route", Files: nil}}
	inferred := []RegistrationPoint{{
		ChangeType: "add-route",
		Files:      []RegFile{{Path: "cmd/server/main.go", ChangeHint: "register"}},
	}}
	merged := MergeRegPoints(existing, inferred)
	if len(merged) != 1 || len(merged[0].Files) != 1 {
		t.Fatalf("got %+v", merged)
	}
}

func TestNeedsLLMEnrich(t *testing.T) {
	doc := NewArchDoc("t", ".", nil)
	doc.Overview = "auto-updated after build."
	if !NeedsLLMEnrich(doc) {
		t.Fatal("weak overview should need enrich")
	}
	doc.Overview = "Task board HTTP API with SQLite persistence and JWT auth."
	doc.RegPoints = []RegistrationPoint{{
		ChangeType: "add-route",
		Files:      []RegFile{{Path: "main.go", ChangeHint: "register"}},
	}}
	if NeedsLLMEnrich(doc) {
		t.Fatal("good overview+regpoints should skip enrich")
	}
}

func TestRefreshMergesHeuristicRegPoints(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "main.go")
	_ = os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/x\n\ngo 1.22\n"), 0o644)
	_ = os.WriteFile(p, []byte("package main\nfunc main() { http.HandleFunc(\"/x\", h) }\n"), 0o644)

	doc := NewArchDoc("t", dir, nil)
	doc.Overview = "Tiny demo HTTP service."
	_ = RefreshArchDocFromProject(dir, doc, RefreshOptions{PreserveOverview: true, HealthKnown: false})
	if !hasUsefulRegPoints(doc) {
		t.Fatalf("expected heuristic regpoints, status=%s rps=%+v", doc.Meta.Status, doc.RegPoints)
	}
}
