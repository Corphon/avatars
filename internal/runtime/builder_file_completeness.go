package runtime

import (
	"context"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

// isLLMRefusal detects when the LLM returns a refusal/request-for-clarification
// instead of actual code. Common patterns from DeepSeek and other models.
func isLLMRefusal(text string) bool {
	lowered := strings.ToLower(text)
	refusalPatterns := []string{
		"i don't have enough context",
		"i don't have enough information",
		"could you share",
		"could you clarify",
		"could you provide",
		"i cannot ",
		"i need more",
		"not enough context",
		"not enough information",
		"please provide",
		"please specify",
		"what specific",
		"which specific",
		"more details",
		"i'm not sure what",
		"i am not sure what",
	}
	for _, p := range refusalPatterns {
		if strings.Contains(lowered, p) {
			return true
		}
	}
	return false
}

// buildRefusalRetryPrompt returns a more insistent prompt when the LLM refused.
func buildRefusalRetryPrompt(originalTask string) string {
	return originalTask + "\n\n" +
		"CRITICAL INSTRUCTION: Write the COMPLETE implementation code now.\n" +
		"Do NOT ask questions. Do NOT request clarification. Do NOT explain what you need.\n" +
		"Generate ALL the files listed in the task using the write_file tool.\n" +
		"Each file must contain complete, compilable code."
}

// findMissingLocalImports parses generated Go files and finds imports of local
// packages that don't exist on disk yet. Returns the missing package import paths.
func findMissingLocalImports(projectRoot string, generatedFiles []builderCodeFile) []string {
	modulePath := readModuleName(projectRoot)
	if modulePath == "" {
		return nil
	}

	existingDirs := listGoDirs(projectRoot)
	existingSet := make(map[string]bool, len(existingDirs))
	for _, d := range existingDirs {
		existingSet[d] = true
	}

	missing := make(map[string]bool)
	for _, cf := range generatedFiles {
		if !strings.HasSuffix(cf.Path, ".go") {
			continue
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, cf.Path, cf.Content, parser.ImportsOnly)
		if err != nil || f == nil {
			continue
		}
		for _, imp := range f.Imports {
			path := strings.Trim(imp.Path.Value, "\"")
			if !strings.HasPrefix(path, modulePath+"/") {
				continue // not a local import
			}
			relPath := strings.TrimPrefix(path, modulePath+"/")
			if existingSet[relPath] {
				continue // already exists
			}
			// Check if we already generated it
			alreadyGenerated := false
			for _, gf := range generatedFiles {
				gfDir := filepath.Dir(gf.Path)
				if gfDir == relPath || strings.HasSuffix(gf.Path, relPath+".go") {
					alreadyGenerated = true
					break
				}
			}
			if !alreadyGenerated {
				missing[relPath+"/"] = true // directory-level marker
			}
		}
	}

	var result []string
	for m := range missing {
		result = append(result, m)
	}
	return result
}

// isFileContentRefusal checks if a generated file's content contains
// LLM refusal patterns (e.g., "I don't have enough context" appended
// after valid code). Checks the ENTIRE content, not just the first line.
func isFileContentRefusal(path, content string) bool {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return true // empty file is a refusal
	}

	ext := strings.ToLower(filepath.Ext(path))

	// Check first line for proper structure.
	firstLine := strings.SplitN(trimmed, "\n", 2)[0]
	switch ext {
	case ".go":
		if !strings.HasPrefix(firstLine, "package ") {
			return true
		}
	case ".py":
		// Python files should not start with refusal patterns.
	case ".js", ".ts":
		// JS/TS should have imports or code.
	case ".mod":
		if !strings.HasPrefix(firstLine, "module ") {
			return true
		}
	}

	// Deep check: scan entire content for refusal patterns.
	// LLM sometimes appends refusal text AFTER valid code (e.g., after a closing }).
	if isLLMRefusal(content) {
		return true
	}

	return false
}
func inferFilePathFromPackage(pkgDir string) string {
	base := filepath.Base(pkgDir)
	return filepath.Join(pkgDir, base+".go")
}

// generateMissingPackageFiles fills gaps by generating files for packages
// that are imported by already-generated files but don't exist yet.
func (e *Engine) generateMissingPackageFiles(ctx context.Context, work workflowNodeWorkContext, projectRoot string, generatedFiles []builderCodeFile) ([]builderCodeFile, error) {
	missing := findMissingLocalImports(projectRoot, generatedFiles)
	if len(missing) == 0 {
		return nil, nil
	}

	var newFiles []builderCodeFile
	for _, pkgDir := range missing {
		filePath := inferFilePathFromPackage(pkgDir)
		_ = os.MkdirAll(filepath.Join(projectRoot, pkgDir), 0755)

		gapPrompt := fmt.Sprintf(
			"Generate the implementation for package %s in file %s.\n"+
				"This package is imported by already-generated code. Write the COMPLETE implementation.\n"+
				"Use ONLY the Go standard library unless go.mod already includes external dependencies.\n"+
				"Original task context: %s",
			pkgDir, filePath, work.input)

		gapFiles, gapErr := e.generateBuilderCodeSingle(ctx, work, gapPrompt)
		if gapErr != nil {
			// Create a minimal stub so the build can at least resolve the import.
			stub := createMinimalStub(filePath, "go")
			newFiles = append(newFiles, builderCodeFile{Path: filePath, Content: stub})
			_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.gap_stub_created", "runtime",
				map[string]any{"file": filePath, "package": pkgDir, "reason": "LLM failed, created stub"}, nil)
			continue
		}
		newFiles = append(newFiles, gapFiles...)
		_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.gap_file_generated", "runtime",
			map[string]any{"file": filePath, "package": pkgDir}, nil)
	}

	return newFiles, nil
}
