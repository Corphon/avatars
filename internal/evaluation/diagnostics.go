package evaluation

import (
	"fmt"
	"strings"
)

type Diagnostic struct {
	Tool     string `json:"tool"`
	Severity string `json:"severity"`
	Summary  string `json:"summary"`
	Message  string `json:"message"`
	Path     string `json:"path"`
	Line     int    `json:"line"`
	Column   int    `json:"column"`
	Code     string `json:"code"`
	Source   string `json:"source"`
}

type WarmLesson struct {
	Kind       string
	Summary    string
	Source     string
	Confidence string
}

func BuildDiagnosticRecords(diagnostics []Diagnostic) []Record {
	if len(diagnostics) == 0 {
		return nil
	}
	records := make([]Record, 0, len(diagnostics))
	for _, diagnostic := range diagnostics {
		record, ok := buildDiagnosticRecord(diagnostic)
		if !ok {
			continue
		}
		records = append(records, record)
	}
	return records
}

func BuildDiagnosticWarmLesson(records []Record) (WarmLesson, bool) {
	bestIndex := -1
	bestRank := -1
	for index, record := range records {
		if strings.ToLower(strings.TrimSpace(record.Kind)) != "passive_feedback" {
			continue
		}
		rank := diagnosticWarmLessonRank(record.Verdict)
		if rank > bestRank {
			bestRank = rank
			bestIndex = index
		}
	}
	if bestIndex < 0 {
		return WarmLesson{}, false
	}
	record := records[bestIndex]
	tool := strings.TrimSpace(record.Tool)
	if tool == "" {
		tool = "the same diagnostic path"
	}
	confidence := "medium"
	if strings.EqualFold(strings.TrimSpace(record.Verdict), "FAIL") {
		confidence = "high"
	}
	return WarmLesson{
		Kind:       "diagnostic_import",
		Summary:    fmt.Sprintf("Before repeating %s, resolve this imported diagnostic: %s", tool, strings.TrimSpace(record.Summary)),
		Source:     "diagnostic_import",
		Confidence: confidence,
	}, true
}

func buildDiagnosticRecord(diagnostic Diagnostic) (Record, bool) {
	message := normalizePassiveSignal(firstNonEmpty(diagnostic.Summary, diagnostic.Message))
	if message == "" {
		return Record{}, false
	}
	tool := strings.TrimSpace(diagnostic.Tool)
	if tool == "" {
		tool = "diagnostic"
	}
	severity := normalizeDiagnosticSeverity(diagnostic.Severity)
	location := diagnosticLocation(diagnostic)
	summary := fmt.Sprintf("Imported diagnostic %s for %s", severity, tool)
	if location != "" {
		summary += " at " + location
	}
	summary += ": " + message
	details := make([]string, 0, 5)
	if strings.TrimSpace(diagnostic.Path) != "" {
		details = append(details, "path: "+strings.TrimSpace(diagnostic.Path))
	}
	if diagnostic.Line > 0 {
		if diagnostic.Column > 0 {
			details = append(details, fmt.Sprintf("position: %d:%d", diagnostic.Line, diagnostic.Column))
		} else {
			details = append(details, fmt.Sprintf("position: %d", diagnostic.Line))
		}
	}
	if strings.TrimSpace(diagnostic.Code) != "" {
		details = append(details, "code: "+strings.TrimSpace(diagnostic.Code))
	}
	if strings.TrimSpace(diagnostic.Source) != "" {
		details = append(details, "source: "+strings.TrimSpace(diagnostic.Source))
	}
	return Record{
		Tool:    tool,
		Kind:    "passive_feedback",
		Verdict: verdictFromDiagnosticSeverity(severity),
		Cause:   causeFromDiagnosticSeverity(severity),
		Summary: summary,
		Source:  diagnosticSource(diagnostic.Source),
		Details: details,
	}, true
}

func normalizeDiagnosticSeverity(severity string) string {
	trimmed := strings.ToUpper(strings.TrimSpace(severity))
	switch trimmed {
	case "ERROR", "WARNING", "INFO", "HINT":
		return trimmed
	default:
		return "WARNING"
	}
}

func verdictFromDiagnosticSeverity(severity string) string {
	if severity == "ERROR" {
		return "FAIL"
	}
	return "PARTIAL"
}

func causeFromDiagnosticSeverity(severity string) string {
	switch severity {
	case "ERROR":
		return "diagnostic_error"
	case "INFO":
		return "diagnostic_info"
	case "HINT":
		return "diagnostic_hint"
	default:
		return "diagnostic_warning"
	}
}

func diagnosticSource(source string) string {
	trimmed := strings.TrimSpace(source)
	if trimmed == "" {
		return "diagnostic_import"
	}
	return trimmed
}

func diagnosticLocation(diagnostic Diagnostic) string {
	path := strings.TrimSpace(diagnostic.Path)
	if path == "" {
		return ""
	}
	if diagnostic.Line > 0 {
		if diagnostic.Column > 0 {
			return fmt.Sprintf("%s:%d:%d", path, diagnostic.Line, diagnostic.Column)
		}
		return fmt.Sprintf("%s:%d", path, diagnostic.Line)
	}
	return path
}

func diagnosticWarmLessonRank(verdict string) int {
	if strings.EqualFold(strings.TrimSpace(verdict), "FAIL") {
		return 2
	}
	if strings.EqualFold(strings.TrimSpace(verdict), "PARTIAL") {
		return 1
	}
	return 0
}
