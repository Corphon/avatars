package arch

import (
	"fmt"
	"sort"
	"strings"
)

// FormatArchDoc serializes an ArchDoc to the architecture.md markdown format.
// This produces the human-readable, editable architecture.md file.
func FormatArchDoc(doc *ArchDoc) string {
	var sb strings.Builder

	sb.WriteString("# Project Architecture\n\n")

	// Meta section.
	sb.WriteString("## Meta\n")
	sb.WriteString(fmt.Sprintf("- generated_at: %s\n", doc.Meta.GeneratedAt))
	sb.WriteString(fmt.Sprintf("- source: %s\n", doc.Meta.Source))
	sb.WriteString(fmt.Sprintf("- status: %s\n", doc.Meta.Status))
	sb.WriteString(fmt.Sprintf("- file_fingerprint: %s\n", doc.Meta.FileFingerprint))
	if len(doc.Meta.LastModifiedFiles) > 0 {
		sb.WriteString(fmt.Sprintf("- last_modified_files: %s\n", strings.Join(doc.Meta.LastModifiedFiles, ", ")))
	}
	sb.WriteString("\n")

	// Overview.
	sb.WriteString("## Overview\n")
	sb.WriteString(doc.Overview)
	sb.WriteString("\n\n")

	// Entry Points.
	sb.WriteString("## Entry Points\n")
	if len(doc.EntryPoints) == 0 {
		sb.WriteString("(none identified)\n\n")
	} else {
		sb.WriteString("| Entry | Path | Kind | Description |\n")
		sb.WriteString("|-------|------|------|-------------|\n")
		for _, ep := range doc.EntryPoints {
			sb.WriteString(fmt.Sprintf("| %s | %s | %s | %s |\n",
				epName(ep.Path), ep.Path, ep.Kind, ep.Summary))
		}
		sb.WriteString("\n")
	}

	// Layers.
	sb.WriteString("## Layers\n")
	if len(doc.Layers) == 0 {
		sb.WriteString("(none identified)\n\n")
	}
	for _, layer := range doc.Layers {
		sb.WriteString(fmt.Sprintf("### %s\n", layer.Name))
		sb.WriteString(fmt.Sprintf("%s\n", layer.Description))
		for _, p := range layer.Paths {
			sb.WriteString(fmt.Sprintf("- %s\n", p))
		}
		sb.WriteString("\n")
	}

	// Registration Points (most important section).
	sb.WriteString("## Registration Points\n")
	sb.WriteString("> When adding/changing a component, ALL of the following files must be modified.\n\n")
	if len(doc.RegPoints) == 0 {
		sb.WriteString("(none identified — the LLM may need to re-analyze)\n\n")
	}
	for _, rp := range doc.RegPoints {
		sb.WriteString(fmt.Sprintf("### %s\n", rp.ChangeType))
		if rp.Note != "" {
			sb.WriteString(fmt.Sprintf("%s\n\n", rp.Note))
		}
		sb.WriteString("| File | Line Range | Change | Example |\n")
		sb.WriteString("|------|-----------|--------|--------|\n")
		for _, rf := range rp.Files {
			sb.WriteString(fmt.Sprintf("| %s | %s | %s | %s |\n",
				rf.Path, rf.LineRange, rf.ChangeHint, rf.Example))
		}
		sb.WriteString("\n")
	}

	// Conventions.
	sb.WriteString("## Cross-cutting Conventions\n")
	if len(doc.Conventions) == 0 {
		sb.WriteString("(none identified)\n\n")
	} else {
		sb.WriteString("| Convention | Description | Mandatory |\n")
		sb.WriteString("|------------|-------------|----------|\n")
		for _, c := range doc.Conventions {
			mand := ""
			if c.Mandatory {
				mand = "✅"
			} else {
				mand = "-"
			}
			sb.WriteString(fmt.Sprintf("| %s | %s | %s |\n", c.Name, c.Description, mand))
		}
		sb.WriteString("\n")
	}

	// Dependencies.
	sb.WriteString("## Dependencies\n")
	sb.WriteString("### Internal\n")
	if len(doc.Dependencies.Internal) == 0 {
		sb.WriteString("(none identified)\n")
	}
	for _, d := range doc.Dependencies.Internal {
		sb.WriteString(fmt.Sprintf("- %s\n", d))
	}
	sb.WriteString("\n### External\n")
	if len(doc.Dependencies.External) == 0 {
		sb.WriteString("(none identified)\n")
	}
	for _, d := range doc.Dependencies.External {
		sb.WriteString(fmt.Sprintf("- %s\n", d))
	}

	// Data Flow section (import graph, key types, external deps).
	if doc.DataFlow != nil {
		sb.WriteString("\n")
		sb.WriteString(FormatDataFlowSection(doc.DataFlow))
	}

	return sb.String()
}

// FormatArchDocSummary returns a compact summary of an ArchDoc suitable for
// injection into LLM prompts (~1500-3000 chars depending on project size).
func FormatArchDocSummary(doc *ArchDoc) string {
	if doc == nil {
		return "(no architecture document)"
	}
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("Overview: %s\n\n", doc.Overview))
	sb.WriteString(fmt.Sprintf("Status: %s | Generated: %s\n\n", doc.Meta.Status, doc.Meta.GeneratedAt))

	if len(doc.EntryPoints) > 0 {
		sb.WriteString("Entry Points:\n")
		for _, ep := range doc.EntryPoints {
			sb.WriteString(fmt.Sprintf("  - %s (%s): %s\n", ep.Path, ep.Kind, ep.Summary))
		}
		sb.WriteString("\n")
	}

	if len(doc.RegPoints) > 0 {
		sb.WriteString("Registration Points (multi-file change patterns):\n")
		for _, rp := range doc.RegPoints {
			sb.WriteString(fmt.Sprintf("  [%s]\n", rp.ChangeType))
			for _, rf := range rp.Files {
				sb.WriteString(fmt.Sprintf("    - %s → %s\n", rf.Path, rf.ChangeHint))
			}
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

// ParseArchDocFromMarkdown parses architecture.md content back into an ArchDoc.
// This is the inverse of FormatArchDoc. It handles the structured markdown
// format defined in the architecture.md specification.
func ParseArchDocFromMarkdown(content string) (*ArchDoc, error) {
	doc := &ArchDoc{
		Dependencies: ArchDependencies{},
	}
	lines := strings.Split(content, "\n")

	currentSection := ""
	currentSubsection := ""
	var currentRegPoint *RegistrationPoint

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Section headers.
		if strings.HasPrefix(trimmed, "## ") {
			currentSection = strings.TrimPrefix(trimmed, "## ")
			currentSubsection = ""
			currentRegPoint = nil
			continue
		}
		if strings.HasPrefix(trimmed, "### ") {
			currentSubsection = strings.TrimPrefix(trimmed, "### ")
			currentRegPoint = nil
			continue
		}

		switch currentSection {
		case "Meta":
			parseMetaLine(doc, trimmed)
		case "Overview":
			if trimmed != "" && !strings.HasPrefix(trimmed, ">") {
				if doc.Overview != "" {
					doc.Overview += " "
				}
				doc.Overview += trimmed
			}
		case "Entry Points":
			parseEntryPointLine(doc, trimmed)
		case "Layers":
			parseLayerLine(doc, trimmed, currentSubsection)
		case "Registration Points":
			parseRegPointLine(doc, trimmed, currentSubsection, &currentRegPoint)
		case "Cross-cutting Conventions":
			parseConventionLine(doc, trimmed)
		case "Dependencies":
			parseDependencyLine(doc, trimmed, currentSubsection)
		}
	}

	if doc.Meta.Status == "" {
		doc.Meta.Status = "draft"
	}
	return doc, nil
}

// Internal helpers for markdown parsing.

func parseMetaLine(doc *ArchDoc, line string) {
	switch {
	case strings.HasPrefix(line, "- generated_at:"):
		doc.Meta.GeneratedAt = strings.TrimSpace(strings.TrimPrefix(line, "- generated_at:"))
	case strings.HasPrefix(line, "- source:"):
		doc.Meta.Source = strings.TrimSpace(strings.TrimPrefix(line, "- source:"))
	case strings.HasPrefix(line, "- status:"):
		doc.Meta.Status = strings.TrimSpace(strings.TrimPrefix(line, "- status:"))
	case strings.HasPrefix(line, "- file_fingerprint:"):
		doc.Meta.FileFingerprint = strings.TrimSpace(strings.TrimPrefix(line, "- file_fingerprint:"))
	case strings.HasPrefix(line, "- last_modified_files:"):
		files := strings.TrimSpace(strings.TrimPrefix(line, "- last_modified_files:"))
		doc.Meta.LastModifiedFiles = splitAndTrim(files, ",")
	}
}

func parseEntryPointLine(doc *ArchDoc, line string) {
	if !strings.HasPrefix(line, "| ") || strings.HasPrefix(line, "| Entry") || strings.HasPrefix(line, "|--") {
		return
	}
	parts := splitTableRow(line)
	if len(parts) < 3 {
		return
	}
	ep := EntryPoint{
		Path:    strings.TrimSpace(parts[1]),
		Kind:    strings.TrimSpace(parts[2]),
	}
	if len(parts) > 3 {
		ep.Summary = strings.TrimSpace(parts[3])
	}
	doc.EntryPoints = append(doc.EntryPoints, ep)
}

func parseLayerLine(doc *ArchDoc, line string, sub string) {
	if sub != "" && strings.HasPrefix(line, "- ") {
		path := strings.TrimPrefix(line, "- ")
		// Find or create the layer.
		for i := range doc.Layers {
			if doc.Layers[i].Name == sub {
				doc.Layers[i].Paths = append(doc.Layers[i].Paths, path)
				return
			}
		}
		doc.Layers = append(doc.Layers, Layer{
			Name:  sub,
			Paths: []string{path},
		})
	}
}

func parseRegPointLine(doc *ArchDoc, line string, sub string, current **RegistrationPoint) {
	if sub == "" || strings.HasPrefix(line, "> ") {
		return
	}

	// New registration point group.
	if *current == nil || (*current).ChangeType != sub {
		rp := RegistrationPoint{ChangeType: sub}
		doc.RegPoints = append(doc.RegPoints, rp)
		*current = &doc.RegPoints[len(doc.RegPoints)-1]
	}

	if strings.HasPrefix(line, "| ") && !strings.HasPrefix(line, "| File") && !strings.HasPrefix(line, "|--") {
		parts := splitTableRow(line)
		if len(parts) >= 3 {
			rf := RegFile{
				Path:       strings.TrimSpace(parts[0]),
				LineRange:  "",
				ChangeHint: strings.TrimSpace(parts[2]),
			}
			if len(parts) > 1 {
				rf.LineRange = strings.TrimSpace(parts[1])
			}
			if len(parts) > 3 {
				rf.Example = strings.TrimSpace(parts[3])
			}
			(*current).Files = append((*current).Files, rf)
		}
	}

	// Non-table lines before the table are the "note".
	if !strings.HasPrefix(line, "|") && line != "" {
		(*current).Note = line
	}
}

func parseConventionLine(doc *ArchDoc, line string) {
	if !strings.HasPrefix(line, "| ") || strings.HasPrefix(line, "| Convention") || strings.HasPrefix(line, "|--") {
		return
	}
	parts := splitTableRow(line)
	if len(parts) < 2 {
		return
	}
	c := Convention{
		Name:        strings.TrimSpace(parts[0]),
		Description: "",
		Mandatory:   false,
	}
	if len(parts) > 1 {
		c.Description = strings.TrimSpace(parts[1])
	}
	if len(parts) > 2 {
		c.Mandatory = strings.TrimSpace(parts[2]) == "✅"
	}
	doc.Conventions = append(doc.Conventions, c)
}

func parseDependencyLine(doc *ArchDoc, line string, sub string) {
	if line == "" || strings.HasPrefix(line, "### ") || strings.HasPrefix(line, "(none") {
		return
	}
	if strings.HasPrefix(line, "- ") {
		dep := strings.TrimPrefix(line, "- ")
		if sub == "External" || sub == "Internal" {
			if sub == "Internal" {
				doc.Dependencies.Internal = append(doc.Dependencies.Internal, dep)
			} else {
				doc.Dependencies.External = append(doc.Dependencies.External, dep)
			}
		}
	}
}

// Utility functions.

func splitTableRow(line string) []string {
	// Remove leading/trailing | and split by |.
	line = strings.TrimPrefix(line, "| ")
	line = strings.TrimSuffix(line, " |")
	return strings.Split(line, " | ")
}

func splitAndTrim(s, sep string) []string {
	parts := strings.Split(s, sep)
	var result []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	sort.Strings(result)
	return result
}

func epName(path string) string {
	base := path
	if idx := strings.LastIndex(path, "/"); idx >= 0 {
		base = path[idx+1:]
	}
	if idx := strings.LastIndex(base, "."); idx >= 0 {
		base = base[:idx]
	}
	return base
}
