package arch

import (
	"strings"
	"testing"
)

func TestFormatArchDocSummary_NilAndPopulated(t *testing.T) {
	if FormatArchDocSummary(nil) != "(no architecture document)" {
		t.Fatal("nil summary")
	}
	doc := sampleDoc()
	got := FormatArchDocSummary(doc)
	for _, want := range []string{"Overview:", "Task board HTTP API", "Entry Points:", "cmd/server/main.go", "Registration Points", "add-route"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %s", want, got)
		}
	}
}

func TestFormatArchDoc_IncludesLayersConventionsDataFlow(t *testing.T) {
	doc := sampleDoc()
	doc.Layers = []Layer{{Name: "handlers", Description: "HTTP layer", Paths: []string{"internal/handlers"}}}
	doc.Dependencies = ArchDependencies{
		Internal: []string{"cmd/server → internal/handlers"},
		External: []string{"github.com/spf13/cobra (CLI framework)"},
	}
	doc.DataFlow = &DataFlowReport{
		ImportGraph: []ImportEdge{{From: "cmd/server", To: "internal/handlers", Kind: "internal"}},
	}
	md := FormatArchDoc(doc)
	for _, want := range []string{
		"# Project Architecture",
		"## Layers",
		"### handlers",
		"HTTP layer",
		"## Cross-cutting Conventions",
		"no-globals",
		"## Dependencies",
		"github.com/spf13/cobra",
		"## Data Flow",
		"internal/handlers",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("missing %q in formatted doc", want)
		}
	}

	parsed, err := ParseArchDocFromMarkdown(md)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Overview == "" || len(parsed.EntryPoints) == 0 {
		t.Fatalf("markdown parse lost content: %+v", parsed)
	}
	if len(parsed.Layers) == 0 || parsed.Layers[0].Name != "handlers" {
		t.Fatalf("layers: %+v", parsed.Layers)
	}
}

func TestScanProject_PythonNpmAndNoiseDirs(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"package.json":           `{"name":"demo"}`,
		"main.py":                "if __name__ == '__main__':\n    pass\n",
		"requirements.txt":       "flask\n",
		"avatars/ignored.go":     "package ignored\n",
		"pkg.egg-info/PKG-INFO":  "Name: pkg\n",
		"config/app.yaml":        "port: 1\n",
	})
	scan, err := ScanProject(root)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(scan.FileTree, "\n")
	if strings.Contains(joined, "avatars") || strings.Contains(joined, "egg-info") {
		t.Fatalf("noise leaked: %v", scan.FileTree)
	}
	foundPy, foundNPM, foundPip := false, false, false
	for _, dm := range scan.DepManifests {
		switch dm.Kind {
		case "npm":
			foundNPM = true
		case "pip":
			foundPip = true
		}
	}
	for _, ec := range scan.EntryCandidates {
		if strings.HasSuffix(filepathToSlash(ec.Path), "main.py") {
			foundPy = true
			if !strings.Contains(ec.Reason, "Python") {
				t.Fatalf("python entry reason=%q", ec.Reason)
			}
		}
	}
	if !foundPy || !foundNPM || !foundPip {
		t.Fatalf("expected python+npm+pip, manifests=%+v entries=%+v", scan.DepManifests, scan.EntryCandidates)
	}
	if len(scan.ConfigFiles) == 0 {
		t.Fatal("expected config/app.yaml")
	}
}

func filepathToSlash(p string) string {
	return strings.ReplaceAll(p, "\\", "/")
}
