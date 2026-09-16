package arch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"avatars/internal/llm"
)

// RefreshOptions controls post-build / ensure refresh behavior.
type RefreshOptions struct {
	// PreserveOverview keeps existing Overview when non-empty and not a prompt dump.
	PreserveOverview bool
	// TaskDesc is used only when Overview must be (re)seeded.
	TaskDesc string
	// ProjectHealthy / HealthKnown feed ApplyAutoStatus.
	ProjectHealthy bool
	HealthKnown    bool
	// LLM + Ctx enable R3-9C optional enrichment (nil client = heuristic only).
	LLM llm.Client
	Ctx context.Context
}

// RefreshArchDocFromProject rewrites scan-derived sections from disk.
// Language-agnostic: ProjectScan + AnalyzeDataFlow + heuristic RegPoints.
// Optional LLM enrich fills Overview/RegPoints when still weak (never blocks).
func RefreshArchDocFromProject(root string, doc *ArchDoc, opts RefreshOptions) []QualityIssue {
	if doc == nil {
		doc = NewArchDoc("workflow-auto-refresh", root, nil)
	}
	scan, err := ScanProject(root)
	if err != nil || scan == nil {
		scan = NewScanForInit(root)
	}

	// Overview: preserve good text; never re-paste a prompt.
	if opts.PreserveOverview && strings.TrimSpace(doc.Overview) != "" && !OverviewLooksLikePrompt(doc.Overview) {
		// keep
	} else if strings.TrimSpace(doc.Overview) == "" || OverviewLooksLikePrompt(doc.Overview) {
		seed := opts.TaskDesc
		if seed == "" {
			seed = doc.Overview
		}
		doc.Overview = SanitizeTaskOverview(seed)
	}

	doc.EntryPoints = buildEntryPoints(root, scan)
	doc.Layers = buildLayersFromScan(scan)
	doc.DataFlow = AnalyzeDataFlow(root)
	doc.Dependencies = depsFromDataFlow(doc.DataFlow)

	// R3-9C heuristic: always merge inferred registration sites.
	inferred := InferRegistrationPoints(root, scan)
	doc.RegPoints = MergeRegPoints(doc.RegPoints, inferred)

	// R3-9C LLM: only when still weak; failure leaves heuristic/scan data intact.
	// Prefer Background if parent is already cancelled (post-run defer).
	if opts.LLM != nil {
		ctx := opts.Ctx
		if ctx == nil || ctx.Err() != nil {
			ctx = context.Background()
		}
		_ = EnrichArchWithLLM(ctx, opts.LLM, root, doc, scan)
	}

	doc.Meta.LastModifiedFiles = scan.KeyFiles()
	doc.Meta.FileFingerprint = ComputeFingerprint(root, doc.Meta.LastModifiedFiles)
	if doc.Meta.Source == "" {
		doc.Meta.Source = "workflow-auto-refresh"
	}

	return ApplyAutoStatus(doc, opts.ProjectHealthy, opts.HealthKnown)
}

// buildEntryPoints fills Entry Points from scan candidates with content-aware Kind.
func buildEntryPoints(root string, scan *ProjectScan) []EntryPoint {
	if scan == nil {
		return nil
	}
	var out []EntryPoint
	for _, ec := range scan.EntryCandidates {
		out = append(out, EntryPoint{
			Path:    filepath.ToSlash(ec.Path),
			Kind:    InferEntryKind(root, ec.Path),
			Summary: entrySummary(ec),
		})
	}
	return out
}

func entrySummary(ec EntryCandidate) string {
	if strings.TrimSpace(ec.Reason) != "" {
		return ec.Reason
	}
	return "Entry point"
}

// InferEntryKind inspects file content for HTTP/CLI/library cues (cross-language).
func InferEntryKind(root, relPath string) string {
	data, err := os.ReadFile(filepath.Join(root, relPath))
	lowerPath := strings.ToLower(filepath.ToSlash(relPath))
	body := ""
	if err == nil {
		body = strings.ToLower(string(data))
		if len(body) > 8000 {
			body = body[:8000]
		}
	}

	httpCues := []string{
		"listenandserve", "http.handle", "http.listen", "gin.", "echo.", "fiber.",
		"chi.", "mux.", "fastapi", "uvicorn", "flask(", "django", "express(",
		"app.listen", "axum::", "actix_web", "rocket::", "hyper::",
		"@app.get", "@app.post", "create_app",
	}
	for _, cue := range httpCues {
		if strings.Contains(body, cue) {
			return "http-server"
		}
	}
	if strings.Contains(lowerPath, "server") || strings.Contains(lowerPath, "/api/") {
		return "http-server"
	}

	cliCues := []string{
		"cobra.", "flag.parse", "os.args", "argparse", "click.", "clap::",
		"process.argv", "commander", "yargs",
	}
	for _, cue := range cliCues {
		if strings.Contains(body, cue) {
			return "cli"
		}
	}
	if strings.Contains(lowerPath, "cmd/") || strings.HasPrefix(filepath.Base(lowerPath), "main.") {
		// Default cmd/main without HTTP cues → cli (common for Go CLIs).
		if strings.Contains(lowerPath, "cmd/") && !strings.Contains(lowerPath, "server") {
			return "cli"
		}
	}
	return "library"
}

// buildLayersFromScan creates Layers from top-level dirs + one level under internal/.
func buildLayersFromScan(scan *ProjectScan) []Layer {
	if scan == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []Layer
	add := func(name, path string) {
		key := filepath.ToSlash(path)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, Layer{
			Name:        name,
			Description: describeLayer(name),
			Paths:       []string{key},
		})
	}
	for _, dir := range scan.KeyDirNames {
		add(dir, dir)
	}
	// Enrich: internal/<pkg> from file tree.
	for _, f := range scan.FileTree {
		slash := filepath.ToSlash(f)
		parts := strings.Split(slash, "/")
		if len(parts) >= 2 && parts[0] == "internal" && parts[1] != "" {
			pkg := parts[1]
			add("internal/"+pkg, "internal/"+pkg)
		}
	}
	return out
}

func describeLayer(name string) string {
	base := name
	if i := strings.LastIndex(name, "/"); i >= 0 {
		base = name[i+1:]
	}
	switch strings.ToLower(base) {
	case "cmd":
		return "Process entrypoints (main packages)"
	case "internal":
		return "Private application packages (not importable by others)"
	case "pkg":
		return "Public library packages"
	case "handlers", "handler", "api", "routes", "controllers":
		return "HTTP/API request handling"
	case "database", "db", "store", "repository", "repo":
		return "Persistence / data access"
	case "models", "model", "domain", "entities":
		return "Domain models / entities"
	case "task", "tasks", "service", "services":
		return "Business logic / services"
	case "middleware", "auth":
		return "Cross-cutting request middleware / auth"
	case "migrations", "migrate":
		return "Schema migrations"
	case "docs", "doc":
		return "Project documentation"
	case "tests", "test", "testdata":
		return "Tests and fixtures"
	case "scripts", "tools":
		return "Dev / ops scripts"
	case "src":
		return "Primary source tree"
	case "lib":
		return "Shared libraries"
	default:
		return "Top-level package/directory: " + name
	}
}

// depsFromDataFlow maps filtered DataFlow into ArchDependencies.
func depsFromDataFlow(df *DataFlowReport) ArchDependencies {
	var deps ArchDependencies
	if df == nil {
		return deps
	}
	seenIn := map[string]bool{}
	for _, e := range df.ImportGraph {
		if e.Kind != "internal" || e.From == e.To {
			continue
		}
		line := e.From + " → " + e.To
		if seenIn[line] {
			continue
		}
		seenIn[line] = true
		deps.Internal = append(deps.Internal, line)
	}
	seenExt := map[string]bool{}
	for _, d := range df.ExternalDeps {
		if strings.TrimSpace(d.Path) == "" || seenExt[d.Path] {
			continue
		}
		seenExt[d.Path] = true
		label := d.Path
		if d.Role != "" && d.Role != "utility" {
			label = fmt.Sprintf("%s (%s)", d.Path, d.Role)
		}
		deps.External = append(deps.External, label)
	}
	return deps
}
