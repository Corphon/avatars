package arch

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// InjectionLevel controls how much architecture context to inject.
type InjectionLevel int

const (
	// InjectNone means no architecture context is injected.
	// Used for simple operations: edit, script, stage, clarify.
	InjectNone InjectionLevel = iota

	// InjectOverview provides Overview + Registration Points only (~1500 chars).
	// Used for complex operations: run, bootstrap, edit-many.
	InjectOverview

	// InjectFull provides the complete architecture document.
	// Used for explicit arch queries or when LLM context is abundant.
	InjectFull
)

// String returns the injection level as a string label.
func (l InjectionLevel) String() string {
	switch l {
	case InjectNone:
		return "none"
	case InjectOverview:
		return "overview"
	case InjectFull:
		return "full"
	default:
		return "unknown"
	}
}

// BuildInjection produces a string to inject into LLM context based on the
// requested level and the architecture document.
//
// InjectionLevel choices:
//   - InjectNone: returns empty string
//   - InjectOverview: Overview + Entry Points + Registration Points, compressed
//   - InjectFull: complete architecture.md content
//
// This is a consumer-agnostic function — it doesn't know about NL router or
// Planner specifics. Each consumer calls BuildInjection with the level it needs.
func BuildInjection(doc *ArchDoc, level InjectionLevel) string {
	if doc == nil || level == InjectNone {
		return ""
	}

	switch level {
	case InjectOverview:
		return buildOverviewInjection(doc)
	case InjectFull:
		return FormatArchDoc(doc)
	default:
		return ""
	}
}

// buildOverviewInjection builds a compact injection (~1500 chars) with
// the most critical information: Overview + Registration Points + Entry Points.
// This is designed to fit within limited LLM context windows while still
// providing actionable architecture guidance.
func buildOverviewInjection(doc *ArchDoc) string {
	var sb strings.Builder

	sb.WriteString("[Project Architecture Context]\n")

	// Overview (most important).
	if doc.Overview != "" {
		sb.WriteString(fmt.Sprintf("Project: %s\n", doc.Overview))
	}

	// Entry Points.
	if len(doc.EntryPoints) > 0 {
		sb.WriteString("Entry points:\n")
		for _, ep := range doc.EntryPoints {
			sb.WriteString(fmt.Sprintf("  %s (%s): %s\n", ep.Path, ep.Kind, ep.Summary))
		}
	}

	// Registration Points — the key value proposition.
	if len(doc.RegPoints) > 0 {
		sb.WriteString("\nMulti-file change patterns (Registration Points):\n")
		for _, rp := range doc.RegPoints {
			sb.WriteString(fmt.Sprintf("  When %s, modify ALL of:\n", rp.ChangeType))
			for _, rf := range rp.Files {
				loc := rf.Path
				if rf.LineRange != "" {
					loc = fmt.Sprintf("%s (%s)", rf.Path, rf.LineRange)
				}
				sb.WriteString(fmt.Sprintf("    - %s → %s\n", loc, rf.ChangeHint))
			}
		}
	}

	// Layers (compact).
	if len(doc.Layers) > 0 {
		sb.WriteString("\nLayers:\n")
		for _, layer := range doc.Layers {
			paths := strings.Join(layer.Paths, ", ")
			if len(paths) > 120 {
				paths = paths[:120] + "..."
			}
			sb.WriteString(fmt.Sprintf("  %s: %s\n", layer.Name, paths))
		}
	}

	// Key conventions (compact).
	if len(doc.Conventions) > 0 {
		var mandatory []string
		for _, c := range doc.Conventions {
			if c.Mandatory {
				mandatory = append(mandatory, c.Name)
			}
		}
		if len(mandatory) > 0 {
			sb.WriteString(fmt.Sprintf("\nMandatory conventions: %s\n", strings.Join(mandatory, ", ")))
		}
	}

	sb.WriteString(fmt.Sprintf("\n[Arch doc status: %s | generated: %s]\n", doc.Meta.Status, doc.Meta.GeneratedAt))

	return sb.String()
}

// SuggestArchCommand returns a suggestion string to display to the user
// when no architecture.md exists but one would be helpful.
func SuggestArchCommand() string {
	return "💡 Tip: Run `avatars arch --analyze` to generate an architecture document. This helps avatars understand your project structure and make better multi-file changes."
}

// PlannerInjection reads architecture.md and returns a compact summary
// focused on Registration Points, for injection into the Planner context.
// This tells the Planner: "this task can be done by editing existing files,
// here are the specific files to modify."
func PlannerInjection(root string) string {
	doc, err := ReadArchDoc(root)
	if err != nil || doc == nil {
		return ""
	}
	if doc.Meta.Status != "confirmed" && doc.Meta.Status != "draft" {
		return ""
	}

	var b strings.Builder
	b.WriteString("Architecture Registration Points (use these to plan file edits):\n")
	for _, rp := range doc.RegPoints {
		b.WriteString(fmt.Sprintf("  [%s]\n", rp.ChangeType))
		for _, rf := range rp.Files {
			loc := rf.Path
			if rf.LineRange != "" {
				loc = fmt.Sprintf("%s (%s)", rf.Path, rf.LineRange)
			}
			b.WriteString(fmt.Sprintf("    - %s → %s\n", loc, rf.ChangeHint))
		}
	}
	if len(doc.EntryPoints) > 0 {
		b.WriteString("Entry points:\n")
		for _, ep := range doc.EntryPoints {
			b.WriteString(fmt.Sprintf("  - %s (%s)\n", ep.Path, ep.Kind))
		}
	}
	return b.String()
}

// NeedsArchDoc returns true if the given operation type would benefit from
// having an architecture.md available for context injection.
//
// INTEGRATION PROPOSAL: Call from nl_llm_router.go's classifyNaturalLanguageQuestion
// to decide whether to inject architecture context. Currently the router uses
// hasArchitectureContext() which only checks file existence — it doesn't
// differentiate by operation type. Wrapping that check with NeedsArchDoc
// would skip arch injection for trivial operations (ask, clarify) and only
// inject for code-gen operations (run, bootstrap, edit-many).
func NeedsArchDoc(operation string) bool {
	switch operation {
	case "run", "bootstrap", "edit-many":
		return true
	default:
		return false
	}
}

// InjectionLevelForOp returns the appropriate injection level for a given
// operation type. Simple ops get InjectNone, complex ops get InjectOverview,
// and explicit architecture queries get InjectFull.
//
// INTEGRATION PROPOSAL: Use in nl_llm_router.go's buildNLSystemPrompt to
// select the injection depth. Currently the router always injects at
// InjectOverview level when arch doc exists. Using this function would
// allow: InjectNone for simple operations (less token waste),
// InjectOverview for code-gen, and InjectFull for arch --analyze.
func InjectionLevelForOp(operation string) InjectionLevel {
	switch operation {
	case "run", "bootstrap", "edit-many":
		return InjectOverview
	case "arch":
		return InjectFull
	default:
		return InjectNone
	}
}

// InjectionWorthy reports whether architecture.md is grounded enough to inject
// into the intent-router system prompt (F116). Ungrounded HTTP/backend drafts
// on an empty tree poison routing and bust the provider prefix cache.
func InjectionWorthy(doc *ArchDoc, root string) bool {
	if doc == nil {
		return false
	}
	status := strings.ToLower(strings.TrimSpace(doc.Meta.Status))
	if status != "confirmed" && status != "draft" {
		return false
	}
	if OverviewLooksLikePrompt(doc.Overview) {
		return false
	}
	if claimsHTTPService(doc) && !treeHasHTTPEvidence(root) {
		return false
	}
	return hasUsefulRegPoints(doc) || treeHasSourceFiles(root)
}

func claimsHTTPService(doc *ArchDoc) bool {
	if doc == nil {
		return false
	}
	var b strings.Builder
	b.WriteString(doc.Overview)
	for _, ep := range doc.EntryPoints {
		b.WriteString(" ")
		b.WriteString(ep.Kind)
		b.WriteString(" ")
		b.WriteString(ep.Summary)
	}
	for _, layer := range doc.Layers {
		b.WriteString(" ")
		b.WriteString(layer.Name)
		b.WriteString(" ")
		b.WriteString(layer.Description)
	}
	lower := strings.ToLower(b.String())
	cues := []string{
		"http api", "http-server", "http server", "rest api", "restful",
		"webhook", "backend", "gin.", "fastapi", "express",
	}
	for _, cue := range cues {
		if strings.Contains(lower, cue) {
			return true
		}
	}
	return false
}

func treeHasSourceFiles(root string) bool {
	n := 0
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil {
			return nil
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", ".avatars", "node_modules", "vendor", "venv", ".venv", "__pycache__", "target", "dist":
				if path != root {
					return filepath.SkipDir
				}
			}
			return nil
		}
		switch strings.ToLower(filepath.Ext(info.Name())) {
		case ".go", ".py", ".js", ".ts", ".tsx", ".rs", ".java", ".cs":
			n++
			if n >= 1 {
				return filepath.SkipAll
			}
		}
		return nil
	})
	return n > 0
}

func treeHasHTTPEvidence(root string) bool {
	found := false
	cues := []string{
		"listenandserve", "http.handle", "gin.", "echo.", "fiber.",
		"fastapi", "uvicorn", "express(", "app.listen", "axum::",
		"actix_web", "@app.get", "@app.post",
	}
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || found {
			return nil
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", ".avatars", "node_modules", "vendor", "venv", ".venv", "__pycache__":
				if path != root {
					return filepath.SkipDir
				}
			}
			return nil
		}
		switch strings.ToLower(filepath.Ext(info.Name())) {
		case ".go", ".py", ".js", ".ts", ".tsx", ".rs", ".java":
		default:
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		body := strings.ToLower(string(data))
		if len(body) > 8000 {
			body = body[:8000]
		}
		for _, cue := range cues {
			if strings.Contains(body, cue) {
				found = true
				return filepath.SkipAll
			}
		}
		return nil
	})
	return found
}

