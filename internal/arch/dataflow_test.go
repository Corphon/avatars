package arch

import (
	"strings"
	"testing"
)

func TestAnalyzeDataFlow_GoInternalAndExternal(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"go.mod": "module example.com/demo\n\ngo 1.22\n",
		"cmd/server/main.go": `package main
import (
	"fmt"
	"net/http"
	"example.com/demo/internal/handlers"
	"github.com/spf13/cobra"
)
func main() { fmt.Println(http.StatusOK); _ = handlers.X; _ = cobra.Command{} }
`,
		"internal/handlers/h.go": "package handlers\nconst X = 1\n",
		"vendor/ignored.go":      "package ignored\n",
	})

	report := AnalyzeDataFlow(root)
	if report == nil {
		t.Fatal("expected dataflow report")
	}

	var sawInternal, sawCobra, sawStdlibEdge bool
	for _, e := range report.ImportGraph {
		if e.Kind == "internal" && strings.Contains(e.To, "handlers") {
			sawInternal = true
		}
		if e.Kind == "external" && strings.Contains(e.To, "cobra") {
			sawCobra = true
		}
		if e.Kind == "stdlib" && (e.To == "fmt" || e.To == "net/http") {
			sawStdlibEdge = true
		}
		if strings.Contains(e.From, "vendor") || strings.Contains(e.To, "vendor") {
			t.Fatalf("vendor leaked into import graph: %+v", e)
		}
	}
	if !sawInternal {
		t.Fatalf("expected internal handlers edge, graph=%+v", report.ImportGraph)
	}
	if !sawCobra {
		t.Fatalf("expected cobra external edge, graph=%+v", report.ImportGraph)
	}
	if !sawStdlibEdge {
		t.Fatalf("expected stdlib edges in graph, graph=%+v", report.ImportGraph)
	}

	foundCobraRole := false
	for _, d := range report.ExternalDeps {
		if strings.Contains(d.Path, "cobra") {
			foundCobraRole = true
			if d.Role != "CLI framework" {
				t.Fatalf("cobra role=%q", d.Role)
			}
		}
		if isGoStdlib(d.Path) {
			t.Fatalf("stdlib leaked into ExternalDeps: %+v", d)
		}
	}
	if !foundCobraRole {
		t.Fatalf("expected cobra in ExternalDeps, got %+v", report.ExternalDeps)
	}
}

func TestAnalyzeDataFlow_PythonExternalVsStdlib(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"app/main.py": "import os\nfrom flask import Flask\nimport requests\n",
	})
	report := AnalyzeDataFlow(root)
	if report == nil {
		t.Fatal("expected python dataflow report")
	}
	var sawFlask, sawOS bool
	for _, e := range report.ImportGraph {
		if e.To == "flask" && e.Kind == "external" {
			sawFlask = true
		}
		if e.To == "os" && e.Kind == "stdlib" {
			sawOS = true
		}
	}
	if !sawFlask || !sawOS {
		t.Fatalf("expected flask external + os stdlib, graph=%+v", report.ImportGraph)
	}
	foundFlask, foundRequests := false, false
	for _, d := range report.ExternalDeps {
		if d.Path == "os" || isPythonStdlib(d.Path) {
			t.Fatalf("python stdlib leaked into ExternalDeps: %+v", d)
		}
		if d.Path == "flask" {
			foundFlask = true
			if d.Role != "web framework" {
				t.Fatalf("flask role=%q", d.Role)
			}
		}
		if d.Path == "requests" {
			foundRequests = true
			if d.Role != "HTTP client" {
				t.Fatalf("requests role=%q", d.Role)
			}
		}
	}
	if !foundFlask || !foundRequests {
		t.Fatalf("expected flask+requests in ExternalDeps, got %+v", report.ExternalDeps)
	}
}

func TestAnalyzeDataFlow_EmptyProjectIsNil(t *testing.T) {
	if got := AnalyzeDataFlow(t.TempDir()); got != nil {
		t.Fatalf("empty project should be nil, got %+v", got)
	}
}

func TestFormatDataFlowSection_RendersTables(t *testing.T) {
	if FormatDataFlowSection(nil) != "" {
		t.Fatal("nil report should be empty")
	}
	md := FormatDataFlowSection(&DataFlowReport{
		ImportGraph: []ImportEdge{
			{From: "cmd/server", To: "internal/handlers", Kind: "internal"},
			{From: "cmd/server", To: "cmd/server", Kind: "internal"},
			{From: "cmd/server", To: "github.com/spf13/cobra", Kind: "external"},
		},
		KeyTypes:     []KeyType{{Name: "Engine", Package: "runtime", Kind: "struct"}},
		ExternalDeps: []ExtDep{{Path: "github.com/spf13/cobra", Role: "CLI framework"}},
	})
	for _, want := range []string{
		"## Data Flow",
		"### Internal Dependencies",
		"cmd/server",
		"internal/handlers",
		"github.com/spf13/cobra",
		"### Key Types",
		"Engine",
		"CLI framework",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("missing %q in:\n%s", want, md)
		}
	}
	if strings.Contains(md, "| cmd/server | cmd/server |") {
		t.Fatal("self-loop should not render in internal table")
	}
}

func TestIsPythonStdlibAndFilterExtDepNoise(t *testing.T) {
	if !isPythonStdlib("os.path") || !isPythonStdlib("json") || isPythonStdlib("flask") {
		t.Fatal("python stdlib detection")
	}
	out := filterExtDepNoise([]ExtDep{
		{Path: "net/http"},
		{Path: "os"},
		{Path: "github.com/foo/bar"},
		{Path: "flask"},
	})
	if len(out) != 2 {
		t.Fatalf("expected github+flask, got %+v", out)
	}
	got := map[string]bool{}
	for _, d := range out {
		got[d.Path] = true
	}
	if !got["github.com/foo/bar"] || !got["flask"] {
		t.Fatalf("expected github+flask, got %+v", out)
	}
}

func TestInferGoDepRole_KnownLibraries(t *testing.T) {
	if inferGoDepRole("github.com/gin-gonic/gin") != "HTTP router" {
		t.Fatal("gin")
	}
	if inferGoDepRole("modernc.org/sqlite") != "database driver" {
		t.Fatal("sqlite")
	}
	if inferGoDepRole("example.com/unknown") != "utility" {
		t.Fatal("default role")
	}
}
