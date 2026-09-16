package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// AdversarialFinding represents a concrete issue discovered by the
// adversarial Critic audit — not just "looks incomplete" but a specific,
// verifiable problem with the generated code.
// Phase 6: Adversarial Verification (inspired by Claude Code verificationAgent).
type AdversarialFinding struct {
	Kind     string // "external_dependency", "duplicate_type", "missing_import"
	File     string // file where the issue was found
	Detail   string // human-readable description
	Evidence string // verifiable evidence (e.g., "type Note defined in 2 files")
}

// AdversarialAudit runs deterministic checks against Builder-generated
// files that the regular Critic checklist might miss. These checks are
// adversarial in nature — they try to BREAK the code, not confirm it works.
//
// Checks performed:
//  1. External dependency scan (reuses CheckExternalDependencies)
//  2. Duplicate type detection across generated + existing files
//  3. Language-agnostic static analysis (TR-Fix-13: go vet / pylint / eslint)
//
// Returns findings that should be added to Critic audit results.
func AdversarialAudit(projectRoot string, generatedFiles []string) []AdversarialFinding {
	var findings []AdversarialFinding

	// Check 1: External dependencies (already proven in P4, reuse here).
	deps := CheckExternalDependencies(projectRoot, generatedFiles)
	for _, dw := range deps {
		findings = append(findings, AdversarialFinding{
			Kind:     "external_dependency",
			File:     dw.File,
			Detail:   fmt.Sprintf("imports %s which is not in go.mod", dw.ImportPath),
			Evidence: fmt.Sprintf("stdlib alternative: %s", dw.StdlibAlt),
		})
	}

	// Check 2: Duplicate type detection.
	// Scan generated files for "type X struct" / "type X interface" and
	// check if X already exists in the API Lock or appears in multiple
	// generated files.
	duplicates := findDuplicateTypes(projectRoot, generatedFiles)
	findings = append(findings, duplicates...)

	// Check 3: Language-agnostic static analysis (TR-Fix-13).
	// Catches logic bugs: nil derefs, unreachable code, error handling gaps,
	// race conditions, printf format errors, loop variable capture, etc.
	saFindings := runStaticAnalysis(projectRoot)
	for _, f := range saFindings {
		findings = append(findings, AdversarialFinding{
			Kind:     "static_analysis",
			File:     f.File,
			Detail:   fmt.Sprintf("[%s|%s] %s", f.Severity, f.Tool, f.Message),
			Evidence: fmt.Sprintf("line %d", f.Line),
		})
	}

	return findings
}

// findDuplicateTypes checks for type declarations that appear in multiple
// generated files — duplicates within Builder output that will cause
// compilation errors. Conflict with pre-existing project types is covered
// by the API Lock in Builder's system prompt + P4 dependency guard.
func findDuplicateTypes(projectRoot string, generatedFiles []string) []AdversarialFinding {
	var findings []AdversarialFinding

	genTypes := make(map[string][]string)
	typeRe := regexp.MustCompile(`type\s+(\w+)\s+(struct|interface)\s*\{`)

	for _, filePath := range generatedFiles {
		if !strings.HasSuffix(filePath, ".go") {
			continue
		}
		fullPath := filePath
		if !filepath.IsAbs(fullPath) {
			fullPath = filepath.Join(projectRoot, filePath)
		}
		content, err := os.ReadFile(fullPath)
		if err != nil {
			continue
		}
		for _, m := range typeRe.FindAllStringSubmatch(string(content), -1) {
			if len(m) >= 3 {
				genTypes[m[1]] = append(genTypes[m[1]], filePath)
			}
		}
	}

	for name, files := range genTypes {
		if len(files) > 1 {
			findings = append(findings, AdversarialFinding{
				Kind:     "duplicate_type",
				File:     files[0],
				Detail:   fmt.Sprintf("type %s declared in %d generated files: %s", name, len(files), strings.Join(files, ", ")),
				Evidence: "Multiple definitions will cause compilation error",
			})
		}
	}
	return findings
}
