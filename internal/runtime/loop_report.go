package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"avatars/internal/planner"
	"avatars/internal/projectfiles"
	"avatars/internal/verification"
)

func buildSynthesisEvidenceDigest(readSummary string, limitPerBucket int) string {
	items := reportReadSummaryItems(readSummary)
	if len(items) == 0 {
		return conciseReportLine(readSummary)
	}
	coverage := buildEvidenceCoverage(readSummary)
	bucketOrder := []string{"docs", "manifests", "entrypoints", "source", "config", "ci", "other"}
	buckets := map[string][]string{}
	for _, item := range items {
		bucket := evidenceCoverageBucket(item)
		buckets[bucket] = append(buckets[bucket], readSummaryItemDigest(item))
	}
	parts := []string{
		fmt.Sprintf("digest total=%d docs=%d manifests=%d entrypoints=%d source=%d config=%d ci=%d other=%d", coverage.Total, coverage.Docs, coverage.Manifests, coverage.Entrypoints, coverage.Source, coverage.Config, coverage.CI, coverage.Other),
	}
	for _, bucket := range bucketOrder {
		anchors := buckets[bucket]
		if len(anchors) == 0 {
			continue
		}
		trimmed := anchors
		if limitPerBucket > 0 && len(trimmed) > limitPerBucket {
			trimmed = trimmed[:limitPerBucket]
		}
		parts = append(parts, fmt.Sprintf("%s: %s", bucket, strings.Join(trimmed, ", ")))
		if remaining := len(anchors) - len(trimmed); remaining > 0 {
			parts = append(parts, fmt.Sprintf("%s_more: +%d", bucket, remaining))
		}
	}
	return strings.Join(parts, " ")
}

func readSummaryItemAnchor(item string) string {
	trimmed := strings.TrimSpace(strings.TrimSuffix(item, "."))
	trimmed = strings.TrimPrefix(trimmed, "Read ")
	if index := strings.Index(trimmed, " successfully"); index >= 0 {
		trimmed = trimmed[:index]
	}
	return strings.TrimSpace(trimmed)
}

func readSummaryItemDigest(item string) string {
	anchor := readSummaryItemAnchor(item)
	details := []string{}
	addField := func(label string, maxLen int) {
		if value := readSummaryItemField(item, label); value != "" {
			if maxLen > 0 && len(value) > maxLen {
				value = value[:maxLen] + "..."
			}
			details = append(details, fmt.Sprintf("%s=%s", strings.ToLower(strings.TrimSuffix(label, ":")), value))
		}
	}
	addField("Title:", 80)
	addField("Module:", 80)
	addField("Package:", 60)
	addField("Headings:", 120)
	addField("Key points:", 120)
	addField("Imports:", 120)
	addField("Top declarations:", 140)
	addField("Function signals:", 120)
	addField("Behavior signals:", 140)
	addField("Path signals:", 120)
	addField("Entrypoint signal:", 80)
	addField("Declaration surface:", 80)
	addField("Command signals:", 120)
	if len(details) == 0 {
		return anchor
	}
	return fmt.Sprintf("%s [%s]", anchor, strings.Join(details, "; "))
}

func readSummaryItemField(item string, label string) string {
	index := strings.Index(item, label)
	if index < 0 {
		return ""
	}
	remainder := strings.TrimSpace(item[index+len(label):])
	if remainder == "" {
		return ""
	}
	if end := strings.Index(remainder, ". "); end >= 0 {
		remainder = remainder[:end]
	}
	return strings.TrimSpace(strings.TrimSuffix(remainder, "."))
}

func summarizeReadResult(path string, content string) string {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return fmt.Sprintf("Read %s successfully, but the file is empty.", path)
	}

	switch strings.ToLower(filepath.Base(path)) {
	case "readme.md", "readme", strings.ToLower(projectfiles.CLIGuide), strings.ToLower(projectfiles.ProcessRecord), strings.ToLower(projectfiles.CodingPlan):
		return summarizeMarkdownRead(path, trimmed)
	case "go.mod":
		return summarizeGoModRead(path, trimmed)
	default:
		if strings.HasSuffix(strings.ToLower(path), ".go") {
			return summarizeGoSourceRead(path, trimmed)
		}
		return summarizeGenericRead(path, trimmed)
	}
}

func summarizeMarkdownRead(path string, content string) string {
	lines := strings.Split(content, "\n")
	title := ""
	headings := []string{}
	bullets := []string{}
	commands := []string{}
	inFence := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			continue
		}
		if inFence || strings.HasPrefix(trimmed, "`") {
			if command := markdownCommandSignal(trimmed); command != "" {
				commands = append(commands, command)
			}
		}
		if title == "" && strings.HasPrefix(trimmed, "#") {
			title = strings.TrimSpace(strings.TrimLeft(trimmed, "#"))
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			headings = append(headings, strings.TrimSpace(strings.TrimLeft(trimmed, "#")))
			continue
		}
		if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") {
			bullets = append(bullets, strings.TrimSpace(trimmed[2:]))
		}
		if len(headings) >= 3 && len(bullets) >= 3 && len(commands) >= 3 {
			break
		}
	}
	parts := []string{fmt.Sprintf("Read %s successfully.", path)}
	if title != "" && isEnglishSafeRuntimeText(title) {
		parts = append(parts, fmt.Sprintf("Title: %s.", title))
	} else if title != "" {
		parts = append(parts, "First line captured from repository content.")
	}
	if len(headings) > 0 {
		parts = append(parts, fmt.Sprintf("Headings: %s.", strings.Join(headings, "; ")))
	}
	if len(bullets) > 0 {
		parts = append(parts, fmt.Sprintf("Key points: %s.", strings.Join(bullets, "; ")))
	}
	if len(commands) > 0 {
		parts = append(parts, fmt.Sprintf("Command signals: %s.", strings.Join(commands[:min(len(commands), 3)], "; ")))
	}
	return strings.Join(parts, " ")
}

func markdownCommandSignal(line string) string {
	trimmed := strings.Trim(strings.TrimSpace(line), "`")
	trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "$"))
	if trimmed == "" || !isEnglishSafeRuntimeText(trimmed) {
		return ""
	}
	for _, prefix := range []string{"go ", "npm ", "pnpm ", "yarn ", "python ", "pytest", "cargo ", "make", "docker ", "avatars "} {
		if strings.HasPrefix(trimmed, prefix) || trimmed == prefix {
			if len(trimmed) > 120 {
				trimmed = trimmed[:120] + "..."
			}
			return trimmed
		}
	}
	return ""
}

func summarizeGoModRead(path string, content string) string {
	module, requirements := parseGoModFacts(content)
	parts := []string{fmt.Sprintf("Read %s successfully.", path)}
	if module != "" {
		parts = append(parts, fmt.Sprintf("Module: %s.", module))
	}
	if len(requirements) > 0 {
		parts = append(parts, fmt.Sprintf("Requires: %s.", strings.Join(requirements[:min(len(requirements), 4)], "; ")))
	}
	return strings.Join(parts, " ")
}

func parseGoModFacts(content string) (string, []string) {
	lines := strings.Split(content, "\n")
	module := ""
	requirements := []string{}
	inRequireBlock := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") {
			continue
		}
		switch {
		case strings.HasPrefix(trimmed, "module "):
			module = strings.TrimSpace(strings.TrimPrefix(trimmed, "module "))
		case strings.HasPrefix(trimmed, "require ("):
			inRequireBlock = true
		case trimmed == ")" && inRequireBlock:
			inRequireBlock = false
		case strings.HasPrefix(trimmed, "require "):
			requirements = append(requirements, strings.TrimSpace(strings.TrimPrefix(trimmed, "require ")))
		case inRequireBlock:
			requirements = append(requirements, trimmed)
		}
	}
	return module, requirements
}

func summarizeGoSourceRead(path string, content string) string {
	lines := strings.Split(content, "\n")
	pkg := ""
	imports := []string{}
	exports := []string{}
	hasMainFunc := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "/*") || strings.HasPrefix(trimmed, "*") {
			continue
		}
		if pkg == "" && strings.HasPrefix(trimmed, "package ") {
			pkg = strings.TrimSpace(strings.TrimPrefix(trimmed, "package "))
			continue
		}
		if strings.HasPrefix(trimmed, "import ") {
			imports = append(imports, strings.TrimSpace(strings.TrimPrefix(trimmed, "import ")))
			continue
		}
		if strings.HasPrefix(trimmed, "func ") || strings.HasPrefix(trimmed, "type ") || strings.HasPrefix(trimmed, "var ") || strings.HasPrefix(trimmed, "const ") {
			exports = append(exports, trimmed)
			if strings.HasPrefix(trimmed, "func main(") {
				hasMainFunc = true
			}
		}
		if len(imports) >= 4 && len(exports) >= 5 {
			break
		}
	}
	parts := []string{fmt.Sprintf("Read %s successfully.", path)}
	if pkg != "" {
		parts = append(parts, fmt.Sprintf("Package: %s.", pkg))
	}
	if len(imports) > 0 {
		parts = append(parts, fmt.Sprintf("Imports: %s.", strings.Join(imports[:min(len(imports), 4)], "; ")))
	}
	if len(exports) > 0 {
		parts = append(parts, fmt.Sprintf("Top declarations: %s.", strings.Join(exports[:min(len(exports), 4)], " | ")))
	}
	if functionSignals := summarizeGoSourceFunctionSignals(content); len(functionSignals) > 0 {
		parts = append(parts, fmt.Sprintf("Function signals: %s.", strings.Join(functionSignals, "; ")))
	}
	if behaviorSignals := summarizeGoSourceBehaviorSignals(content); len(behaviorSignals) > 0 {
		parts = append(parts, fmt.Sprintf("Behavior signals: %s.", strings.Join(behaviorSignals, "; ")))
	}
	if pathSignals := summarizeRepositoryPathSignals(content); len(pathSignals) > 0 {
		parts = append(parts, fmt.Sprintf("Path signals: %s.", strings.Join(pathSignals, "; ")))
	}
	if hasMainFunc || strings.HasSuffix(strings.ToLower(filepath.Base(path)), "main.go") {
		parts = append(parts, "Entrypoint signal: main function present.")
	}
	if len(exports) >= 4 {
		parts = append(parts, fmt.Sprintf("Declaration surface: %d top-level items observed.", len(exports)))
	}
	return strings.Join(parts, " ")
}

func summarizeRepositoryPathSignals(content string) []string {
	re := regexp.MustCompile(`["']([^"']+/[^"']+\.[A-Za-z0-9_+-]+)["']`)
	seen := map[string]bool{}
	signals := []string{}
	for _, match := range re.FindAllStringSubmatch(content, -1) {
		if len(match) < 2 {
			continue
		}
		candidate := cleanRequestFileTarget(match[1])
		if candidate == "" || isIgnoredSurveyPath(candidate) {
			continue
		}
		key := strings.ToLower(filepath.ToSlash(candidate))
		if seen[key] {
			continue
		}
		seen[key] = true
		signals = append(signals, filepath.ToSlash(candidate))
		if len(signals) >= 4 {
			break
		}
	}
	return signals
}

func summarizeGenericRead(path string, content string) string {
	lines := strings.Split(content, "\n")
	preview := strings.TrimSpace(lines[0])
	if !isEnglishSafeRuntimeText(preview) {
		return fmt.Sprintf("Read %s successfully. First line captured from repository content.", path)
	}
	if len(preview) > 120 {
		preview = preview[:120] + "..."
	}
	return fmt.Sprintf("Read %s successfully. First line: %s", path, preview)
}

func summarizeGoSourceFunctionSignals(content string) []string {
	lines := strings.Split(content, "\n")
	signals := []string{}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "/*") || strings.HasPrefix(trimmed, "*") {
			continue
		}
		if strings.HasPrefix(trimmed, "func ") {
			signal := summarizeGoFunctionSignature(trimmed)
			if signal != "" {
				signals = append(signals, signal)
			}
		}
		if len(signals) >= 4 {
			break
		}
	}
	return signals
}

func summarizeGoSourceBehaviorSignals(content string) []string {
	lowered := strings.ToLower(content)
	signals := []string{}
	add := func(signal string, tokens ...string) {
		for _, token := range tokens {
			if strings.Contains(lowered, strings.ToLower(token)) {
				signals = append(signals, signal)
				return
			}
		}
	}
	add("reads files", "os.ReadFile", ".ReadFile(", "readFile(")
	add("writes files", "os.WriteFile", ".WriteFile(", ".SaveAs(", ".Save(")
	add("executes SQL", ".Query(", ".QueryContext(", ".Exec(", ".ExecContext(", "executeSQL", "runsql", "sqlstr")
	add("handles Excel workbooks", "excelize.", "WorkbookPayload", "SheetPayload", "SetCell", "GetRows")
	add("registers runtime items", ".Register(", "NewRegistry(", "registry")
	add("applies plugins", ".Apply(", "plugin.Payload", "Plugin interface")
	add("uses context cancellation boundary", "context.Context", "ctx context")
	add("uses locking", "sync.Mutex", ".Lock(", ".Unlock(")
	add("returns explicit errors", "fmt.Errorf", "errors.New", "return err")
	deduped := dedupeReportLines(signals)
	return deduped[:min(len(deduped), 6)]
}

func summarizeGoFunctionSignature(signature string) string {
	trimmed := strings.TrimSpace(signature)
	if !strings.HasPrefix(trimmed, "func ") {
		return ""
	}
	trimmed = strings.TrimPrefix(trimmed, "func ")
	if idx := strings.Index(trimmed, "{"); idx >= 0 {
		trimmed = strings.TrimSpace(trimmed[:idx])
	}
	if strings.HasPrefix(trimmed, "(") {
		if idx := strings.Index(trimmed, ")"); idx >= 0 && idx+1 < len(trimmed) {
			receiver := strings.TrimSpace(trimmed[1:idx])
			trimmed = strings.TrimSpace(trimmed[idx+1:])
			if fields := strings.Fields(receiver); len(fields) > 0 {
				receiverType := strings.TrimLeft(fields[len(fields)-1], "*")
				if receiverType != "" {
					trimmed = receiverType + "." + trimmed
				}
			}
		}
	}
	if idx := strings.Index(trimmed, "("); idx >= 0 {
		trimmed = strings.TrimSpace(trimmed[:idx])
	}
	if trimmed == "" {
		return ""
	}
	if len(trimmed) > 80 {
		trimmed = trimmed[:80] + "..."
	}
	return trimmed
}

func resolveAnalysisReportPath(input string) string {
	lowered := strings.ToLower(strings.TrimSpace(input))
	if !looksLikeReportOutputRequest(lowered) {
		return ""
	}
	for _, candidate := range requestMarkdownPathCandidates(input) {
		if strings.EqualFold(filepath.Ext(candidate), ".md") {
			if isHumanDeliveryDocPath(candidate) {
				continue
			}
			return candidate
		}
	}
	for _, alias := range reportOutputTargetAliases() {
		if isHumanDeliveryDocPath(alias) {
			continue
		}
		if strings.Contains(lowered, alias) {
			return alias
		}
	}
	return ""
}

// isHumanDeliveryDocPath is true for user-facing delivery notes that must
// never receive the Analysis Report template (F110). Cross-language: DELIVERY.md
// / 交付.md, not just Go library READMEs.
func isHumanDeliveryDocPath(path string) bool {
	base := strings.ToLower(filepath.Base(strings.TrimSpace(path)))
	if base == "" {
		return false
	}
	if strings.Contains(base, "交付") {
		return true
	}
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	switch stem {
	case "delivery", "changelog", "release-notes", "releasenotes":
		return true
	}
	return strings.HasPrefix(stem, "delivery-") || strings.HasPrefix(stem, "delivery_")
}

func isAnalysisReportFile(path string) bool {
	return projectfiles.IsAnalysisReportFile(path)
}

func isGeneratedAnalysisReportPath(path string) bool {
	if strings.ToLower(filepath.Ext(path)) != ".md" {
		return false
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(content), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		lowered := strings.ToLower(trimmed)
		return lowered == "# analysis report" || lowered == "# bootstrap planning report"
	}
	return false
}

func looksLikeExplicitReportInspection(input string) bool {
	lowered := strings.ToLower(strings.TrimSpace(input))
	for _, token := range []string{
		"write",
		"output",
		"save",
		"summarize in",
		"report to",
		"写入",
		"输出",
		"保存",
		"记录在",
	} {
		if strings.Contains(lowered, token) {
			return false
		}
	}
	for _, token := range []string{
		"inspect",
		"review",
		"report quality",
		"检查",
		"审查",
		"查看",
		"读",
	} {
		if strings.Contains(lowered, token) {
			return true
		}
	}
	return false
}

func requestMarkdownPathCandidates(input string) []string {
	seen := map[string]bool{}
	candidates := []string{}
	add := func(candidate string) {
		cleaned := cleanRequestFileTarget(candidate)
		if cleaned == "" || !strings.EqualFold(filepath.Ext(cleaned), ".md") || seen[strings.ToLower(cleaned)] {
			return
		}
		seen[strings.ToLower(cleaned)] = true
		candidates = append(candidates, cleaned)
	}
	for _, token := range strings.FieldsFunc(input, isRequestTargetSeparator) {
		add(token)
	}
	for _, token := range regexp.MustCompile(`(?i)[A-Za-z0-9_.\-/\\]+\.md`).FindAllString(input, -1) {
		add(token)
	}
	return candidates
}

func buildAnalysisReportMarkdown(input string, plan planner.Plan, readSummary string, finalSummary string, synthesis string, generatedSkillPath string, generatedSkillPreview string, invokedSkillName string, restored bool) string {
	synthesisBlock := reportMarkdownBlock(synthesis)
	if analysisReportLooksComplete(synthesisBlock) {
		if appendix := reportEvidenceAppendixIfNeeded(synthesisBlock, plan, readSummary, generatedSkillPath, generatedSkillPreview, invokedSkillName, restored); appendix != "" {
			return synthesisBlock + appendix
		}
		return synthesisBlock + "\n"
	}
	if bootstrapPlanningState(readSummary) {
		return buildBootstrapPlanningReport(input, plan, readSummary, generatedSkillPath, generatedSkillPreview, invokedSkillName, restored)
	}
	stateLines := projectStateLines(readSummary)
	synthesisForReport := synthesisBlockForWrappedReport(synthesisBlock)
	sections := []string{
		"# Analysis Report",
		fmt.Sprintf("## Request\n%s", strings.TrimSpace(input)),
		fmt.Sprintf("## Project Purpose\n%s", projectPurposeLine(readSummary, synthesis)),
		"## Synthesis Status",
		reportSynthesisStatus(finalSummary, synthesis),
		"## Synthesis",
		synthesisForReport,
	}
	sections = append(sections, reportFindingsSections(readSummary, finalSummary, synthesis)...)
	sections = append(sections,
		"## Candidate Risks With Evidence",
		reportCandidateRisksWithEvidence(readSummary),
		"## Project State",
		strings.Join(stateLines, "\n"),
		"## Confidence",
		reportConfidenceSection(readSummary, synthesis),
		"## Code Trace Evidence",
		reportCodeTraceEvidence(readSummary),
		"## Verification Leads",
		reportVerificationLeads(readSummary),
		"## Verification Results",
		reportVerificationResults(readSummary),
		"## Remediation Leads",
		reportRemediationLeads(readSummary),
		"## Function-Level Risk Leads",
		reportFunctionRiskLeads(readSummary),
		"## Evidence Quality",
		reportEvidenceQuality(readSummary),
		"## Repository Gap Evidence",
		reportRepositoryGapEvidence(readSummary),
		"## Next Checks",
		reportNextChecks(readSummary, generatedSkillPath, generatedSkillPreview, invokedSkillName),
		"## Evidence Coverage",
		evidenceCoverageMarkdown(readSummary),
		"## Evidence Reviewed",
		strings.Join(reportEvidenceListItems(plan, readSummary, generatedSkillPath, generatedSkillPreview, invokedSkillName, restored), "\n"),
	)
	return strings.Join(sections, "\n\n") + "\n"
}

func analysisReportLooksComplete(text string) bool {
	lowered := strings.ToLower(strings.TrimSpace(text))
	if !(reportHasHeading(text, "Project Purpose") &&
		reportHasHeading(text, "Proven Findings") &&
		strings.Contains(lowered, "evidence-tied risks") &&
		reportHasHeading(text, "Open Questions")) {
		return false
	}
	return reportProvenFindingsHaveEvidenceAnchors(text)
}

func synthesisBlockForWrappedReport(text string) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "No synthesis text was available."
	}
	for _, heading := range []string{"Project Purpose", "Evidence Reviewed", "Evidence Coverage", "Code Trace Evidence", "Repository Gap Evidence", "Proven Findings", "Evidence-Tied Risks / Needs Verification", "Open Questions", "Next Checks"} {
		if reportHasHeading(trimmed, heading) {
			return synthesisSummaryLine(trimmed)
		}
	}
	return trimmed
}

func reportHasHeading(text string, heading string) bool {
	target := strings.ToLower(strings.TrimSpace(heading))
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "#") {
			continue
		}
		title := strings.ToLower(strings.TrimSpace(strings.TrimLeft(trimmed, "#")))
		if title == target {
			return true
		}
	}
	return false
}

func reportProvenFindingsHaveEvidenceAnchors(text string) bool {
	body := markdownSectionBody(text, "Proven Findings")
	if strings.TrimSpace(body) == "" {
		return false
	}
	lowered := strings.ToLower(body)
	if strings.Contains(lowered, "no concrete defect was proven") {
		return true
	}
	checked := false
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		checked = true
		if !reportLineHasEvidenceAnchor(trimmed) || !reportLineHasRequiredProofForClaimKind(trimmed) {
			return false
		}
	}
	return checked
}

func reportLineHasEvidenceAnchor(line string) bool {
	lowered := strings.ToLower(line)
	if strings.Contains(lowered, "evidence anchor:") ||
		strings.Contains(lowered, "source:") ||
		strings.Contains(lowered, "observed in ") ||
		strings.Contains(lowered, "read ") ||
		strings.Contains(lowered, "go test") ||
		strings.Contains(lowered, "go build") {
		return true
	}
	for _, token := range []string{".go", ".md", ".json", ".yaml", ".yml", "go.mod", "package.json", "cmd/", "internal/", "frontend/", "docs/"} {
		if strings.Contains(lowered, token) {
			return true
		}
	}
	return false
}

func reportLineHasRequiredProofForClaimKind(line string) bool {
	lowered := strings.ToLower(line)
	if containsAnySubstring(lowered, "missing ci", "missing ci/cd", "no ci", "no ci/cd", "lack of ci", "lacks ci", "without ci", "pipeline configuration", "github action") {
		return containsAnySubstring(lowered, ".github", "workflow", "ci.yml", "ci.yaml", "directory scan", "file scan", "file listing", "rg ", "get-childitem")
	}
	if containsAnySubstring(lowered, "missing test", "no test", "lack of test", "lacks test", "without test", "test coverage") {
		return containsAnySubstring(lowered, "_test.go", "go test", "test output", "directory scan", "file scan", "file listing", "rg ", "get-childitem")
	}
	if containsAnySubstring(lowered, "does not exist", "latest stable", "current stable", "outdated", "newer release", "current release") {
		return containsAnySubstring(lowered, "official", "release note", "version check", "go env", "go version", "package registry", "source:")
	}
	return true
}

func containsAnySubstring(text string, tokens ...string) bool {
	for _, token := range tokens {
		if strings.Contains(text, token) {
			return true
		}
	}
	return false
}

func markdownSectionBody(text string, heading string) string {
	target := strings.ToLower(strings.TrimSpace(heading))
	lines := strings.Split(text, "\n")
	capturing := false
	body := []string{}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			title := strings.TrimSpace(strings.TrimLeft(trimmed, "#"))
			if capturing {
				break
			}
			if strings.EqualFold(title, target) {
				capturing = true
			}
			continue
		}
		if capturing {
			body = append(body, line)
		}
	}
	return strings.TrimSpace(strings.Join(body, "\n"))
}

func projectPurposeLine(readSummary string, synthesis string) string {
	if purpose := projectPurposeFromSynthesis(synthesis); purpose != "" {
		return purpose
	}
	if purpose := projectPurposeFromReadSummary(readSummary); purpose != "" {
		return purpose
	}
	return "Project purpose was not proven from the available evidence."
}

func reportMarkdownBlock(text string) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "No synthesis available."
	}
	if strings.HasPrefix(trimmed, "```markdown") {
		trimmed = strings.TrimPrefix(trimmed, "```markdown")
		trimmed = strings.TrimSpace(strings.TrimSuffix(trimmed, "```"))
	}
	return trimmed
}

func synthesisSummaryLine(text string) string {
	trimmed := reportMarkdownBlock(text)
	if trimmed == "" || trimmed == "No synthesis available." {
		return "analysis report generated"
	}
	if analysisReportLooksComplete(trimmed) {
		return "analysis report generated"
	}
	lines := strings.Split(trimmed, "\n")
	for _, line := range lines {
		s := strings.TrimSpace(line)
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		if len(s) > 120 {
			s = s[:120] + "..."
		}
		return s
	}
	return "analysis report generated"
}

func conciseReportLine(text string) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "No evidence captured."
	}
	if strings.Contains(trimmed, " Read ") || strings.Contains(trimmed, "Read ") {
		lines := reportReadSummaryItems(trimmed)
		if len(lines) > 0 {
			return fmt.Sprintf("Repository survey read %d evidence item(s); see Evidence Reviewed for details.", len(lines))
		}
	}
	return trimmed
}

func reportEvidenceList(plan planner.Plan, readSummary string, generatedSkillPath string, generatedSkillPreview string, invokedSkillName string, restored bool) string {
	return strings.Join(reportEvidenceListItems(plan, readSummary, generatedSkillPath, generatedSkillPreview, invokedSkillName, restored), "\n")
}

func compactReportEvidenceList(plan planner.Plan, readSummary string, generatedSkillPath string, generatedSkillPreview string, invokedSkillName string, restored bool, limit int) string {
	items := reportEvidenceListItems(plan, readSummary, generatedSkillPath, generatedSkillPreview, invokedSkillName, restored)
	if limit > 0 && len(items) > limit {
		omitted := len(items) - limit
		items = append(append([]string{}, items[:limit]...), fmt.Sprintf("- Evidence list compacted: %d additional item(s) omitted from the fallback report body.", omitted))
	}
	return strings.Join(items, "\n")
}

func reportEvidenceListItems(plan planner.Plan, readSummary string, generatedSkillPath string, generatedSkillPreview string, invokedSkillName string, restored bool) []string {
	items := []string{}
	readItems := reportReadSummaryItems(readSummary)
	if len(readItems) == 0 {
		items = append(items, "- "+conciseReportLine(readSummary))
	} else {
		for _, item := range readItems {
			items = append(items, "- "+item)
		}
	}
	items = append(items,
		fmt.Sprintf("- Avatars: %d", len(plan.Avatars)),
		fmt.Sprintf("- Workflow nodes: %d", len(plan.Nodes)),
	)
	if generatedSkillPath != "" {
		items = append(items, "- Generated skill candidate: "+generatedSkillPath)
	}
	if generatedSkillPreview != "" {
		items = append(items, "- Generated skill preview: "+generatedSkillPreview)
	}
	if invokedSkillName != "" {
		items = append(items, "- Invoked skill: "+invokedSkillName)
	}
	if restored {
		items = append(items, "- Continuation: restored transcript context")
	}
	return items
}

func reportReadSummaryItems(readSummary string) []string {
	trimmed := strings.TrimSpace(readSummary)
	if trimmed == "" {
		return nil
	}
	matches := regexp.MustCompile(`Read\s+[^.]+\.`).FindAllStringIndex(trimmed, -1)
	if len(matches) == 0 {
		return nil
	}
	items := make([]string, 0, len(matches))
	for index, match := range matches {
		start := match[0]
		end := len(trimmed)
		if index+1 < len(matches) {
			end = matches[index+1][0]
		}
		item := strings.TrimSpace(trimmed[start:end])
		item = strings.TrimSuffix(item, ".")
		if item == "" {
			continue
		}
		items = append(items, item+".")
	}
	return items
}

type evidenceCoverage struct {
	Total       int
	Docs        int
	Manifests   int
	Entrypoints int
	Source      int
	Config      int
	CI          int
	Other       int
}

func buildEvidenceCoverage(readSummary string) evidenceCoverage {
	coverage := evidenceCoverage{}
	for _, item := range reportReadSummaryItems(readSummary) {
		coverage.Total++
		switch evidenceCoverageBucket(item) {
		case "docs":
			coverage.Docs++
		case "manifests":
			coverage.Manifests++
		case "entrypoints":
			coverage.Entrypoints++
		case "source":
			coverage.Source++
		case "config":
			coverage.Config++
		case "ci":
			coverage.CI++
		default:
			coverage.Other++
		}
	}
	return coverage
}

func evidenceCoverageBucket(item string) string {
	lowered := strings.ToLower(filepath.ToSlash(readSummaryItemAnchor(item)))
	switch {
	case strings.Contains(lowered, ".github/") || strings.Contains(lowered, "github workflow") || strings.Contains(lowered, "ci"):
		return "ci"
	case strings.Contains(lowered, "go.mod") || strings.Contains(lowered, "go.sum") || strings.Contains(lowered, "package.json") || strings.Contains(lowered, "package-lock.json") || strings.Contains(lowered, "pyproject.toml") || strings.Contains(lowered, "cargo.toml") || strings.Contains(lowered, "requirements.txt"):
		return "manifests"
	case strings.Contains(lowered, "readme") || strings.Contains(lowered, "docs/") || strings.Contains(lowered, "plugin_dev") || strings.Contains(lowered, "plan.md") || strings.Contains(lowered, "milestone") || strings.Contains(lowered, "checklist") || strings.Contains(lowered, "guide") || strings.Contains(lowered, "discussion") || strings.Contains(lowered, "migration"):
		return "docs"
	case strings.Contains(lowered, "main.go") || strings.Contains(lowered, "app.go") || strings.Contains(lowered, "cmd/") || strings.Contains(lowered, "frontend/app.") || strings.Contains(lowered, "entrypoint signal"):
		return "entrypoints"
	case strings.Contains(lowered, "configs/") || strings.Contains(lowered, "config") || strings.Contains(lowered, ".yaml") || strings.Contains(lowered, ".yml") || strings.Contains(lowered, ".json"):
		return "config"
	case strings.Contains(lowered, ".go") || strings.Contains(lowered, ".js") || strings.Contains(lowered, ".ts") || strings.Contains(lowered, "package:") || strings.Contains(lowered, "top declarations:"):
		return "source"
	default:
		return "other"
	}
}

func evidenceCoverageSummaryLine(readSummary string) string {
	coverage := buildEvidenceCoverage(readSummary)
	return fmt.Sprintf("total=%d docs=%d manifests=%d entrypoints=%d source=%d config=%d ci=%d other=%d", coverage.Total, coverage.Docs, coverage.Manifests, coverage.Entrypoints, coverage.Source, coverage.Config, coverage.CI, coverage.Other)
}

func evidenceCoverageMarkdown(readSummary string) string {
	coverage := buildEvidenceCoverage(readSummary)
	lines := []string{
		"- " + evidenceCoverageSummaryLine(readSummary),
	}
	if coverage.Total == 0 {
		lines = append(lines, "- Coverage gap: no readable evidence item was captured.")
	} else {
		if coverage.Source == 0 {
			lines = append(lines, "- Coverage gap: no source-domain files were read.")
		}
		if coverage.Manifests == 0 {
			lines = append(lines, "- Coverage gap: no manifest/dependency files were read.")
		}
		if coverage.Entrypoints == 0 {
			lines = append(lines, "- Coverage gap: no entrypoint files were read.")
		}
		if coverage.Docs == 0 {
			lines = append(lines, "- Coverage gap: no project documentation files were read.")
		}
	}
	return strings.Join(lines, "\n")
}

func fallbackProjectSummary(readSummary string, synthesisStatus string) string {
	coverage := buildEvidenceCoverage(readSummary)
	items := reportReadSummaryItems(readSummary)
	lines := []string{
		"- Deterministic fallback report: final LLM synthesis did not complete, so conclusions are generated from repository read evidence only.",
		fmt.Sprintf("- Evidence shape: docs=%d manifests=%d entrypoints=%d source=%d config=%d ci=%d.", coverage.Docs, coverage.Manifests, coverage.Entrypoints, coverage.Source, coverage.Config, coverage.CI),
	}
	if strings.TrimSpace(synthesisStatus) != "" {
		lines = append(lines, "- Synthesis status: "+strings.TrimSpace(synthesisStatus))
	}
	switch {
	case coverage.Source > 0 && coverage.Manifests > 0 && coverage.Docs > 0:
		lines = append(lines, "- Read coverage is broad enough to infer the main architecture and workflow split, but not enough to prove defects without symbol-level checks and tests.")
	case coverage.Source > 0:
		lines = append(lines, "- Source evidence exists, but missing docs or manifests keep the review at a tentative implementation-summary level.")
	default:
		lines = append(lines, "- Source evidence is missing; this report cannot evaluate implementation quality.")
	}
	if containsAnyItem(items, "internal/manual") || containsAnyItem(items, "internal/auto") {
		if looksLikeWailsExcelEvidence(items) {
			lines = append(lines, "- The repository layout already exposes a manual workbook/SQL path and a task-driven automation path; the key question is whether their shared state and handoff are clean.")
		}
	}
	if containsAnyItem(items, "internal/output") || containsAnyItem(items, "internal/template") {
		if looksLikeWailsExcelEvidence(items) {
			lines = append(lines, "- Export/template code is present, so workbook write-back behavior is part of the product surface and deserves a direct path-level check.")
		}
	}
	if containsAnyItem(items, "internal/plugin") || containsAnyItem(items, "internal/plugins") {
		lines = append(lines, "- The plugin registry looks compile-time bound, so extension behavior should be checked for coupling and discoverability.")
	}
	if looksLikeWailsExcelEvidence(items) {
		lines = append(lines, "- Evidence-backed project shape: a Wails desktop app with an Excel import/SQL backend split and a task-driven automatic mode.")
	} else if coverage.Source == 0 && coverage.Manifests == 0 {
		lines = append(lines, "- No source/manifest evidence; if synthesis was unavailable, do not guess the product shape.")
	}
	return strings.Join(lines, "\n")
}

func looksLikeWailsExcelEvidence(items []string) bool {
	if containsAnyItem(items, "frontend/wailsjs") || containsAnyItem(items, "wails.json") {
		return true
	}
	return containsAnyItem(items, "internal/manual") && containsAnyItem(items, "internal/auto")
}

func fallbackImplementationSnapshot(readSummary string) string {
	items := reportReadSummaryItems(readSummary)
	lines := []string{}
	// F104: README.md / main.go / package.json alone must NOT imply a Wails
	// Excel workbench. Only emit product-shape claims from concrete evidence.
	if looksLikeWailsExcelEvidence(items) {
		lines = append(lines, "- Evidence-backed project shape: Wails desktop app with Excel/SQL split (wails.json / frontend/wailsjs / internal/manual+auto).")
	}
	if containsAnyItem(items, "README.md", "Readme_cn.md") && !looksLikeWailsExcelEvidence(items) {
		lines = append(lines, "- README was read; do not infer product shape from the filename alone.")
	}
	if containsAnyItem(items, "go.mod") {
		lines = append(lines, "- Backend/build stack: Go module evidence found; dependency context available from `go.mod`.")
		lines = append(lines, "- Evidence anchor: go.mod is the build/dependency source of truth.")
	}
	if containsAnyItem(items, "pyproject.toml", "requirements.txt", "setup.py") {
		lines = append(lines, "- Python project manifest evidence found.")
	}
	if containsAnyItem(items, "package.json") && !looksLikeWailsExcelEvidence(items) {
		lines = append(lines, "- Node/package manifest evidence found (`package.json`).")
	}
	if looksLikeWailsExcelEvidence(items) && containsAnyItem(items, "frontend", "app.js", "package.json") {
		lines = append(lines, "- Frontend stack: Wails frontend/backend boundary is visible.")
		lines = append(lines, "- Evidence anchor: frontend/app.js and frontend/wailsjs/go/main/App.js show the UI/runtime boundary.")
	}
	if containsAnyItem(items, "Cargo.toml") {
		lines = append(lines, "- Rust crate evidence found (`Cargo.toml`).")
	}
	if containsAnyItem(items, "main.go") && !looksLikeWailsExcelEvidence(items) {
		lines = append(lines, "- Entrypoint: `main.go` exists.")
	}
	if looksLikeWailsExcelEvidence(items) && containsAnyItem(items, "main.go") {
		lines = append(lines, "- Entrypoint: `main.go` exists and exposes startup wiring evidence.")
		lines = append(lines, "- Evidence anchor: main.go owns startup wiring for the desktop app boundary.")
	}
	if looksLikeWailsExcelEvidence(items) && containsAnyItem(items, "internal/manual") {
		lines = append(lines, "- Manual workflow: `internal/manual` evidence indicates workbook/session and SQL execution logic.")
		lines = append(lines, "- Evidence anchor: internal/manual/session.go is the path to trace for shared session behavior.")
	}
	if looksLikeWailsExcelEvidence(items) && containsAnyItem(items, "internal/auto") {
		lines = append(lines, "- Automatic workflow: `internal/auto` evidence indicates task-driven migration/automation logic.")
		lines = append(lines, "- Evidence anchor: internal/auto/runner.go is the path to trace for task orchestration and remediation flow.")
	}
	if containsAnyItem(items, "internal/plugin") || containsAnyItem(items, "internal/plugins") {
		lines = append(lines, "- Extension model: plugin interface/registry evidence exists; deeper review needed to judge extensibility quality.")
		lines = append(lines, "- Evidence anchor: internal/plugin/registry.go and internal/plugins/registry.go show compile-time registry wiring.")
	}
	if looksLikeWailsExcelEvidence(items) && (containsAnyItem(items, "internal/output") || containsAnyItem(items, "internal/template")) {
		lines = append(lines, "- Output layer: Excel/template output helpers were read; correctness still needs tests or symbol-level inspection.")
		lines = append(lines, "- Evidence anchor: internal/output/export.go and internal/template/* control workbook write-back behavior.")
	}
	if looksLikeWailsExcelEvidence(items) && containsAnyItem(items, "frontend/app.js", "frontend/wailsjs/go/main/App.js", "main.go") {
		lines = append(lines, "- Implementation split: frontend bootstrap and Go entrypoint evidence point to a Wails app boundary rather than a monolith.")
		lines = append(lines, "- Evidence anchor: frontend/app.js, frontend/wailsjs/go/main/App.js, and main.go define the app boundary.")
	}
	if len(lines) == 0 {
		return "- No implementation snapshot could be derived from the current read evidence."
	}
	return strings.Join(lines, "\n")
}

func projectPurposeFromSynthesis(synthesis string) string {
	trimmed := reportMarkdownBlock(synthesis)
	if trimmed == "" || trimmed == "No synthesis available." {
		return ""
	}
	lines := strings.Split(trimmed, "\n")
	inPurpose := false
	for _, line := range lines {
		current := strings.TrimSpace(line)
		if current == "" {
			continue
		}
		lowered := strings.ToLower(strings.TrimLeft(current, "# "))
		if strings.HasPrefix(current, "#") {
			inPurpose = strings.Contains(lowered, "project purpose") || strings.Contains(lowered, "purpose")
			continue
		}
		if inPurpose {
			purpose := strings.TrimPrefix(current, "- ")
			if looksLikeReportPurpose(purpose) {
				return purpose
			}
			return ""
		}
	}
	if !strings.Contains(trimmed, "Read ") {
		purpose := conciseFirstSentence(trimmed)
		if looksLikeReportPurpose(purpose) {
			return purpose
		}
	}
	return ""
}

func projectPurposeFromReadSummary(readSummary string) string {
	for _, item := range reportReadSummaryItems(readSummary) {
		if !strings.Contains(strings.ToLower(item), "readme") {
			continue
		}
		if purpose := extractMarkdownPurposeFromReadItem(item); purpose != "" {
			return purpose
		}
	}
	items := reportReadSummaryItems(readSummary)
	if len(items) > 0 {
		return fmt.Sprintf("Project purpose could not be safely inferred from %d read summary item(s).", len(items))
	}
	return ""
}

func extractMarkdownPurposeFromReadItem(item string) string {
	for _, marker := range []string{"Key points:", "Title:"} {
		index := strings.Index(item, marker)
		if index < 0 {
			continue
		}
		value := strings.TrimSpace(item[index+len(marker):])
		if value == "" {
			continue
		}
		if semi := strings.Index(value, ";"); semi >= 0 {
			value = strings.TrimSpace(value[:semi])
		}
		value = strings.TrimSuffix(value, ".")
		if value == "" {
			return ""
		}
		purpose := conciseFirstSentence(value)
		if looksLikeReportPurpose(purpose) {
			return purpose
		}
	}
	return ""
}

func looksLikeReportPurpose(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false
	}
	lowered := strings.ToLower(trimmed)
	if strings.Contains(lowered, "?") {
		return false
	}
	for _, token := range []string{
		"analyze ",
		"inspect ",
		"fix ",
		"build ",
		"please ",
		"why ",
		"what ",
		"how ",
		"local deterministic synthesis",
		"runtime continuity",
		"request focus",
		"no synthesis available",
		"分析",
		"为什么",
		"如何",
		"怎么",
		"请",
	} {
		if strings.Contains(lowered, token) {
			return false
		}
	}
	return true
}

func conciseFirstSentence(text string) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return ""
	}
	for _, sep := range []string{". ", "\n"} {
		if index := strings.Index(trimmed, sep); index > 0 {
			trimmed = strings.TrimSpace(trimmed[:index+1])
			break
		}
	}
	if len(trimmed) > 240 {
		trimmed = strings.TrimSpace(trimmed[:240]) + "..."
	}
	return trimmed
}

func reportFindingsSections(readSummary string, finalSummary string, synthesis string) []string {
	proven := reportProvenFindings(readSummary)
	risks := reportEvidenceTiedRisks(readSummary, finalSummary, synthesis)
	openQuestions := reportOpenQuestions(readSummary, finalSummary, synthesis)
	if len(proven) == 0 {
		proven = append(proven, "- No concrete defect was proven from the current survey.")
	}
	sections := []string{
		"## Proven Findings",
		strings.Join(proven, "\n"),
		"## Evidence-Tied Risks / Needs Verification",
	}
	if len(risks) == 0 {
		sections = append(sections, "- No tentative risk was identified by the current fallback checks.")
	} else {
		sections = append(sections, strings.Join(risks, "\n"))
	}
	sections = append(sections,
		"## Open Questions",
	)
	if len(openQuestions) == 0 {
		sections = append(sections, "- No open question remained after the current fallback checks.")
	} else {
		sections = append(sections, strings.Join(openQuestions, "\n"))
	}
	return sections
}

type missingPathSourceEvidence struct {
	Path            string
	Anchor          string
	FunctionSignals string
}

func sourceBackedMissingPathEvidence(readSummary string) []missingPathSourceEvidence {
	items := reportReadSummaryItems(readSummary)
	missing := declaredMissingRepositoryPaths(readSummary)
	evidence := []missingPathSourceEvidence{}
	for _, path := range missing {
		slashPath := filepath.ToSlash(path)
		for _, item := range items {
			pathSignals := readSummaryItemField(item, "Path signals:")
			if pathSignals == "" || !strings.Contains(pathSignals, slashPath) {
				continue
			}
			anchor := readSummaryItemAnchor(item)
			functionSignals := readSummaryItemField(item, "Function signals:")
			evidence = append(evidence, missingPathSourceEvidence{
				Path:            slashPath,
				Anchor:          anchor,
				FunctionSignals: functionSignals,
			})
			break
		}
	}
	return evidence
}

func reportProvenFindings(readSummary string) []string {
	findings := []string{}
	for _, result := range focusedVerificationResultParts(readSummary) {
		if !strings.Contains(strings.ToUpper(result), "=> FAIL") {
			continue
		}
		findings = append(findings, fmt.Sprintf("- Focused verifier failed: %s. Evidence anchor: Verification Results.", result))
	}
	for _, evidence := range sourceBackedMissingPathEvidence(readSummary) {
		functionDetail := ""
		if evidence.FunctionSignals != "" {
			functionDetail = fmt.Sprintf(" Function evidence: %s.", evidence.FunctionSignals)
		}
		findings = append(findings, fmt.Sprintf("- `%s` is missing while `%s` references it in source path signals.%s Evidence anchor: %s.", evidence.Path, evidence.Anchor, functionDetail, evidence.Anchor))
	}
	return dedupeReportLines(findings)
}

func fallbackConclusionLines(readSummary string, synthesisStatus string) []string {
	coverage := buildEvidenceCoverage(readSummary)
	items := reportReadSummaryItems(readSummary)
	lines := []string{}
	if coverage.Source > 0 && coverage.Manifests > 0 && coverage.Docs > 0 {
		lines = append(lines, "- The repository has enough docs, manifests, and source to describe architecture, but not enough to prove defects without a deeper pass.")
		lines = append(lines, "- This is enough to give a credible architecture read, but not enough to justify hard bug claims from summaries alone.")
	} else if coverage.Source > 0 {
		lines = append(lines, "- Source evidence exists, but missing docs or manifests keep the review at an implementation-summary level.")
	} else if coverage.Total > 0 {
		lines = append(lines, "- The run captured only partial evidence, so any architecture claim should stay tentative.")
	}
	if containsAnyItem(items, "internal/manual", "internal/auto") {
		lines = append(lines, "- The split between manual workbook/SQL handling and task-driven automation is visible in the repository layout.")
		lines = append(lines, "- That split suggests the codebase is organized around two user flows, not a single generic script entrypoint.")
		lines = append(lines, "- Evidence anchor: internal/manual/session.go and internal/auto/runner.go are the main files to prove whether the handoff is clean.")
	}
	if containsAnyItem(items, "main.go") && containsAnyItem(items, "frontend/app.js", "frontend/app.ts", "frontend/app.tsx") {
		lines = append(lines, "- Startup wiring spans frontend bootstrap and a Go entrypoint, which fits a desktop app boundary and deserves route-level inspection.")
		lines = append(lines, "- Evidence anchor: main.go plus frontend/app.js / frontend/wailsjs/go/main/App.js define the startup path.")
	}
	if strings.Contains(strings.ToLower(synthesisStatus), "failed") || strings.Contains(strings.ToLower(synthesisStatus), "timeout") {
		lines = append(lines, "- The LLM synthesis timed out, so this report is a deterministic fallback rather than a full semantic review.")
		lines = append(lines, "- Because synthesis failed, keep defect claims narrow and treat the rest as review leads, not conclusions.")
	}
	if len(lines) == 0 {
		lines = append(lines, "- No stronger conclusion could be proven from the current evidence shape.")
	}
	return lines
}

func reportEvidenceTiedRisks(readSummary string, finalSummary string, synthesis string) []string {
	items := reportReadSummaryItems(readSummary)
	anchors := evidenceAnchorsFromItems(items)
	risks := []string{}
	if !containsAnyItem(items, "go.mod", "package.json", "pyproject.toml", "Cargo.toml") {
		risks = append(risks, "- "+anchorWithFallback(anchors, "Manifest evidence is thin; dependency and build assumptions need verification."))
	}
	if !containsAnyItem(items, "main.go", "cmd/", "frontend/app.js", "frontend/app.ts", "frontend/app.tsx") {
		risks = append(risks, "- "+anchorWithFallback(anchors, "Entrypoint evidence is thin; runtime wiring and startup flow still need verification."))
	}
	if !containsAnyItem(items, "README.md", "docs/") {
		risks = append(risks, "- "+anchorWithFallback(anchors, "Documentation evidence is thin; project purpose and workflow intent are still weakly supported."))
	}
	if !strings.Contains(strings.ToLower(finalSummary), "issue") && !strings.Contains(strings.ToLower(synthesis), "issue") {
		risks = append(risks, "- "+anchorWithFallback(anchors, "Synthesizer output did not name a concrete defect; issue proof remains tentative."))
	}
	if strings.Contains(strings.ToLower(finalSummary), "synthesis fallback used") || strings.Contains(strings.ToLower(synthesis), "synthesis status") || strings.Contains(strings.ToLower(synthesis), "did not complete") {
		risks = append(risks, "- "+anchorWithFallback(anchors, "LLM synthesis did not complete or fell back; this report stays evidence-limited."))
	}
	if containsAnyItem(items, "internal/manual", "internal/auto") {
		risks = append(risks, "- "+anchorWithFallback(anchors, "The manual/auto split still needs code-level tracing to confirm state sharing and error propagation boundaries."))
		risks = append(risks, "- Evidence anchor: internal/manual/session.go and internal/auto/runner.go should be read together before any stronger claim about shared state.")
	}
	return dedupeReportLines(risks)
}

func reportOpenQuestions(readSummary string, finalSummary string, synthesis string) []string {
	items := reportReadSummaryItems(readSummary)
	anchors := evidenceAnchorsFromItems(items)
	questions := []string{}
	if !containsAnyItem(items, "main.go", "cmd/") {
		questions = append(questions, "- "+anchorWithFallback(anchors, "Which entrypoint path owns startup orchestration?"))
	}
	if !containsAnyItem(items, "go.mod", "package.json", "Cargo.toml", "pyproject.toml") {
		questions = append(questions, "- "+anchorWithFallback(anchors, "Which build/dependency manifest is the source of truth for this workspace?"))
	}
	if strings.Contains(strings.ToLower(finalSummary), "synthesis fallback used") || strings.Contains(strings.ToLower(synthesis), "did not complete") {
		questions = append(questions, "- "+anchorWithFallback(anchors, "Which concrete files need a deeper symbol-level pass before findings can be promoted?"))
	}
	if containsAnyItem(items, "internal/manual", "internal/auto") {
		questions = append(questions, "- "+anchorWithFallback(anchors, "Does the workbook/session boundary actually stay shared across manual mode and automatic tasks?"))
	}
	return dedupeReportLines(questions)
}

func evidenceAnchorsFromItems(items []string) []string {
	anchors := []string{}
	for _, item := range items {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" {
			continue
		}
		if idx := strings.Index(trimmed, "Read "); idx >= 0 {
			trimmed = strings.TrimSpace(trimmed[idx+len("Read "):])
		}
		if marker := strings.Index(strings.ToLower(trimmed), " successfully"); marker > 0 {
			trimmed = strings.TrimSpace(trimmed[:marker])
		} else if fields := strings.Fields(trimmed); len(fields) > 0 {
			trimmed = strings.Trim(fields[0], ".,;:")
		}
		anchors = append(anchors, trimmed)
	}
	return anchors
}

func anchorWithFallback(anchors []string, sentence string) string {
	if len(anchors) == 0 {
		return sentence
	}
	limit := 3
	if len(anchors) < limit {
		limit = len(anchors)
	}
	return fmt.Sprintf("%s Evidence anchor: %s.", sentence, strings.Join(anchors[:limit], ", "))
}

func containsAnyItem(items []string, tokens ...string) bool {
	for _, item := range items {
		lowered := strings.ToLower(item)
		slashed := strings.ToLower(filepath.ToSlash(item))
		for _, token := range tokens {
			loweredToken := strings.ToLower(filepath.ToSlash(token))
			if strings.Contains(lowered, loweredToken) || strings.Contains(slashed, loweredToken) {
				return true
			}
		}
	}
	return false
}

func dedupeReportLines(lines []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		out = append(out, trimmed)
	}
	return out
}

func reportConfidenceSection(readSummary string, synthesis string) string {
	loweredSummary := strings.ToLower(readSummary)
	loweredSynthesis := strings.ToLower(synthesis)
	switch {
	case strings.Contains(loweredSummary, "no repository evidence was found"):
		return "- Low: no source, manifest, or project documentation evidence was available."
	case strings.Contains(loweredSummary, "package") && strings.Contains(loweredSummary, "module"):
		return "- Medium: repository manifests and source summaries were reviewed, but full semantic execution was not performed."
	case strings.Contains(loweredSynthesis, "issue") || strings.Contains(loweredSynthesis, "risk"):
		return "- Medium: findings are grounded in read evidence and LLM synthesis, but still need verifier or code-level confirmation."
	default:
		return "- Low to medium: output is based on available file summaries."
	}
}

func reportReviewLeads(readSummary string) string {
	items := reportReadSummaryItems(readSummary)
	leads := []string{}
	if containsAnyItem(items, "internal/manual/session.go") && containsAnyItem(items, "internal/auto/runner.go") {
		leads = append(leads, "- `internal/manual/session.go` + `internal/auto/runner.go`: verify whether automatic tasks read the same workbook/session state that manual SQL uses, and where errors cross that boundary.")
	}
	if containsAnyItem(items, "internal/output/export.go") {
		leads = append(leads, "- `internal/output/export.go`: verify workbook write-back cell addressing, header inclusion, append-column behavior, and error messages.")
	}
	if containsAnyItem(items, "internal/plugin/registry.go", "internal/plugins/registry.go") {
		leads = append(leads, "- `internal/plugin/registry.go` + `internal/plugins/registry.go`: verify plugin discoverability, compile-time coupling, duplicate names, and approval boundaries.")
	}
	if containsAnyItem(items, "frontend/app.js") && containsAnyItem(items, "main.go") {
		leads = append(leads, "- `frontend/app.js` + `main.go`: verify the first user-input route, backend call shape, and how failures surface to the desktop UI.")
	}
	if containsAnyItem(items, "go.mod") {
		leads = append(leads, "- `go.mod`: verify declared Go/Wails/Excel dependencies against documented setup and test commands.")
	}
	if len(leads) == 0 {
		return "- No file-level review lead could be generated from the current evidence; expand the source survey first."
	}
	return strings.Join(dedupeReportLines(leads), "\n")
}

func reportVerificationLeads(readSummary string) string {
	items := reportReadSummaryItems(readSummary)
	leads := []string{}
	for _, evidence := range sourceBackedMissingPathEvidence(readSummary) {
		functionDetail := strings.TrimSpace(evidence.FunctionSignals)
		if functionDetail == "" {
			functionDetail = "referencing function"
		}
		leads = append(leads, fmt.Sprintf("- source-backed gap: `%s` referenced by `%s` (`%s`). Add or run a targeted test that exercises the missing-file path and asserts the user-facing error/fallback.", evidence.Path, evidence.Anchor, functionDetail))
	}
	add := func(sourcePath string, testPath string, command string, focus string) {
		if !containsAnyItem(items, sourcePath) {
			return
		}
		if containsAnyItem(items, testPath) {
			leads = append(leads, fmt.Sprintf("- `%s`: run `%s` to verify %s. Evidence anchor: %s.", sourcePath, command, focus, testPath))
			return
		}
		leads = append(leads, fmt.Sprintf("- `%s`: no matching `%s` evidence was captured; add a focused test for %s.", sourcePath, testPath, focus))
	}
	add("internal/manual/session.go", "internal/manual/session_test.go", "go test ./internal/manual -run TestSessionManager", "workbook load, SQL execution, and session error behavior")
	add("internal/output/export.go", "internal/output/export_test.go", "go test ./internal/output -run TestWriteQueryToWorkbook", "Excel write-back, append/header layout, and overwrite behavior")
	add("internal/auto/runner.go", "internal/auto/runner_test.go", "go test ./internal/auto -run TestRunner", "task registration, unknown-task errors, and runner orchestration")
	add("internal/preset/store.go", "internal/preset/store_test.go", "go test ./internal/preset -run Test", "preset load/save validation and JSON error behavior")
	if len(leads) == 0 {
		return "- No runnable verifier lead could be derived from the current source/test evidence."
	}
	return strings.Join(dedupeReportLines(leads), "\n")
}

func focusedVerificationChecksFromReadSummary(readSummary string) []verification.CommandCheck {
	items := reportReadSummaryItems(readSummary)
	checks := []verification.CommandCheck{}
	add := func(sourcePath string, testPath string, name string, command []string, expected string) {
		if len(checks) >= 4 {
			return
		}
		if !containsAnyItem(items, sourcePath) || !containsAnyItem(items, testPath) {
			return
		}
		checks = append(checks, verification.CommandCheck{
			Name:     name,
			Command:  append([]string(nil), command...),
			Expected: expected,
		})
	}
	add("internal/manual/session.go", "internal/manual/session_test.go", "manual session focused tests", []string{"go", "test", "./internal/manual", "-run", "TestSessionManager"}, "Manual workbook load, SQL execution, and session error behavior should pass focused tests.")
	add("internal/output/export.go", "internal/output/export_test.go", "output workbook focused tests", []string{"go", "test", "./internal/output", "-run", "TestWriteQueryToWorkbook"}, "Excel write-back, append/header layout, and overwrite behavior should pass focused tests.")
	add("internal/auto/runner.go", "internal/auto/runner_test.go", "auto runner focused tests", []string{"go", "test", "./internal/auto", "-run", "TestRunner"}, "Task registration, unknown-task errors, and runner orchestration should pass focused tests.")
	add("internal/preset/store.go", "internal/preset/store_test.go", "preset store focused tests", []string{"go", "test", "./internal/preset", "-run", "Test"}, "Preset load/save validation and JSON error behavior should pass focused tests.")
	return checks
}

func runFocusedVerificationLeads(ctx context.Context, workingDir string, executor verification.Executor, readSummary string) string {
	checks := focusedVerificationChecksFromReadSummary(readSummary)
	if len(checks) == 0 {
		return ""
	}
	report := verification.Report{Verdict: verification.VerdictPass}
	passCount := 0
	partialCount := 0
	failCount := 0
	for _, check := range checks {
		verifyCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
		checkReport := verification.NewRunner(workingDir, executor, []verification.CommandCheck{check}).Run(verifyCtx)
		cancel()
		report.Checks = append(report.Checks, checkReport.Checks...)
		report.Warnings = append(report.Warnings, checkReport.Warnings...)
		switch checkReport.Verdict {
		case verification.VerdictFail:
			failCount++
		case verification.VerdictPartial:
			partialCount++
		default:
			passCount++
		}
	}
	switch {
	case failCount > 0:
		report.Verdict = verification.VerdictFail
	case partialCount > 0:
		report.Verdict = verification.VerdictPartial
	default:
		report.Verdict = verification.VerdictPass
	}
	report.Summary = fmt.Sprintf("Verification finished with %s. %d passed, %d partial, %d failed.", report.Verdict, passCount, partialCount, failCount)
	return formatFocusedVerificationEvidence(report)
}

func formatFocusedVerificationEvidence(report verification.Report) string {
	lines := []string{"Read focused verification results successfully."}
	resultParts := []string{}
	for _, check := range report.Checks {
		actual := strings.TrimSpace(check.Actual)
		if actual == "" {
			actual = "no actual result text"
		}
		part := fmt.Sprintf("`%s` => %s (%s)", check.CommandRun, check.Result, actual)
		if output := verificationOutputExcerpt(check.OutputObserved, 3); output != "" {
			part += " output: " + output
		}
		resultParts = append(resultParts, part)
	}
	if len(resultParts) > 0 {
		lines = append(lines, "Focused verification results: "+strings.Join(resultParts, " || ")+".")
	}
	if strings.TrimSpace(report.Summary) != "" {
		lines = append(lines, "Focused verification summary: "+strings.TrimSpace(report.Summary))
	}
	for _, warning := range report.Warnings {
		if trimmed := strings.TrimSpace(warning); trimmed != "" {
			lines = append(lines, "Focused verification warning: "+trimmed)
		}
	}
	return strings.Join(lines, " ")
}

func verificationOutputExcerpt(output string, limit int) string {
	if limit <= 0 {
		limit = 1
	}
	lines := []string{}
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			lines = append(lines, trimmed)
			if len(lines) >= limit {
				break
			}
		}
	}
	return strings.Join(lines, " | ")
}

func appendReadSummaryEvidence(readSummary string, evidence string) string {
	trimmedEvidence := strings.TrimSpace(evidence)
	if trimmedEvidence == "" {
		return strings.TrimSpace(readSummary)
	}
	trimmedSummary := strings.TrimSpace(readSummary)
	if trimmedSummary == "" {
		return trimmedEvidence
	}
	return trimmedSummary + " " + trimmedEvidence
}

// buildPostBuildDiskInventory lists source/config paths present after Builder (V5).
func buildPostBuildDiskInventory(wd string) string {
	var found []string
	roots := []string{"app", "cmd", "src", "internal", "pkg", "tests", "migrations"}
	files := []string{"requirements.txt", "go.mod", "pyproject.toml", "alembic.ini", ".env.example"}
	for _, f := range files {
		if _, err := os.Stat(filepath.Join(wd, f)); err == nil {
			found = append(found, f)
		}
	}
	extOK := map[string]bool{".py": true, ".go": true, ".ts": true, ".tsx": true, ".js": true, ".sql": true}
	for _, root := range roots {
		base := filepath.Join(wd, root)
		_ = filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
			if err != nil || info == nil || info.IsDir() {
				return nil
			}
			if !extOK[strings.ToLower(filepath.Ext(info.Name()))] {
				return nil
			}
			rel, relErr := filepath.Rel(wd, path)
			if relErr != nil {
				return nil
			}
			found = append(found, filepath.ToSlash(rel))
			if len(found) >= 40 {
				return filepath.SkipAll
			}
			return nil
		})
		if len(found) >= 40 {
			break
		}
	}
	if len(found) == 0 {
		return ""
	}
	return "disk_inventory: " + strings.Join(found, ", ")
}

func extractDiskInventoryLine(readSummary string) string {
	const prefix = "disk_inventory: "
	if i := strings.Index(readSummary, prefix); i >= 0 {
		rest := readSummary[i+len(prefix):]
		if j := strings.Index(rest, " Builder changed_files:"); j >= 0 {
			rest = rest[:j]
		}
		return strings.TrimSpace(rest)
	}
	return ""
}

func extractChangedFilesLine(readSummary string) string {
	const prefix = "Builder changed_files: "
	if i := strings.Index(readSummary, prefix); i >= 0 {
		return strings.TrimSpace(readSummary[i+len(prefix):])
	}
	return ""
}

func extractThisRunWroteSourcesLine(readSummary string) string {
	const prefix = "THIS RUN WROTE IMPLEMENTATION SOURCES: "
	i := strings.Index(readSummary, prefix)
	if i < 0 {
		return ""
	}
	rest := strings.TrimSpace(readSummary[i+len(prefix):])
	if j := strings.Index(rest, ". Prior Delivery"); j >= 0 {
		rest = strings.TrimSpace(rest[:j])
	}
	return rest
}

func reportVerificationResults(readSummary string) string {
	results := focusedVerificationResultParts(readSummary)
	summary := focusedVerificationSummary(readSummary)
	warnings := focusedVerificationWarnings(readSummary)
	if len(results) == 0 && summary == "" && len(warnings) == 0 {
		return "- Focused verifier was not run; no runnable source/test lead was available from current evidence."
	}
	lines := []string{}
	for _, result := range results {
		lines = append(lines, "- "+result)
	}
	if summary != "" {
		lines = append(lines, "- summary: "+summary)
	}
	for _, warning := range warnings {
		lines = append(lines, "- warning: "+warning)
	}
	return strings.Join(lines, "\n")
}

type focusedVerificationFailure struct {
	Command      string
	Result       string
	Output       string
	TestName     string
	TestLocation string
	PackagePath  string
}

func focusedVerificationFailures(readSummary string) []focusedVerificationFailure {
	failures := []focusedVerificationFailure{}
	for _, result := range focusedVerificationResultParts(readSummary) {
		if !strings.Contains(strings.ToUpper(result), "=> FAIL") {
			continue
		}
		failure := focusedVerificationFailure{
			Command: strings.TrimSpace(firstBacktickedValue(result)),
			Result:  strings.TrimSpace(result),
		}
		if outputIndex := strings.Index(result, " output: "); outputIndex >= 0 {
			failure.Output = strings.TrimSpace(result[outputIndex+len(" output: "):])
		}
		failure.PackagePath = packagePathFromGoTestCommand(failure.Command)
		failure.TestName = testNameFromVerificationOutput(failure.Output)
		failure.TestLocation = testLocationFromVerificationOutput(failure.Output, failure.PackagePath)
		failures = append(failures, failure)
	}
	return failures
}

func firstBacktickedValue(text string) string {
	start := strings.Index(text, "`")
	if start < 0 {
		return ""
	}
	rest := text[start+1:]
	end := strings.Index(rest, "`")
	if end < 0 {
		return ""
	}
	return rest[:end]
}

func packagePathFromGoTestCommand(command string) string {
	fields := strings.Fields(command)
	for index, field := range fields {
		if field == "test" && index+1 < len(fields) {
			return strings.TrimSpace(fields[index+1])
		}
	}
	return ""
}

func testNameFromVerificationOutput(output string) string {
	re := regexp.MustCompile(`--- FAIL:\s+([^\s(]+)`)
	if match := re.FindStringSubmatch(output); len(match) > 1 {
		return match[1]
	}
	return ""
}

func testLocationFromVerificationOutput(output string, packagePath string) string {
	re := regexp.MustCompile(`([A-Za-z0-9_./\\-]+_test\.go:\d+)`)
	if match := re.FindStringSubmatch(output); len(match) > 1 {
		location := filepath.ToSlash(match[1])
		if strings.Contains(location, "/") || packagePath == "" {
			return location
		}
		pkg := strings.TrimPrefix(filepath.ToSlash(packagePath), "./")
		if pkg == "" || strings.HasPrefix(pkg, "-") {
			return location
		}
		return pkg + "/" + location
	}
	return ""
}

func reportRemediationLeads(readSummary string) string {
	failures := focusedVerificationFailures(readSummary)
	if len(failures) == 0 {
		return "- No verifier-backed remediation lead was generated; no focused verifier failure was observed."
	}
	lines := []string{}
	for _, failure := range failures {
		target := focusedFailureSourceTarget(readSummary, failure)
		testName := failure.TestName
		if testName == "" {
			testName = "the failing test"
		}
		location := failure.TestLocation
		if location == "" {
			location = "the failing test output"
		}
		lines = append(lines, fmt.Sprintf("- Inspect `%s` from `%s`: `%s` failed in `%s`. Compare the expected value and actual output in the assertion, then adjust the implementation or test contract. Source target: `%s`.", location, failure.Command, testName, location, target))
	}
	return strings.Join(dedupeReportLines(lines), "\n")
}

func focusedFailureSourceTarget(readSummary string, failure focusedVerificationFailure) string {
	items := reportReadSummaryItems(readSummary)
	for _, candidate := range focusedFailureSourceCandidates(failure) {
		if item := readSummaryItemForPath(items, candidate); item != "" {
			if functions := readSummaryItemField(item, "Function signals:"); functions != "" {
				return fmt.Sprintf("%s (%s)", filepath.ToSlash(candidate), functions)
			}
			return filepath.ToSlash(candidate)
		}
	}
	if failure.PackagePath != "" {
		return failure.PackagePath
	}
	return "the failing package"
}

func focusedFailureSourceCandidates(failure focusedVerificationFailure) []string {
	candidates := []string{}
	if failure.TestLocation != "" {
		location := strings.Split(filepath.ToSlash(failure.TestLocation), ":")[0]
		if strings.HasSuffix(location, "_test.go") {
			candidates = append(candidates, strings.TrimSuffix(location, "_test.go")+".go")
		}
	}
	switch filepath.ToSlash(strings.TrimSpace(failure.PackagePath)) {
	case "./internal/manual", "internal/manual":
		candidates = append(candidates, "internal/manual/session.go")
	case "./internal/output", "internal/output":
		candidates = append(candidates, "internal/output/export.go")
	case "./internal/auto", "internal/auto":
		candidates = append(candidates, "internal/auto/runner.go")
	case "./internal/preset", "internal/preset":
		candidates = append(candidates, "internal/preset/store.go")
	}
	return dedupeCandidateStrings(candidates)
}

func dedupeCandidateStrings(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(filepath.ToSlash(trimmed))
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, trimmed)
	}
	return out
}

func readSummaryItemForPath(items []string, path string) string {
	target := strings.ToLower(filepath.ToSlash(strings.TrimSpace(path)))
	if target == "" {
		return ""
	}
	for _, item := range items {
		anchor := strings.ToLower(filepath.ToSlash(readSummaryItemAnchor(item)))
		if anchor == target || strings.Contains(strings.ToLower(filepath.ToSlash(item)), target) {
			return item
		}
	}
	return ""
}

func focusedFailureCoversPath(readSummary string, path string) bool {
	target := strings.Trim(strings.ToLower(filepath.ToSlash(path)), "/")
	if target == "" {
		return false
	}
	for _, failure := range focusedVerificationFailures(readSummary) {
		for _, candidate := range append(focusedFailureSourceCandidates(failure), failure.PackagePath) {
			normalized := strings.TrimPrefix(strings.Trim(strings.ToLower(filepath.ToSlash(candidate)), "/"), "./")
			if normalized == target || strings.HasPrefix(normalized, target+"/") || strings.HasPrefix(target, normalized+"/") {
				return true
			}
		}
	}
	return false
}

func focusedVerificationFailureSummary(readSummary string) string {
	failures := focusedVerificationFailures(readSummary)
	if len(failures) == 0 {
		return ""
	}
	first := failures[0]
	location := first.TestLocation
	if location == "" {
		location = "unknown test location"
	}
	testName := first.TestName
	if testName == "" {
		testName = "focused verifier"
	}
	command := first.Command
	if command == "" {
		command = "focused verifier command"
	}
	return fmt.Sprintf("Focused verifier failure: `%s` failed at `%s` (`%s`).", command, location, testName)
}

func focusedVerificationResultParts(readSummary string) []string {
	body := betweenReportMarkers(readSummary, "Focused verification results:", "Focused verification summary:")
	if body == "" {
		return nil
	}
	body = strings.TrimSpace(strings.TrimSuffix(body, "."))
	parts := []string{}
	for _, part := range strings.Split(body, " || ") {
		trimmed := strings.TrimSpace(strings.TrimSuffix(part, "."))
		if trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return parts
}

func focusedVerificationSummary(readSummary string) string {
	summary := betweenReportMarkers(readSummary, "Focused verification summary:", "Focused verification warning:")
	return strings.TrimSpace(strings.TrimSuffix(summary, "."))
}

func focusedVerificationWarnings(readSummary string) []string {
	warnings := []string{}
	remaining := readSummary
	for {
		idx := strings.Index(remaining, "Focused verification warning:")
		if idx < 0 {
			break
		}
		remaining = remaining[idx+len("Focused verification warning:"):]
		next := strings.Index(remaining, "Focused verification warning:")
		part := remaining
		if next >= 0 {
			part = remaining[:next]
			remaining = remaining[next:]
		} else {
			remaining = ""
		}
		if trimmed := strings.TrimSpace(strings.TrimSuffix(part, ".")); trimmed != "" {
			warnings = append(warnings, trimmed)
		}
		if remaining == "" {
			break
		}
	}
	return warnings
}

func betweenReportMarkers(text string, startMarker string, endMarker string) string {
	start := strings.Index(text, startMarker)
	if start < 0 {
		return ""
	}
	body := text[start+len(startMarker):]
	if endMarker != "" {
		if end := strings.Index(body, endMarker); end >= 0 {
			body = body[:end]
		}
	}
	return strings.TrimSpace(body)
}

func reportCandidateRisksWithEvidence(readSummary string) string {
	items := reportReadSummaryItems(readSummary)
	risks := []string{}
	for _, item := range items {
		anchor := readSummaryItemAnchor(item)
		behavior := readSummaryItemField(item, "Behavior signals:")
		functions := displayReportField(readSummaryItemField(item, "Function signals:"))
		if anchor == "" || behavior == "" {
			continue
		}
		slashed := filepath.ToSlash(anchor)
		switch {
		case strings.Contains(slashed, "app.go"):
			risks = append(risks, fmt.Sprintf("1. `%s` entrypoint state and IO coupling\n   - Evidence: behavior signals include `%s`; functions: %s.\n   - Impact: preset loading, workbook session access, and SQL execution can fail or become stale at the UI boundary if config/session errors are not surfaced consistently.\n   - Verification: add focused tests around `NewApp`, `App.LoadSQLPresets`, `App.LoadWorkbook`, and `App.ExecuteManualSQL` for missing `configs/sqlstr.json`, replaced workbook sessions, and SQL error propagation.", anchor, behavior, functions))
		case strings.Contains(slashed, "internal/manual/session.go"):
			risks = append(risks, fmt.Sprintf("2. `%s` manual workbook-to-SQL session boundary\n   - Evidence: behavior signals include `%s`; functions: %s.\n   - Impact: schema drift, invalid SQL, or concurrent workbook replacement could return misleading query results or stale payload state.\n   - Verification: add `internal/manual/session_test.go` cases for workbook reload, duplicate/changed headers, invalid SQL, empty session, and concurrent `LoadWorkbook`/`ExecuteSQL` access.", anchor, behavior, functions))
		case strings.Contains(slashed, "internal/output/export.go"):
			risks = append(risks, fmt.Sprintf("3. `%s` Excel output write path\n   - Evidence: behavior signals include `%s`; functions: %s.\n   - Impact: append-row detection, header inclusion, formula rows, or partial writes could corrupt generated workbooks or silently misplace results.\n   - Verification: add golden workbook tests for start cell parsing, append mode, include/exclude headers, formula preservation, and write failure cleanup.", anchor, behavior, functions))
		case strings.Contains(slashed, "internal/auto/runner.go"):
			risks = append(risks, fmt.Sprintf("3. `%s` automatic task orchestration boundary\n   - Evidence: behavior signals include `%s`; functions: %s.\n   - Impact: unknown task names, cancellation, parameter mutation, or output propagation errors could make automatic runs fail late or produce incomplete workbooks.\n   - Verification: add runner tests for unknown tasks, context cancellation, task parameter immutability, and failed task output handling.", anchor, behavior, functions))
		case strings.Contains(slashed, "internal/plugin/"):
			risks = append(risks, fmt.Sprintf("3. `%s` plugin registration and lookup path\n   - Evidence: behavior signals include `%s`; functions: %s.\n   - Impact: duplicate plugin names, missing plugins, or plugin side effects could alter automatic task results without clear operator feedback.\n   - Verification: add registry tests for duplicate registration policy, missing plugin errors, and plugin context/error propagation.", anchor, behavior, functions))
		}
	}
	risks = dedupeReportLines(risks)
	if len(risks) == 0 {
		return "- No candidate risk with file/function evidence could be derived from the current source reads."
	}
	if len(risks) > 3 {
		risks = risks[:3]
	}
	for index, risk := range risks {
		risks[index] = renumberMarkdownRisk(risk, index+1)
	}
	return strings.Join(risks, "\n")
}

func renumberMarkdownRisk(value string, number int) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	if dot := strings.Index(trimmed, ". "); dot > 0 {
		prefix := trimmed[:dot]
		allDigits := true
		for _, r := range prefix {
			if r < '0' || r > '9' {
				allDigits = false
				break
			}
		}
		if allDigits {
			return fmt.Sprintf("%d. %s", number, trimmed[dot+2:])
		}
	}
	return fmt.Sprintf("%d. %s", number, trimmed)
}

func reportFunctionRiskLeads(readSummary string) string {
	items := reportReadSummaryItems(readSummary)
	leads := []string{}
	for _, item := range items {
		anchor := readSummaryItemAnchor(item)
		behavior := readSummaryItemField(item, "Behavior signals:")
		functions := readSummaryItemField(item, "Function signals:")
		if anchor == "" || behavior == "" {
			continue
		}
		switch {
		case strings.Contains(filepath.ToSlash(anchor), "app.go"):
			leads = append(leads, fmt.Sprintf("- `%s`: preset/workbook entrypoint crosses file IO, SQL, workbook session, and locking. Risk focus: startup fallback, missing config, and stale shared session. Functions: %s.", anchor, displayReportField(functions)))
		case strings.Contains(filepath.ToSlash(anchor), "internal/manual/session.go"):
			leads = append(leads, fmt.Sprintf("- `%s`: manual SQL path combines workbook payload, SQLite execution, and locking. Risk focus: schema drift, invalid SQL errors, concurrent session replacement, and stale query state. Functions: %s.", anchor, displayReportField(functions)))
		case strings.Contains(filepath.ToSlash(anchor), "internal/output/export.go"):
			leads = append(leads, fmt.Sprintf("- `%s`: output path reads/writes Excel workbooks. Risk focus: append row detection, header inclusion, formula rows, overwrite behavior, and partial file writes. Functions: %s.", anchor, displayReportField(functions)))
		case strings.Contains(filepath.ToSlash(anchor), "internal/auto/runner.go"):
			leads = append(leads, fmt.Sprintf("- `%s`: auto runner owns task registration and context boundary. Risk focus: unknown task errors, parameter cloning, task cancellation, and output propagation. Functions: %s.", anchor, displayReportField(functions)))
		case strings.Contains(filepath.ToSlash(anchor), "internal/plugin/"):
			leads = append(leads, fmt.Sprintf("- `%s`: plugin path covers registry/application behavior. Risk focus: duplicate names, unreviewed plugin side effects, and context propagation. Functions: %s.", anchor, displayReportField(functions)))
		}
	}
	if len(leads) == 0 {
		return "- No function-level risk lead could be derived from behavior signals."
	}
	return strings.Join(dedupeReportLines(leads), "\n")
}

func reportEvidenceQuality(readSummary string) string {
	coverage := buildEvidenceCoverage(readSummary)
	items := reportReadSummaryItems(readSummary)
	behaviorItems := 0
	testItems := 0
	for _, item := range items {
		if readSummaryItemField(item, "Behavior signals:") != "" {
			behaviorItems++
		}
		if strings.Contains(strings.ToLower(filepath.ToSlash(readSummaryItemAnchor(item))), "_test.go") {
			testItems++
		}
	}
	verifierLeadCount := countReportLines(reportVerificationLeads(readSummary))
	verdict := "low"
	switch {
	case coverage.Source >= 4 && testItems >= 2 && behaviorItems >= 4 && verifierLeadCount >= 2:
		verdict = "high"
	case coverage.Source > 0 && behaviorItems > 0:
		verdict = "medium"
	}
	lines := []string{
		fmt.Sprintf("- verdict: %s", verdict),
		fmt.Sprintf("- source files: %d; test files: %d; behavior-signal files: %d; verifier leads: %d", coverage.Source, testItems, behaviorItems, verifierLeadCount),
	}
	if verdict != "high" {
		lines = append(lines, "- caution: do not promote broad architectural concerns to proven findings without source-backed behavior plus a test/command verifier.")
	}
	return strings.Join(lines, "\n")
}

func displayReportField(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "unknown"
	}
	return trimmed
}

func countReportLines(block string) int {
	count := 0
	for _, line := range strings.Split(block, "\n") {
		trimmed := strings.TrimSpace(line)
		lowered := strings.ToLower(trimmed)
		if strings.HasPrefix(trimmed, "- ") && !strings.Contains(lowered, "no runnable verifier lead") && !strings.Contains(lowered, "no focused verifier") {
			count++
		}
	}
	return count
}

func reportCodeTraceEvidence(readSummary string) string {
	items := reportReadSummaryItems(readSummary)
	if len(items) == 0 {
		return "- No trace evidence was captured."
	}
	bucketed := map[string][]string{}
	for _, item := range items {
		if strings.Contains(strings.ToLower(filepath.ToSlash(item)), "_test.go") {
			if line := reportCodeTraceLine(item); line != "" {
				bucketed["tests"] = append(bucketed["tests"], "- tests: "+line)
			}
			continue
		}
		bucket := evidenceCoverageBucket(item)
		switch bucket {
		case "source":
			if line := reportCodeTraceLine(item); line != "" {
				bucketed["source"] = append(bucketed["source"], "- source: "+line)
			}
		case "entrypoints":
			if line := reportCodeTraceLine(item); line != "" {
				bucketed["entrypoints"] = append(bucketed["entrypoints"], "- entrypoint: "+line)
			}
		case "manifests":
			if line := reportCodeTraceLine(item); line != "" {
				bucketed["manifests"] = append(bucketed["manifests"], "- manifest: "+line)
			}
		case "docs":
			if line := reportCodeTraceLine(item); line != "" {
				bucketed["docs"] = append(bucketed["docs"], "- docs: "+line)
			}
		}
	}
	lines := []string{}
	for _, bucket := range []string{"entrypoints", "source", "tests", "manifests", "docs"} {
		limit := len(bucketed[bucket])
		switch bucket {
		case "docs", "manifests":
			limit = min(limit, 1)
		case "source":
			limit = min(limit, 5)
		default:
			limit = min(limit, 2)
		}
		for _, line := range bucketed[bucket][:limit] {
			lines = append(lines, line)
			if len(lines) >= 10 {
				break
			}
		}
		if len(lines) >= 10 {
			break
		}
	}
	if len(lines) == 0 {
		return "- No trace evidence was captured."
	}
	return strings.Join(dedupeReportLines(lines), "\n")
}

func reportRepositoryGapEvidence(readSummary string) string {
	gaps := declaredMissingRepositoryPaths(readSummary)
	if len(gaps) == 0 {
		return "- No declared missing repository files were detected from the current evidence."
	}
	lines := []string{}
	for _, gap := range gaps {
		lines = append(lines, fmt.Sprintf("- `%s` is referenced in reviewed evidence but was not found in the repository workspace.", filepath.ToSlash(gap)))
		if len(lines) >= 8 {
			break
		}
	}
	return strings.Join(dedupeReportLines(lines), "\n")
}

func declaredMissingRepositoryPaths(readSummary string) []string {
	seen := map[string]bool{}
	missing := []string{}
	re := regexp.MustCompile("`([^`]+)`")
	for _, match := range re.FindAllStringSubmatch(readSummary, -1) {
		if len(match) < 2 {
			continue
		}
		candidate := cleanRequestFileTarget(match[1])
		if candidate == "" || isIgnoredSurveyPath(candidate) {
			continue
		}
		if strings.ContainsAny(candidate, " \t\r\n") || looksLikeBacktickedCommand(candidate) {
			continue
		}
		if !strings.Contains(candidate, string(filepath.Separator)) {
			continue
		}
		key := strings.ToLower(filepath.Clean(candidate))
		if seen[key] {
			continue
		}
		seen[key] = true
		if repositoryPathExists(candidate) {
			continue
		}
		missing = append(missing, filepath.Clean(candidate))
	}
	sort.Strings(missing)
	return missing
}

func looksLikeBacktickedCommand(candidate string) bool {
	lowered := strings.ToLower(strings.TrimSpace(candidate))
	return strings.HasPrefix(lowered, "go test") ||
		strings.HasPrefix(lowered, "go vet") ||
		strings.HasPrefix(lowered, "go build") ||
		strings.HasPrefix(lowered, "npm ") ||
		strings.HasPrefix(lowered, "npx ") ||
		strings.HasPrefix(lowered, "wails ")
}

func repositoryPathExists(path string) bool {
	_, err := os.Stat(filepath.Clean(path))
	return err == nil
}

func reportCodeTraceLine(item string) string {
	anchor := readSummaryItemAnchor(item)
	if anchor == "" {
		return ""
	}
	details := []string{}
	for _, label := range []string{"Function signals:", "Behavior signals:", "Path signals:", "Top declarations:", "Imports:", "Headings:", "Key points:", "Command signals:", "Entrypoint signal:", "Module:", "Package:"} {
		if value := readSummaryItemField(item, label); value != "" {
			if len(value) > 110 {
				value = value[:110] + "..."
			}
			details = append(details, fmt.Sprintf("%s %s", strings.TrimSuffix(label, ":"), value))
		}
	}
	if len(details) == 0 {
		return anchor
	}
	return fmt.Sprintf("%s | %s", anchor, strings.Join(details, " | "))
}

func reportNextChecks(readSummary string, generatedSkillPath string, generatedSkillPreview string, invokedSkillName string) string {
	checks := []string{}
	items := reportReadSummaryItems(readSummary)
	checks = append(checks, remediationNextChecksFromFocusedFailures(readSummary)...)
	for _, evidence := range sourceBackedMissingPathEvidence(readSummary) {
		functionDetail := "the referencing code path"
		if evidence.FunctionSignals != "" {
			functionDetail = evidence.FunctionSignals
		}
		checks = append(checks, fmt.Sprintf("- Verify source-backed missing path `%s`: add or run a targeted check around `%s` (`%s`) to confirm runtime fallback/error behavior when the file is absent.", evidence.Path, evidence.Anchor, functionDetail))
	}
	if containsAnyItem(items, "internal/manual") && !focusedFailureCoversPath(readSummary, "internal/manual") {
		checks = append(checks, "- Trace `internal/manual/session.go` and the workbook import/SQL path for state sharing, cache invalidation, and failure propagation.")
	}
	if containsAnyItem(items, "internal/auto") && !focusedFailureCoversPath(readSummary, "internal/auto") {
		checks = append(checks, "- Trace `internal/auto/runner.go` and one representative task file to confirm how task orchestration and remediation flow actually work.")
	}
	if containsAnyItem(items, "internal/output", "internal/template") && !focusedFailureCoversPath(readSummary, "internal/output") {
		checks = append(checks, "- Trace `internal/output/export.go` and template helpers for workbook write-back correctness and failure shape.")
	}
	if containsAnyItem(items, "internal/plugin", "internal/plugins") {
		checks = append(checks, "- Trace plugin registry wiring for discoverability, coupling, and approval boundaries.")
	}
	if containsAnyItem(items, "main.go", "cmd/") {
		checks = append(checks, "- Trace `main.go` and natural-language routing entrypoints for the first user-input decision boundary.")
	}
	if len(checks) == 0 {
		checks = append(checks, "- Expand source survey into deeper symbol-level readouts for the main entrypoints.")
	}
	checks = append(checks, "- Confirm report content matches the requested markdown path and keeps proof artifacts in an appendix.")
	if generatedSkillPath != "" {
		checks = append(checks, "- Review generated skill candidate approval state.")
	}
	if generatedSkillPreview != "" {
		checks = append(checks, "- Consider promoting the prepared skill preview into the guarded write path on a later approved run.")
	}
	if invokedSkillName != "" {
		checks = append(checks, "- Re-run with the invoked approved skill if deeper guidance is needed.")
	}
	return strings.Join(dedupeReportLines(checks), "\n")
}

func remediationNextChecksFromFocusedFailures(readSummary string) []string {
	checks := []string{}
	for _, failure := range focusedVerificationFailures(readSummary) {
		location := failure.TestLocation
		if location == "" {
			location = "the failing test output"
		}
		testName := failure.TestName
		if testName == "" {
			testName = "the failing focused verifier test"
		}
		command := failure.Command
		if command == "" {
			command = "the focused verifier command"
		}
		checks = append(checks, fmt.Sprintf("- Fix/inspect verifier failure first: `%s` from `%s` failed at `%s`; use the assertion details in `Verification Results` before doing broader trace work.", testName, command, location))
	}
	return checks
}

func reportSynthesisStatus(finalSummary string, synthesis string) string {
	combined := strings.ToLower(strings.TrimSpace(finalSummary + "\n" + synthesis))
	switch {
	case strings.Contains(combined, "synthesis fallback used"):
		return "- Fallback: LLM synthesis did not complete; this report is deterministic and evidence-limited."
	case strings.Contains(combined, "synthesis status") && strings.Contains(combined, "failed"):
		return "- Fallback: LLM synthesis failed; this report is deterministic and evidence-limited."
	case strings.Contains(combined, "no synthesis available"):
		return "- Fallback: no synthesis text was available."
	default:
		return "- Completed: LLM synthesis text was available or a complete markdown report was supplied."
	}
}

func synthesisStatusIndicatesDegraded(status string) bool {
	lowered := strings.ToLower(strings.TrimSpace(status))
	return lowered != "" && (strings.Contains(lowered, "fallback") || strings.Contains(lowered, "failed") || strings.Contains(lowered, "timeout"))
}

func buildFallbackAnalysisReport(readSummary string, plan planner.Plan, generatedSkillPath string, generatedSkillPreview string, invokedSkillName string, synthesisStatus string) string {
	if bootstrapPlanningState(readSummary) {
		return buildBootstrapPlanningReport("", plan, readSummary, generatedSkillPath, generatedSkillPreview, invokedSkillName, false)
	}
	status := strings.TrimSpace(synthesisStatus)
	if status == "" {
		status = "fallback: no LLM synthesis text was produced"
	}
	stateLines := projectStateLines(readSummary)
	sections := []string{
		"# Analysis Report",
		"## Project Purpose",
		projectPurposeLine(readSummary, ""),
		"## Fallback Summary",
		fallbackProjectSummary(readSummary, status),
		"## Evidence-Backed Conclusions",
		strings.Join(fallbackConclusionLines(readSummary, status), "\n"),
		"## Implementation Snapshot",
		fallbackImplementationSnapshot(readSummary),
		"## Project State",
		strings.Join(stateLines, "\n"),
		"## Code Trace Evidence",
		reportCodeTraceEvidence(readSummary),
		"## Verification Leads",
		reportVerificationLeads(readSummary),
		"## Verification Results",
		reportVerificationResults(readSummary),
		"## Remediation Leads",
		reportRemediationLeads(readSummary),
		"## Function-Level Risk Leads",
		reportFunctionRiskLeads(readSummary),
		"## Evidence Quality",
		reportEvidenceQuality(readSummary),
		"## Repository Gap Evidence",
		reportRepositoryGapEvidence(readSummary),
		"## Evidence Coverage",
		evidenceCoverageMarkdown(readSummary),
		"## Synthesis Status",
		"- Fallback: " + status,
	}
	sections = append(sections, reportFindingsSections(readSummary, "Synthesis fallback used", "LLM synthesis did not complete")...)
	sections = append(sections,
		"## Review Leads",
		reportReviewLeads(readSummary),
		"## Evidence Reviewed",
		compactReportEvidenceList(plan, readSummary, generatedSkillPath, generatedSkillPreview, invokedSkillName, false, 8),
		"## Confidence",
		"- Low to medium: deterministic repository evidence was captured, but final LLM synthesis was unavailable.",
		"## Next Checks",
		reportNextChecks(readSummary, generatedSkillPath, generatedSkillPreview, invokedSkillName),
	)
	return strings.Join(sections, "\n\n") + "\n"
}

func reportEvidenceAppendixIfNeeded(synthesisBlock string, plan planner.Plan, readSummary string, generatedSkillPath string, generatedSkillPreview string, invokedSkillName string, restored bool) string {
	lowered := strings.ToLower(synthesisBlock)
	parts := []string{}
	if !strings.Contains(lowered, "verification results") {
		parts = append(parts, "### Verification Results\n"+reportVerificationResults(readSummary))
	}
	if !strings.Contains(lowered, "remediation leads") {
		parts = append(parts, "### Remediation Leads\n"+reportRemediationLeads(readSummary))
	}
	if !strings.Contains(lowered, "verification leads") {
		parts = append(parts, "### Verification Leads\n"+reportVerificationLeads(readSummary))
	}
	if !strings.Contains(lowered, "function-level risk leads") {
		parts = append(parts, "### Function-Level Risk Leads\n"+reportFunctionRiskLeads(readSummary))
	}
	if !strings.Contains(lowered, "evidence quality") {
		parts = append(parts, "### Evidence Quality\n"+reportEvidenceQuality(readSummary))
	}
	if !strings.Contains(lowered, "repository gap evidence") {
		parts = append(parts, "### Repository Gap Evidence\n"+reportRepositoryGapEvidence(readSummary))
	}
	if !(strings.Contains(lowered, "## evidence reviewed") || strings.Contains(lowered, "# evidence reviewed") || strings.Contains(lowered, "### evidence reviewed")) {
		items := compactReportEvidenceList(plan, readSummary, generatedSkillPath, generatedSkillPreview, invokedSkillName, restored, 8)
		if strings.TrimSpace(items) != "" {
			parts = append(parts, "### Reviewed Evidence\n"+items)
		}
	}
	if !(strings.Contains(lowered, "## evidence coverage") || strings.Contains(lowered, "# evidence coverage") || strings.Contains(lowered, "### evidence coverage")) {
		parts = append(parts, "### Coverage Summary\n"+evidenceCoverageMarkdown(readSummary))
	}
	if len(parts) == 0 {
		return ""
	}
	return "\n\n## Evidence Appendix\n\n" + strings.Join(parts, "\n\n") + "\n"
}

func buildBootstrapPlanningReport(input string, plan planner.Plan, readSummary string, generatedSkillPath string, generatedSkillPreview string, invokedSkillName string, restored bool) string {
	request := strings.TrimSpace(input)
	if request == "" {
		request = "No request text captured in fallback report."
	}
	sections := []string{
		"# Bootstrap Planning Report",
		"## Request",
		request,
		"## Current Evidence",
		strings.Join(reportEvidenceListItems(plan, readSummary, generatedSkillPath, generatedSkillPreview, invokedSkillName, restored), "\n"),
		"## Project State",
		strings.Join(bootstrapPlanningStateLines(readSummary), "\n"),
		"## Required Preparation Files",
		"- `README.md`: project purpose, target users, workflows, setup, and run commands.",
		"- `docs/architecture.md`: system boundaries, modules, data flow, and key decisions.",
		"- `docs/plan.md`: MVP scope, milestones, acceptance criteria, and risks.",
		"- Language manifest such as `go.mod`, `package.json`, `pyproject.toml`, or `Cargo.toml`.",
		"- Entrypoint source such as `main.go`, `cmd/<app>/main.go`, `src/main.*`, or `app.*`.",
		"## Recommended Next Steps",
		"- Decide whether this is a new project bootstrap task or a repository analysis task.",
		"- If bootstrapping, generate architecture and plan files first, then create manifests and entrypoints.",
		"- If analyzing, rerun after at least one project document, manifest, or source entrypoint exists.",
		"## Issues",
		"- No concrete code defect can be claimed because no target-project evidence exists.",
		"## Confidence",
		"- High confidence in the absence of discoverable repository evidence; no confidence in project-specific conclusions yet.",
	}
	return strings.Join(sections, "\n\n") + "\n"
}

func bootstrapPlanningState(readSummary string) bool {
	return strings.Contains(strings.ToLower(readSummary), "no repository evidence was found")
}

func projectStateLines(readSummary string) []string {
	items := reportReadSummaryItems(readSummary)
	if len(items) == 0 {
		return []string{}
	}
	hasSource := false
	hasManifest := false
	hasDocs := false
	for _, item := range items {
		lowered := strings.ToLower(item)
		if strings.Contains(lowered, "package:") || strings.Contains(lowered, "top declarations:") || strings.Contains(lowered, "entrypoint signal:") {
			hasSource = true
		}
		if strings.Contains(lowered, "go.mod") || strings.Contains(lowered, "package.json") || strings.Contains(lowered, "pyproject.toml") || strings.Contains(lowered, "cargo.toml") || strings.Contains(lowered, "module:") || strings.Contains(lowered, "requires:") {
			hasManifest = true
		}
		if strings.Contains(lowered, "readme") || strings.Contains(lowered, "docs") || strings.Contains(lowered, "plan") || strings.Contains(lowered, "analysis") || strings.Contains(lowered, "cli_guide") {
			hasDocs = true
		}
	}
	lines := []string{}
	if hasSource {
		lines = append(lines, "- Source evidence was found.")
	} else {
		lines = append(lines, "- Repository evidence is too thin for issue-hunting.")
	}
	switch {
	case hasSource && hasManifest && hasDocs:
		lines = append(lines, "- Docs, manifests, and source were all reviewed.")
	case hasSource && hasManifest && !hasDocs:
		lines = append(lines,
			"- Source and manifest evidence were found.",
			"- Missing next: project docs for workflows, constraints, and operating notes.",
		)
	case hasSource && !hasManifest && hasDocs:
		lines = append(lines,
			"- Source and docs evidence were found.",
			"- Missing next: manifest and dependency files for build context.",
		)
	case hasSource && !hasManifest && !hasDocs:
		lines = append(lines,
			"- Only source evidence was found.",
			"- Missing next: README, docs, and manifest files.",
		)
	case hasDocs && !hasManifest:
		lines = append(lines,
			"- Only documentation evidence was found.",
			"- Missing next: manifest and entrypoint/source files.",
		)
	case hasManifest && !hasDocs:
		lines = append(lines,
			"- Only manifest evidence was found.",
			"- Missing next: project docs and entrypoint/source files.",
		)
	default:
		lines = append(lines,
			"- No project purpose, architecture, language stack, entrypoint, manifest, or test surface can be verified yet.",
			"- Missing next: README, docs/architecture.md, docs/plan.md, manifest, and entrypoint source.",
		)
	}
	return lines
}

func bootstrapPlanningStateLines(readSummary string) []string {
	if !bootstrapPlanningState(readSummary) {
		return []string{
			"- No repository evidence was found.",
			"- No project purpose, architecture, language stack, entrypoint, manifest, or test surface can be verified yet.",
		}
	}
	return []string{
		"- No repository evidence was found.",
		"- No project purpose, architecture, language stack, entrypoint, manifest, or test surface can be verified yet.",
	}
}

func (e *Engine) writeRunReport(path string, content string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return err
	}
	return nil
}

func min(a int, b int) int {
	if a < b {
		return a
	}
	return b
}

func isEnglishSafeRuntimeText(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return false
	}
	for _, r := range trimmed {
		if r == '\n' || r == '\r' || r == '\t' {
			continue
		}
		if r > unicode.MaxASCII {
			return false
		}
	}
	return true
}

func decodeRawMessage(raw json.RawMessage) any {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}

	var value any
	if err := json.Unmarshal(trimmed, &value); err != nil {
		return string(trimmed)
	}
	return value
}

// buildAnalysisCommands returns read-only shell commands appropriate for
// the given analysis task. Returns nil if the task doesn't look like a
// data-collection request. This enables the plan-mode Builder to execute
// actual analysis instead of generating an empty skill.
func buildAnalysisCommands(input string, readSummary string) []string {
	lowered := strings.ToLower(input)

	// Find/search/grep patterns
	if strings.Contains(lowered, "todo") || strings.Contains(lowered, "fixme") ||
		strings.Contains(lowered, "找出") || strings.Contains(lowered, "搜索") {
		pattern := "TODO|FIXME|todo|fixme"
		if strings.Contains(lowered, "hack") || strings.Contains(lowered, "xxx") {
			pattern += "|HACK|XXX"
		}
		return []string{fmt.Sprintf(
			`grep -rn --include="*.go" --include="*.md" --include="*.py" --include="*.js" -E "%s" . 2>nul | head -50`, pattern)}
	}

	// Count Go files
	if (strings.Contains(lowered, "go") && strings.Contains(lowered, "文件")) ||
		(strings.Contains(lowered, "go") && strings.Contains(lowered, "file")) ||
		strings.Contains(lowered, "统计") {
		return []string{
			`find . -name "*.go" -not -path "*/.git/*" -not -path "*/.avatars/*" -not -path "*/vendor/*" -not -path "*/node_modules/*" | wc -l`,
			`find . -name "*.go" -not -path "*/.git/*" -not -path "*/.avatars/*" -not -path "*/vendor/*" -not -path "*/node_modules/*" -exec wc -l {} + 2>nul | tail -1`,
		}
	}

	// ASCII directory tree
	if strings.Contains(lowered, "树") || strings.Contains(lowered, "tree") ||
		strings.Contains(lowered, "结构") {
		return []string{
			`find . -not -path "*/.git/*" -not -path "*/.avatars/*" -not -path "*/node_modules/*" -not -path "*/vendor/*" -maxdepth 3 -type d | sort | head -40`,
		}
	}

	// Top imports / dependency analysis
	if strings.Contains(lowered, "import") || strings.Contains(lowered, "导入") ||
		strings.Contains(lowered, "依赖") {
		return []string{
			`grep -rh "^import" --include="*.go" . 2>nul | sort | uniq -c | sort -rn | head -15`,
			`cat go.mod 2>nul`,
		}
	}

	// Code line count
	if strings.Contains(lowered, "行") || strings.Contains(lowered, "line") ||
		strings.Contains(lowered, "loc") {
		return []string{
			`find . -name "*.go" -not -path "*/.git/*" -not -path "*/.avatars/*" -not -path "*/vendor/*" -not -path "*/node_modules/*" -exec cat {} + 2>nul | wc -l`,
		}
	}

	// Exported functions / types / interfaces
	if (strings.Contains(lowered, "函数") || strings.Contains(lowered, "func") ||
		strings.Contains(lowered, "类型") || strings.Contains(lowered, "type") ||
		strings.Contains(lowered, "接口") || strings.Contains(lowered, "interface") ||
		strings.Contains(lowered, "导出") || strings.Contains(lowered, "export")) &&
		(strings.Contains(lowered, "列出") || strings.Contains(lowered, "按") ||
			strings.Contains(lowered, "list") || strings.Contains(lowered, "find") ||
			strings.Contains(lowered, "分组") || strings.Contains(lowered, "group")) {
		return []string{
			`grep -rn "^func [A-Z]" --include="*.go" . 2>nul | head -80`,
			`grep -rn "^type [A-Z]" --include="*.go" . 2>nul | head -40`,
		}
	}

	// README / project analysis: read key files
	if strings.Contains(lowered, "分析") || strings.Contains(lowered, "analyze") ||
		strings.Contains(lowered, "了解") || strings.Contains(lowered, "understand") {
		return []string{
			`cat README.md 2>nul | head -100`,
			`find . -name "*.go" -not -path "*/.git/*" -not -path "*/.avatars/*" -not -path "*/vendor/*" -not -path "*/node_modules/*" | head -30`,
			`cat go.mod 2>nul`,
		}
	}

	return nil
}
