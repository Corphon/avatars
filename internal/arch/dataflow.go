package arch

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"avatars/internal/projectfiles"
)

// ImportEdge describes one directed dependency between packages.
type ImportEdge struct {
	From string `json:"from"` // importing package path
	To   string `json:"to"`   // imported package path
	Kind string `json:"kind"` // "internal" or "external"
}

// DataFlowReport captures inter-package dependencies and key data structures.
type DataFlowReport struct {
	ImportGraph  []ImportEdge `json:"import_graph"`
	KeyTypes     []KeyType    `json:"key_types"`     // types that cross package boundaries
	ExternalDeps []ExtDep     `json:"external_deps"` // third-party dependencies with roles
}

// KeyType is a type definition that crosses package boundaries.
type KeyType struct {
	Name    string `json:"name"`
	Package string `json:"package"`
	Kind    string `json:"kind"` // "struct", "interface", "func"
}

// ExtDep is an external dependency with its inferred role.
type ExtDep struct {
	Path string `json:"path"`
	Role string `json:"role"` // e.g. "HTTP router", "database driver", "CLI framework"
}

// AnalyzeDataFlow scans a project root and builds a DataFlowReport.
// Supports Go (go/parser) and Python (regex). Returns nil if no source files found.
func AnalyzeDataFlow(root string) *DataFlowReport {
	report := &DataFlowReport{}
	hasGo := false
	hasPy := false

	// Check what languages are present.
	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			if info != nil && shouldSkipDir(info.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext == ".go" {
			hasGo = true
		}
		if ext == ".py" {
			hasPy = true
		}
		return nil
	})

	if hasGo {
		analyzeGoImports(root, report)
	}
	if hasPy {
		analyzePythonImports(root, report)
	}

	if len(report.ImportGraph) == 0 && len(report.KeyTypes) == 0 {
		return nil
	}

	// Deduplicate, drop self-loops / stdlib noise, sort.
	report.ImportGraph = dedupeImportEdges(filterImportNoise(report.ImportGraph))
	report.KeyTypes = dedupeKeyTypes(report.KeyTypes)
	report.ExternalDeps = dedupeExtDeps(filterExtDepNoise(report.ExternalDeps))

	return report
}

// filterImportNoise drops self-loops (handlers→handlers) from the graph.
func filterImportNoise(edges []ImportEdge) []ImportEdge {
	var out []ImportEdge
	for _, e := range edges {
		if e.From == e.To {
			continue
		}
		out = append(out, e)
	}
	return out
}

// filterExtDepNoise drops residual stdlib paths that slipped into ExternalDeps.
// Go stdlib is slash-separated (net/http) or would already have been tagged
// stdlib by analyzeGoImports. Do not treat Python third-party top-level names
// (flask, requests) as Go stdlib just because they lack a dot.
func filterExtDepNoise(deps []ExtDep) []ExtDep {
	var out []ExtDep
	for _, d := range deps {
		if isPythonStdlib(d.Path) {
			continue
		}
		if strings.Contains(d.Path, "/") && isGoStdlib(d.Path) {
			continue
		}
		out = append(out, d)
	}
	return out
}

// isGoStdlib reports Go standard-library import paths.
// Convention: first path element has no dot (fmt, net/http, encoding/json).
func isGoStdlib(importPath string) bool {
	if importPath == "" || strings.HasPrefix(importPath, ".") {
		return false
	}
	first := strings.Split(importPath, "/")[0]
	return !strings.Contains(first, ".")
}

// shouldSkipDir returns true for well-known noise directories.
func shouldSkipDir(name string) bool {
	if projectfiles.ShouldSkipWalkDir(name) {
		return true
	}
	switch name {
	case ".idea", ".vscode":
		return true
	}
	return strings.HasPrefix(name, ".")
}

// analyzeGoImports parses Go source files and extracts imports.
func analyzeGoImports(root string, report *DataFlowReport) {
	fset := token.NewFileSet()
	modulePath := readModulePath(root)
	seenPkgs := make(map[string]bool)

	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			if info != nil && shouldSkipDir(info.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}

		f, parseErr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if parseErr != nil {
			return nil
		}

		// Determine this file's package path.
		pkgDir := filepath.Dir(path)
		relPkg, _ := filepath.Rel(root, pkgDir)
		relPkg = filepath.ToSlash(relPkg)
		if relPkg == "." {
			relPkg = "root"
		}
		seenPkgs[relPkg] = true

		for _, imp := range f.Imports {
			impPath := strings.Trim(imp.Path.Value, "\"")
			kind := "external"
			to := impPath

			if modulePath != "" && strings.HasPrefix(impPath, modulePath) {
				kind = "internal"
				// Show relative path within the module.
				to = strings.TrimPrefix(impPath, modulePath)
				to = strings.TrimPrefix(to, "/")
				if to == "" {
					to = "root"
				}
			} else if isGoStdlib(impPath) {
				kind = "stdlib"
			}

			report.ImportGraph = append(report.ImportGraph, ImportEdge{
				From: relPkg,
				To:   to,
				Kind: kind,
			})

			if kind == "external" {
				report.ExternalDeps = append(report.ExternalDeps, ExtDep{
					Path: impPath,
					Role: inferGoDepRole(impPath),
				})
			}
		}

		// Extract exported types/functions for key type detection.
		for _, decl := range f.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.TYPE {
				continue
			}
			for _, spec := range gen.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || !ts.Name.IsExported() {
					continue
				}
				kind := "struct"
				if _, isInterface := ts.Type.(*ast.InterfaceType); isInterface {
					kind = "interface"
				}
				report.KeyTypes = append(report.KeyTypes, KeyType{
					Name:    ts.Name.Name,
					Package: relPkg,
					Kind:    kind,
				})
			}
		}
		return nil
	})
	_ = seenPkgs
}

// analyzePythonImports extracts Python imports using regex.
func analyzePythonImports(root string, report *DataFlowReport) {
	importRe := regexp.MustCompile(`^\s*(?:from\s+(\S+)\s+import|import\s+(\S+))`)
	rootAbs, _ := filepath.Abs(root)

	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			if info != nil && shouldSkipDir(info.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".py") {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		// Determine this file's package.
		pkgDir := filepath.Dir(path)
		relPkg, _ := filepath.Rel(rootAbs, pkgDir)
		relPkg = filepath.ToSlash(relPkg)
		if relPkg == "." {
			relPkg = "root"
		}

		lines := strings.Split(string(data), "\n")
		for _, line := range lines {
			matches := importRe.FindStringSubmatch(line)
			if matches == nil {
				continue
			}
			impPath := matches[1]
			if impPath == "" {
				impPath = matches[2]
			}
			if impPath == "" || strings.HasPrefix(impPath, ".") {
				continue // skip relative imports
			}

			kind := "external"
			// Standard library modules
			if isPythonStdlib(impPath) {
				kind = "stdlib"
			}

			report.ImportGraph = append(report.ImportGraph, ImportEdge{
				From: relPkg,
				To:   impPath,
				Kind: kind,
			})

			if kind == "external" {
				report.ExternalDeps = append(report.ExternalDeps, ExtDep{
					Path: impPath,
					Role: inferPyDepRole(impPath),
				})
			}
		}
		return nil
	})
}

// readModulePath extracts the Go module path from go.mod.
func readModulePath(root string) string {
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return ""
	}
	re := regexp.MustCompile(`^\s*module\s+(\S+)`)
	for _, line := range strings.Split(string(data), "\n") {
		if m := re.FindStringSubmatch(line); m != nil {
			return m[1]
		}
	}
	return ""
}

// inferGoDepRole guesses the role of a Go dependency from its path.
func inferGoDepRole(path string) string {
	switch {
	case strings.Contains(path, "gin-gonic"):
		return "HTTP router"
	case strings.Contains(path, "gorilla/mux"):
		return "HTTP router"
	case strings.Contains(path, "chi"):
		return "HTTP router"
	case strings.Contains(path, "echo"):
		return "HTTP router"
	case strings.Contains(path, "fiber"):
		return "HTTP router"
	case strings.Contains(path, "sql"):
		return "database driver"
	case strings.Contains(path, "pgx"):
		return "PostgreSQL driver"
	case strings.Contains(path, "mongo"):
		return "MongoDB driver"
	case strings.Contains(path, "redis"):
		return "cache"
	case strings.Contains(path, "cobra"):
		return "CLI framework"
	case strings.Contains(path, "spf13/viper"):
		return "configuration"
	case strings.Contains(path, "zap"):
		return "logging"
	case strings.Contains(path, "logrus"):
		return "logging"
	case strings.Contains(path, "testify"):
		return "testing"
	case strings.Contains(path, "yaml"):
		return "YAML parsing"
	case strings.Contains(path, "json"):
		return "JSON handling"
	case strings.Contains(path, "protobuf"):
		return "serialization"
	case strings.Contains(path, "grpc"):
		return "RPC framework"
	case strings.Contains(path, "jwt"):
		return "authentication"
	case strings.Contains(path, "oauth"):
		return "authentication"
	default:
		return "utility"
	}
}

// isPythonStdlib returns true if the module is part of Python's standard library.
func isPythonStdlib(module string) bool {
	top := strings.Split(module, ".")[0]
	switch top {
	case "os", "sys", "json", "csv", "re", "math", "statistics",
		"datetime", "time", "collections", "itertools", "functools",
		"io", "pathlib", "argparse", "logging", "unittest", "subprocess",
		"hashlib", "base64", "http", "urllib", "xml", "html",
		"threading", "multiprocessing", "asyncio", "socket", "ssl",
		"typing", "dataclasses", "enum", "abc", "copy", "textwrap",
		"tempfile", "shutil", "glob", "fnmatch", "random", "secrets",
		"uuid", "decimal", "fractions", "string", "struct", "traceback",
		"warnings", "contextlib", "operator", "weakref", "gc", "inspect",
		"ast", "dis", "codecs", "locale", "gettext", "configparser",
		"sqlite3", "email", "zipfile", "tarfile", "gzip", "bz2", "lzma":
		return true
	}
	return false
}

// inferPyDepRole guesses the role of a Python dependency.
func inferPyDepRole(path string) string {
	switch {
	case strings.Contains(path, "flask"):
		return "web framework"
	case strings.Contains(path, "django"):
		return "web framework"
	case strings.Contains(path, "fastapi"):
		return "web framework"
	case strings.Contains(path, "requests"):
		return "HTTP client"
	case strings.Contains(path, "pandas"):
		return "data analysis"
	case strings.Contains(path, "numpy"):
		return "numerical computing"
	case strings.Contains(path, "pytest"):
		return "testing"
	case strings.Contains(path, "sqlalchemy"):
		return "ORM"
	case strings.Contains(path, "pydantic"):
		return "data validation"
	case strings.Contains(path, "click"):
		return "CLI framework"
	case strings.Contains(path, "rich"):
		return "terminal UI"
	case strings.Contains(path, "celery"):
		return "task queue"
	case strings.Contains(path, "pillow"):
		return "image processing"
	case strings.Contains(path, "matplotlib"):
		return "plotting"
	case strings.Contains(path, "scipy"):
		return "scientific computing"
	default:
		return "utility"
	}
}

func dedupeImportEdges(edges []ImportEdge) []ImportEdge {
	seen := make(map[string]bool)
	var out []ImportEdge
	for _, e := range edges {
		key := e.From + "→" + e.To
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].From != out[j].From {
			return out[i].From < out[j].From
		}
		return out[i].To < out[j].To
	})
	return out
}

func dedupeKeyTypes(types []KeyType) []KeyType {
	seen := make(map[string]bool)
	var out []KeyType
	for _, t := range types {
		key := t.Package + "." + t.Name
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Package != out[j].Package {
			return out[i].Package < out[j].Package
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func dedupeExtDeps(deps []ExtDep) []ExtDep {
	seen := make(map[string]bool)
	var out []ExtDep
	for _, d := range deps {
		if seen[d.Path] {
			continue
		}
		seen[d.Path] = true
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// FormatDataFlowSection renders a DataFlowReport as a markdown section
// for inclusion in architecture.md. Called by FormatArchDoc.
func FormatDataFlowSection(report *DataFlowReport) string {
	if report == nil {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("## Data Flow\n\n")

	if len(report.ImportGraph) > 0 {
		var internal, external []ImportEdge
		for _, e := range report.ImportGraph {
			switch e.Kind {
			case "internal":
				if e.From != e.To {
					internal = append(internal, e)
				}
			case "external":
				// Skip stdlib (and any residual self-loops).
				external = append(external, e)
			}
		}
		if len(internal) > 0 {
			sb.WriteString("### Internal Dependencies\n\n")
			sb.WriteString("| From | To |\n|------|----|\n")
			for _, e := range internal {
				sb.WriteString("| " + e.From + " | " + e.To + " |\n")
			}
			sb.WriteString("\n")
		}
		if len(external) > 0 {
			sb.WriteString("### External Imports\n\n")
			sb.WriteString("| Package | Import |\n|---------|--------|\n")
			shown := make(map[string]bool)
			for _, e := range external {
				key := e.From + "→" + e.To
				if shown[key] {
					continue
				}
				shown[key] = true
				sb.WriteString("| " + e.From + " | " + e.To + " |\n")
			}
			sb.WriteString("\n")
		}
	}
	if len(report.KeyTypes) > 0 {
		sb.WriteString("### Key Types\n\n")
		sb.WriteString("| Type | Package | Kind |\n|------|---------|------|\n")
		for _, t := range report.KeyTypes {
			sb.WriteString("| " + t.Name + " | " + t.Package + " | " + t.Kind + " |\n")
		}
		sb.WriteString("\n")
	}
	if len(report.ExternalDeps) > 0 {
		sb.WriteString("### External Dependencies\n\n")
		sb.WriteString("| Dependency | Role |\n|------------|------|\n")
		for _, d := range report.ExternalDeps {
			sb.WriteString("| " + d.Path + " | " + d.Role + " |\n")
		}
		sb.WriteString("\n")
	}
	return sb.String()
}
