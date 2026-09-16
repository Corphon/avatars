package runtime

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// validateGoImports checks that all imports in a Go file actually resolve to
// valid packages. Delegates to the deterministic CheckExternalDependencies
// which cross-references imports against go.mod.
//
// IM-Fix-1: Called from checkFileHealth after syntax validation passes.
// Returns empty string if all imports are valid, or a human-readable error.
func validateGoImports(path string) string {
	// Use the deterministic CheckExternalDependencies to find bad imports.
	warnings := CheckExternalDependencies(".", []string{path})
	if len(warnings) == 0 {
		return ""
	}

	projectRoot, _ := os.Getwd()
	modulePath, _ := parseGoMod(projectRoot)

	var badImports []string
	for _, dw := range warnings {
		alt := ""
		if dw.StdlibAlt != "" {
			alt = " (stdlib alternative: " + dw.StdlibAlt + ")"
		}
		msg := fmt.Sprintf("%q%s", dw.ImportPath, alt)
		badImports = append(badImports, msg)
	}

	if len(badImports) == 0 {
		return ""
	}

	hint := ""
	if modulePath != "" {
		hint = fmt.Sprintf(" — imports must use module path prefix %q", modulePath)
	}

	return fmt.Sprintf("File %s has hallucinated imports that are not in go.mod%s: %s",
		filepath.Base(path), hint, strings.Join(badImports, ", "))
}

// validateGoImportsWithGoImports runs goimports on the given Go source to
// normalize imports (remove unused, add missing stdlib, standardize grouping).
// IM-Fix-3: Called from checkFileHealth after syntax validation.
//
// Returns the normalized content and any error. If goimports is not installed,
// returns the original content unchanged.
func validateGoImportsWithGoImports(content string) (string, error) {
	goimportsPath, err := exec.LookPath("goimports")
	if err != nil {
		// goimports not installed — graceful degradation.
		return content, nil
	}

	// Write content to a temp file because goimports needs a .go extension
	// to recognize the file as Go source.
	tmpFile, err := os.CreateTemp("", "avatars-import-*.go")
	if err != nil {
		return content, nil
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	if _, err := tmpFile.WriteString(content); err != nil {
		tmpFile.Close()
		return content, nil
	}
	tmpFile.Close()

	cmd := exec.Command(goimportsPath, "-w", tmpPath)
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		// goimports failed — keep original content.
		return content, nil
	}

	// Read back the normalized content.
	normalized, err := os.ReadFile(tmpPath)
	if err != nil {
		return content, nil
	}

	return string(normalized), nil
}
