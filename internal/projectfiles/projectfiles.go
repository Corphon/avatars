package projectfiles

import (
	"path/filepath"
	"strings"
)

const (
	ReadmeCanonical = "README.md"
	DocsDir         = "docs"
	CLIGuide        = "CLI_guide.md"
	ProcessRecord   = "process_record.md"
	CodingPlan      = "coding_plan.md"
	SkillsDoc       = "skills.md"
	GoMod           = "go.mod"
	AvatarsModule   = "avatars"
)

func ReadmeCandidates() []string {
	return []string{
		ReadmeCanonical,
		"Readme.md",
		"readme.md",
	}
}

func DocsIndexNames() []string {
	return []string{
		"README.md",
		"Readme.md",
		"readme.md",
		"index.md",
		"Index.md",
	}
}

func BootstrapFileCandidates() []string {
	candidates := append([]string{}, ReadmeCandidates()...)
	for _, name := range DocsIndexNames() {
		candidates = append(candidates, filepath.Join(DocsDir, name))
	}
	return candidates
}

func BootstrapSurveyTargets() []string {
	// File paths only — the read tool cannot open directories. Manifests
	// cover Go/JS/Rust/Python; missing files are skipped at survey time.
	return []string{
		ReadmeCanonical,
		filepath.Join(DocsDir, "README.md"),
		CLIGuide,
		"go.mod",
		"package.json",
		"Cargo.toml",
		"pyproject.toml",
		"requirements.txt",
		"Makefile",
	}
}

// ReportOutputAliases returns the canonical list of report output file aliases.
// These are the file names that avatars recognizes as valid report output targets.
// Keep aliases descriptive and standard — avoid single-task shortcuts.
func ReportOutputAliases() []string {
	return []string{
		"analysis.md",
		"outcome.md",
		"report.md",
		"findings.md",
		"summary.md",
	}
}

func IsAnalysisReportFile(path string) bool {
	base := strings.ToLower(filepath.Base(strings.TrimSpace(path)))
	if base == "" {
		return false
	}
	for _, alias := range ReportOutputAliases() {
		if base == strings.ToLower(alias) {
			return true
		}
	}
	if filepath.Ext(base) != ".md" {
		return false
	}
	stem := strings.TrimSuffix(base, ".md")
	for _, marker := range []string{"analysis", "anal", "report", "outcome", "findings", "summary"} {
		if stem == marker ||
			strings.HasPrefix(stem, marker+"-") ||
			strings.HasPrefix(stem, marker+"_") ||
			strings.HasSuffix(stem, "-"+marker) ||
			strings.HasSuffix(stem, "_"+marker) {
			return true
		}
	}
	return false
}

func IsAvatarsContextFile(path string) bool {
	switch strings.ToLower(filepath.Base(strings.TrimSpace(path))) {
	case strings.ToLower(CodingPlan), strings.ToLower(ProcessRecord), strings.ToLower(CLIGuide), strings.ToLower(SkillsDoc):
		return true
	default:
		return IsAnalysisReportFile(path)
	}
}

func IsDefaultAvatarsSurveyPath(path string) bool {
	cleaned := filepath.Clean(path)
	return cleaned == filepath.Clean(filepath.Join("cmd", "avatars", "main.go")) ||
		cleaned == filepath.Clean(filepath.Join("configs", "agent.yaml")) ||
		cleaned == filepath.Clean(filepath.Join(DocsDir, "avatars.md"))
}

func AvatarsRepositorySignaturePaths() []string {
	return []string{
		filepath.Join("cmd", "avatars", "main.go"),
		filepath.Join("configs", "agent.yaml"),
	}
}

func RepositoryLooksLikeAvatars(moduleName string, pathExists func(string) bool) bool {
	module := strings.TrimSpace(moduleName)
	if module == AvatarsModule {
		return true
	}
	if module != "" {
		return false
	}
	if pathExists == nil {
		return false
	}
	for _, path := range AvatarsRepositorySignaturePaths() {
		if !pathExists(path) {
			return false
		}
	}
	return true
}
