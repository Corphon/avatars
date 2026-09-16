package arch

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"avatars/internal/llm"
)

// GenerateArchDoc uses an LLM to produce an ArchDoc from a ProjectScan.
// The mode parameter selects the scenario: "init", "analyze", or "resume".
// For "init" mode, userIntent provides the user's project description.
// For "resume" mode, existingDoc is the previous ArchDoc (may be nil).
//
// The LLM receives:
//  1. A system prompt describing the architecture analyst role
//  2. The structured ProjectScan (or user intent for --init)
//  3. A JSON schema describing the expected output
//
// Returns the LLM-generated ArchDoc with status "draft".
func GenerateArchDoc(ctx context.Context, client llm.Client, scan *ProjectScan, mode string, userIntent string, existingDoc *ArchDoc, focusTopic string) (*ArchDoc, error) {
	systemPrompt := buildArchSystemPrompt(mode, focusTopic)
	userPrompt := buildArchUserPrompt(scan, mode, userIntent, existingDoc)

	// NOTE: No JSONSchema — DeepSeek and many providers don't support json_schema
	// type. We use StructuredOutput (json_object) and parse manually.
	resp, err := client.Generate(ctx, llm.Request{
		SystemPrompt:     systemPrompt,
		UserPrompt:       userPrompt,
		StructuredOutput: true,
		Category:         llm.CategoryAnalysis,
	})
	if err != nil {
		return nil, fmt.Errorf("arch generate: LLM call failed: %w", err)
	}

	// The LLM returns JSON (we asked for structured output). Try JSON parse first,
	// fall back to markdown parse if the LLM wrapped it in markdown.
	doc, err := ParseArchDoc(resp.Text)
	if err != nil {
		doc, err = ParseArchDocFromMarkdown(resp.Text)
	}
	if err != nil {
		preview := resp.Text
		if len(preview) > 500 {
			preview = preview[:500] + "..."
		}
		return nil, fmt.Errorf("arch generate: parse LLM output (%d chars): %w\nRaw preview: %s", len(resp.Text), err, preview)
	}
	// Post-validation: remove hallucinated file paths that don't exist in the project.
	// LLMs often invent plausible paths (e.g., "internal/provider/" when the real
	// path is "internal/llm/providers/"). This silently cleans up before persisting.
	if scan != nil {
		doc = validateArchDocPaths(scan, doc)
	}

	// Stamp metadata.
	doc.Meta.Source = mode
	doc.Meta.Status = "draft"
	if scan != nil {
		doc.Meta.LastModifiedFiles = scan.KeyFiles()
		doc.Meta.FileFingerprint = ComputeFingerprint(scan.Root, doc.Meta.LastModifiedFiles)
	}

	return doc, nil
}

// validateArchDocPaths removes or flags file paths in the ArchDoc that don't
// exist in the project scan. This prevents LLM path hallucination from
// producing useless Registration Points.
func validateArchDocPaths(scan *ProjectScan, doc *ArchDoc) *ArchDoc {
	// Build a set of real files and directories for quick lookup.
	realFiles := map[string]bool{}
	realDirs := map[string]bool{}
	for _, f := range scan.FileTree {
		realFiles[f] = true
		// Also index by just the filename (for loose matching).
		realFiles[filepath.Base(f)] = true
		// Index directory paths.
		dir := f
		for {
			if idx := strings.LastIndex(dir, "/"); idx >= 0 {
				dir = dir[:idx]
				realDirs[dir] = true
			} else {
				break
			}
		}
	}

	// Validate Entry Points.
	var validEPs []EntryPoint
	for _, ep := range doc.EntryPoints {
		if realFiles[ep.Path] || realDirs[ep.Path] {
			validEPs = append(validEPs, ep)
		} else if realFiles[filepath.Base(ep.Path)] {
			// Path is wrong but filename exists — fuzzy keep with a note.
			ep.Summary += " [path may be approximate]"
			validEPs = append(validEPs, ep)
		}
		// else: hallucinated — silently drop.
	}
	doc.EntryPoints = validEPs

	// Validate Registration Points — most critical for edit-many bridge.
	var validRPs []RegistrationPoint
	for _, rp := range doc.RegPoints {
		var validFiles []RegFile
		for _, rf := range rp.Files {
			if realFiles[rf.Path] || realDirs[rf.Path] || strings.Contains(rf.Path, "**") {
				validFiles = append(validFiles, rf)
			} else if realFiles[filepath.Base(rf.Path)] {
				rf.Path = "[approximate] " + rf.Path
				validFiles = append(validFiles, rf)
			}
			// else: hallucinated — silently drop this file from the list.
		}
		if len(validFiles) >= 1 {
			rp.Files = validFiles
			validRPs = append(validRPs, rp)
		}
	}
	doc.RegPoints = validRPs

	return doc
}

// buildArchSystemPrompt builds the role description for the architecture analyst LLM.
// The prompt is deliberately language-agnostic — it works for any project type.
func buildArchSystemPrompt(mode string, focusTopic string) string {
	base := `You are a senior software architect analyzing a project's structure. Your job is to produce a clear, structured architecture document that captures how the project is organized.

## Your Task
Given a project scan (file tree, entry points, dependency manifests, config files), produce a structured analysis covering:
1. **Overview**: 2-3 sentences describing what this project does (modules, storage, auth, phase boundaries). NEVER paste the user's raw requirements/prompt verbatim.
2. **Entry Points**: How execution enters the project (HTTP server, CLI, library, scripts, etc.)
3. **Layers**: Logical layers (config, transport, business logic, data, UI, etc.) with the files in each
4. **Registration Points** (critical): Places where adding/changing a component requires coordinated edits across multiple files. For example: adding a new provider/plugin/command/route/page — list ALL files that must be modified and how.
5. **Conventions**: Cross-cutting patterns and rules (error handling, logging, naming, interface contracts)
6. **Dependencies**: Internal module relationships and external framework/library dependencies

## Principles
- Be LANGUAGE-AGNOSTIC. Don't assume Go/Python/JS patterns — infer from what you see.
- Registration Points are the most important section. Think: "If someone wants to add a new X to this project, what files must they touch?" List every file and the specific change needed.
- Be specific but not verbose. Use the file paths from the scan.
- If the project is empty/minimal (init mode), design a clean architecture from the user's intent.

## Output Format
Return PURE JSON — no markdown fences, no explanation outside the JSON.
Use this EXACT structure (field names are mandatory):

{
  "overview": "<2-3 sentence description>",
  "entry_points": [
    {"path": "cmd/server/main.go", "kind": "http-server | cli | library | frontend | script", "summary": "one-line description"}
  ],
  "layers": [
    {"name": "Config", "description": "what this layer does", "paths": ["file1.go", "dir/"]}
  ],
  "registration_points": [
    {
      "change_type": "add-provider",
      "note": "When adding a new provider, ALL of these files must be modified",
      "files": [
        {"path": "internal/config/config.go", "line_range": "L200-L250", "change_hint": "append to providers list", "example": "\\\"new-provider\\\": NewProvider()"}
      ]
    }
  ],
  "conventions": [
    {"name": "Error handling", "description": "use fmt.Errorf with %w", "mandatory": true}
  ],
  "dependencies": {
    "internal": ["internal/config → internal/platform"],
    "external": ["github.com/gin-gonic/gin (HTTP framework)"]
  }
}

CRITICAL: Use the EXACT field names shown above. Do NOT use "action" for "change_type", "description" for "summary", "entry" for "path", "type" for "kind", "files_to_modify" for "files", etc.`

	// Append mode-specific guidance.
	switch mode {
	case "init":
		base += "\n\n**Mode: INIT** — The project is new or nearly empty. Design the architecture based on the user's stated intent. Propose a clean, standard structure for the project type.\nInfer the shape from intent: library/package/crate → in-process API (no HTTP); CLI → flags/stdio; HTTP service → only if they asked for HTTP/REST/server. Do NOT default to a 'Go backend + HTTP API'. JSON examples below are schema illustrations, not the required shape."
	case "resume":
		base += "\n\n**Mode: RESUME** — The project has partial implementation. You'll see the existing architecture document and the current project state. Identify gaps: what's missing from the target architecture, what's implemented, and what needs to be done next."
	default:
		base += "\n\n**Mode: ANALYZE** — The project has existing code. Read the file structure and infer the architecture from what's actually there. Do not invent what isn't present."
	}

	// Append focus guidance when the user wants specific patterns found.
	if focusTopic != "" {
		base += fmt.Sprintf("\n\n**FOCUS TOPIC: %s** — The user specifically wants you to identify how this pattern works in the project. "+
			"Pay extra attention to this. Find ALL files involved in this pattern and create a dedicated Registration Point for it. "+
			"Be thorough: trace the full lifecycle (creation → registration → configuration → UI integration if applicable).", focusTopic)
	}

	return base
}

// buildArchUserPrompt builds the user message containing the project scan data.
func buildArchUserPrompt(scan *ProjectScan, mode string, userIntent string, existingDoc *ArchDoc) string {
	var sb strings.Builder

	switch mode {
	case "init":
		sb.WriteString("## User Intent\n")
		if userIntent != "" {
			sb.WriteString(userIntent)
		} else {
			sb.WriteString("Build a new project.")
		}
		sb.WriteString("\n\n")
		if scan != nil && scan.FileStats.TotalFiles > 0 {
			sb.WriteString("## Existing Files (minimal)\n")
			writeScanSummary(&sb, scan)
		}

	case "resume":
		sb.WriteString("## Target Architecture (Previous)\n")
		if existingDoc != nil {
			sb.WriteString(FormatArchDocSummary(existingDoc))
		} else {
			sb.WriteString("(No previous architecture document found.)\n")
		}
		sb.WriteString("\n## Current Project State\n")
		if scan != nil {
			writeScanSummary(&sb, scan)
		} else {
			sb.WriteString("(No project scan available.)\n")
		}
		sb.WriteString("\n## Instructions\n")
		sb.WriteString("Compare the target architecture with the current state. ")
		sb.WriteString("Identify what's implemented, what's missing, and update the architecture document accordingly. ")
		sb.WriteString("If the project has diverged from the target architecture, note the changes.\n")

	default: // analyze
		sb.WriteString("## Project Scan\n")
		if scan != nil {
			writeScanDetail(&sb, scan)
		} else {
			sb.WriteString("(No project scan available.)\n")
		}
	}

	return sb.String()
}

// writeScanSummary writes a compact scan suitable for init/resume prompts.
func writeScanSummary(sb *strings.Builder, scan *ProjectScan) {
	sb.WriteString(fmt.Sprintf("Root: %s\n", scan.Root))
	sb.WriteString(fmt.Sprintf("Total files: %d\n", scan.FileStats.TotalFiles))
	if len(scan.KeyDirNames) > 0 {
		sb.WriteString(fmt.Sprintf("Top-level dirs: %s\n", strings.Join(scan.KeyDirNames, ", ")))
	}
	if len(scan.DepManifests) > 0 {
		sb.WriteString("Dependency manifests:\n")
		for _, dm := range scan.DepManifests {
			sb.WriteString(fmt.Sprintf("  - %s (%s)\n", dm.Path, dm.Kind))
		}
	}
	if len(scan.FileTree) <= 30 {
		sb.WriteString("Files:\n")
		for _, f := range scan.FileTree {
			sb.WriteString(fmt.Sprintf("  %s\n", f))
		}
	}
}

// writeScanDetail writes a full scan for analyze mode.
// Strategy: directory tree (compact) + key files (detailed) + extension summary.
// This prevents LLM path hallucination — the LLM sees actual directory structure
// rather than guessing common patterns like "internal/providers/" when the real
// path is "internal/llm/providers/".
func writeScanDetail(sb *strings.Builder, scan *ProjectScan) {
	sb.WriteString(fmt.Sprintf("Root: %s\n", scan.Root))
	sb.WriteString(fmt.Sprintf("Total files: %d\n\n", scan.FileStats.TotalFiles))

	// 1. Directory tree — compact overview, prevents path hallucination.
	dirs := extractDirTree(scan.FileTree)
	sb.WriteString("## Directory Tree\n")
	sb.WriteString("```\n")
	for _, d := range dirs {
		sb.WriteString(d)
		sb.WriteString("\n")
	}
	sb.WriteString("```\n\n")

	// 2. Key files in architecture-relevant directories — actual paths LLM can reference.
	keyPrefixes := []string{
		"cmd/", "internal/config/", "internal/llm/", "internal/api/",
		"internal/platform/", "internal/app/", "internal/models/",
		"frontend/src/pages/", "frontend/src/components/settings/",
		"frontend/src/store/", "frontend/src/api/",
	}
	shown := map[string]bool{}
	sb.WriteString("## Key Files (architecture-relevant paths)\n")
	for _, prefix := range keyPrefixes {
		count := 0
		for _, f := range scan.FileTree {
			if strings.HasPrefix(f, prefix) && !shown[f] {
				shown[f] = true
				sb.WriteString(fmt.Sprintf("  %s\n", f))
				count++
				if count >= 15 {
					sb.WriteString(fmt.Sprintf("  ... (%d more files in %s)\n",
						countFilesWithPrefix(scan.FileTree, prefix)-15, prefix))
					break
				}
			}
		}
	}

	// 3. Remaining important files not already shown.
	remaining := 0
	for _, f := range scan.FileTree {
		if !shown[f] && !strings.HasPrefix(f, "node_modules/") && !strings.HasPrefix(f, ".git/") {
			if remaining < 30 {
				sb.WriteString(fmt.Sprintf("  %s\n", f))
			}
			remaining++
		}
	}
	if remaining > 30 {
		sb.WriteString(fmt.Sprintf("  ... and %d more files\n", remaining-30))
	}

	sb.WriteString("\n## Entry Point Candidates\n")
	if len(scan.EntryCandidates) == 0 {
		sb.WriteString("  (none detected)\n")
	}
	for _, ec := range scan.EntryCandidates {
		sb.WriteString(fmt.Sprintf("  - %s (%s)\n", ec.Path, ec.Reason))
		if ec.Content != "" {
			sb.WriteString(fmt.Sprintf("    preview: %s\n", strings.ReplaceAll(strings.TrimSpace(ec.Content), "\n", " ")))
		}
	}

	sb.WriteString("\n## Dependency Manifests\n")
	if len(scan.DepManifests) == 0 {
		sb.WriteString("  (none detected)\n")
	}
	for _, dm := range scan.DepManifests {
		sb.WriteString(fmt.Sprintf("  - %s (%s)\n", dm.Path, dm.Kind))
		if dm.Content != "" {
			sb.WriteString(fmt.Sprintf("    preview: %s\n", strings.ReplaceAll(strings.TrimSpace(dm.Content), "\n", " ")))
		}
	}

	sb.WriteString("\n## Config Files\n")
	if len(scan.ConfigFiles) == 0 {
		sb.WriteString("  (none detected)\n")
	}
	for _, cf := range scan.ConfigFiles {
		sb.WriteString(fmt.Sprintf("  - %s\n", cf))
	}

	sb.WriteString("\n## File Stats by Extension\n")
	for ext, count := range scan.FileStats.ByExtension {
		sb.WriteString(fmt.Sprintf("  %s: %d\n", ext, count))
	}
}

// archDocJSONSchema returns the JSON schema for LLM structured output.
// Maps directly to the ArchDoc struct fields.
//
// INTEGRATION PROPOSAL: Wire into GenerateArchDoc by setting
// llm.Request{StructuredOutput: true, JSONSchema: archDocJSONSchema()}
// so the LLM returns valid ArchDoc JSON instead of free-text markdown.
// Currently GenerateArchDoc parses free-text with ParseArchDoc — switching
// to structured output would eliminate parsing errors.
func archDocJSONSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"overview": map[string]any{"type": "string", "description": "2-3 sentence project overview"},
			"entry_points": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{"type": "string"}, "kind": map[string]any{"type": "string"}, "summary": map[string]any{"type": "string"},
					},
					"required": []string{"path", "kind", "summary"},
				},
			},
			"layers": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"name": map[string]any{"type": "string"}, "description": map[string]any{"type": "string"},
						"paths": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					},
					"required": []string{"name", "description", "paths"},
				},
			},
			"registration_points": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"change_type": map[string]any{"type": "string"},
						"files": map[string]any{
							"type": "array",
							"items": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"path": map[string]any{"type": "string"}, "line_range": map[string]any{"type": "string"},
									"change_hint": map[string]any{"type": "string"}, "example": map[string]any{"type": "string"},
								},
								"required": []string{"path", "change_hint"},
							},
						},
						"note": map[string]any{"type": "string"},
					},
					"required": []string{"change_type", "files"},
				},
			},
			"conventions": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"name": map[string]any{"type": "string"}, "description": map[string]any{"type": "string"}, "mandatory": map[string]any{"type": "boolean"},
					},
					"required": []string{"name", "description", "mandatory"},
				},
			},
			"dependencies": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"internal": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"external": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				},
				"required": []string{"internal", "external"},
			},
		},
		"required": []string{"overview", "entry_points", "layers", "registration_points", "conventions", "dependencies"},
	}
}

// ParseArchDoc parses JSON content into an ArchDoc.
// The input may be pure JSON or JSON embedded in markdown/text.
func ParseArchDoc(content string) (*ArchDoc, error) {
	content = strings.TrimSpace(content)

	// Try direct JSON parse first.
	var doc ArchDoc
	if err := json.Unmarshal([]byte(content), &doc); err == nil {
		return &doc, nil
	}

	// Try extracting JSON from markdown code blocks.
	if extracted := extractJSONBlock(content); extracted != "" {
		if err := json.Unmarshal([]byte(extracted), &doc); err == nil {
			return &doc, nil
		}
	}

	// Try finding JSON object between { and }.
	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")
	if start >= 0 && end > start {
		candidate := content[start : end+1]
		if err := json.Unmarshal([]byte(candidate), &doc); err == nil {
			return &doc, nil
		}
	}

	return nil, fmt.Errorf("arch parse: could not extract valid JSON from LLM output")
}

// extractDirTree builds a compact directory tree from a flat file list.
// Only shows directories (not individual files), keeping output compact
// while still preventing LLM path hallucination.
func extractDirTree(files []string) []string {
	dirSet := map[string]bool{}
	for _, f := range files {
		dir := f
		for {
			parent := ""
			if idx := strings.LastIndex(dir, "/"); idx >= 0 {
				parent = dir[:idx]
			} else {
				break
			}
			dirSet[parent] = true
			dir = parent
		}
	}

	var dirs []string
	for d := range dirSet {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)

	// Build indented tree.
	var result []string
	prevParts := []string{}
	for _, d := range dirs {
		parts := strings.Split(d, "/")
		depth := 0
		for depth < len(parts) && depth < len(prevParts) && parts[depth] == prevParts[depth] {
			depth++
		}
		indent := strings.Repeat("  ", depth)
		result = append(result, fmt.Sprintf("%s%s/", indent, strings.Join(parts[depth:], "/")))
		prevParts = parts
	}
	return result
}

func countFilesWithPrefix(files []string, prefix string) int {
	count := 0
	for _, f := range files {
		if strings.HasPrefix(f, prefix) {
			count++
		}
	}
	return count
}

func extractJSONBlock(content string) string {
	// Look for ```json ... ``` or ``` ... ```
	for _, fence := range []string{"```json", "```"} {
		start := strings.Index(content, fence)
		if start < 0 {
			continue
		}
		start += len(fence)
		end := strings.Index(content[start:], "```")
		if end < 0 {
			continue
		}
		return strings.TrimSpace(content[start : start+end])
	}
	return ""
}
