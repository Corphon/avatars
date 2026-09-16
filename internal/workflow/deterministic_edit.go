package workflow

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// DeterministicEdit describes one concrete file change that can be applied
// without LLM involvement. Each edit targets a specific file and operation.
type DeterministicEdit struct {
	File      string
	Operation string // "add_go_import", "add_to_slice", "add_switch_case", "add_jsx_menuitem"
	Params    map[string]string
}

// ApplyDeterministicEdit applies a single edit to a file.
// Returns true if the edit was applied, false if it was already present or failed.
func ApplyDeterministicEdit(projectRoot string, edit DeterministicEdit) (bool, error) {
	switch edit.Operation {
	case "add_go_import":
		return applyAddGoImport(projectRoot, edit)
	case "add_to_slice":
		return applyAddToSlice(projectRoot, edit)
	case "add_switch_case":
		return applyAddSwitchCase(projectRoot, edit)
	case "add_jsx_menuitem":
		return applyAddJSXMenuItem(projectRoot, edit)
	default:
		return false, fmt.Errorf("unknown deterministic edit operation: %s", edit.Operation)
	}
}

// ExtractDeterministicEdits parses a task description and returns a list of
// deterministic edits that can be applied without LLM.
// B1/PE-6: This is the template-based editing engine that replaces LLM for
// common integration patterns. Extended trigger words to cover more task
// phrasings (wire, register, integrate, connect).
func ExtractDeterministicEdits(taskInput string) []DeterministicEdit {
	var edits []DeterministicEdit
	lower := strings.ToLower(taskInput)

	// Action words that indicate integration editing (not creation).
	hasAction := strings.Contains(lower, "add") || strings.Contains(lower, "wire") ||
		strings.Contains(lower, "register") || strings.Contains(lower, "integrate") ||
		strings.Contains(lower, "connect") || strings.Contains(lower, "include") ||
		strings.Contains(lower, "insert") || strings.Contains(lower, "append")

	// Check for file extensions to determine operation context.
	hasGo := strings.Contains(taskInput, ".go")
	hasJSX := strings.Contains(taskInput, ".jsx") || strings.Contains(taskInput, ".tsx")

	// Pattern: "add import for X" / "wire provider import".
	// F28: greenfield tasks that merely say `import "mod/pkg"` / "import package"
	// must NOT invent internal/llm/providers/* blank imports.
	if hasAction && strings.Contains(lower, "import") && looksLikeProviderWiringTask(lower, taskInput) {
		edits = append(edits, extractGoImportEdits(taskInput)...)
	}

	// Pattern: "add X to list/slice" or "register X in validate" — for Go files.
	if hasAction && (strings.Contains(lower, "list") || strings.Contains(lower, "slice") ||
		strings.Contains(lower, "validate") || (hasGo && strings.Contains(lower, "config"))) {
		edits = append(edits, extractSliceEdits(taskInput)...)
	}

	// Pattern: "add case X" or "wire X case in ApplyYConfig"
	if hasAction && (strings.Contains(lower, "case") || strings.Contains(lower, "switch") ||
		strings.Contains(lower, "config_apply") || strings.Contains(lower, "applyconfig")) {
		edits = append(edits, extractSwitchCaseEdits(taskInput)...)
	}

	// Pattern: "add X option/entry in dropdown/Select/MenuItem" or JSX wiring.
	if hasAction && (strings.Contains(lower, "dropdown") || strings.Contains(lower, "menuitem") ||
		strings.Contains(lower, "select") || (hasJSX && strings.Contains(lower, "settings"))) {
		edits = append(edits, extractJSXEdits(taskInput)...)
	}

	return edits
}

// === Go Import Operations ===

// looksLikeProviderWiringTask is true only when the task is about wiring an
// LLM/vision provider blank-import — not greenfield "import package foo" docs.
func looksLikeProviderWiringTask(lower, taskInput string) bool {
	if blankImportPathRe.MatchString(taskInput) {
		return true
	}
	if strings.Contains(lower, "blank import") || strings.Contains(lower, "side-effect import") {
		return true
	}
	hasProvider := strings.Contains(lower, "provider") ||
		strings.Contains(lower, "llm/") ||
		strings.Contains(lower, "vision/providers")
	hasWire := strings.Contains(lower, "wire") || strings.Contains(lower, "register") ||
		strings.Contains(lower, "add import") || strings.Contains(lower, "import for")
	return hasProvider && hasWire
}

var blankImportPathRe = regexp.MustCompile(`_\s*"([^"]+)"`)

// providerNameStopwords rejects tokens that the loose "add X" regex would
// otherwise treat as a provider name (F28: "unexported" → llm/providers/unexported).
var providerNameStopwords = map[string]bool{
	"a": true, "an": true, "the": true, "to": true, "into": true, "for": true,
	"new": true, "case": true, "import": true, "package": true, "module": true,
	"file": true, "go": true, "test": true, "tests": true, "cli": true,
	"unexported": true, "exported": true, "internal": true, "provider": true,
	"providers": true, "config": true, "option": true, "entry": true,
	"support": true, "and": true, "or": true, "with": true, "from": true,
}

// extractProviderNameFromTask tries to extract a provider/package name from the
// task input. Returns lowercase kebab/snake form and original capitalized form.
// Example: "Sapiens_AI" → ("sapiensai", "Sapiens_AI")
// Example: "agnes-2.0-flash" → ("agnes", "agnes-2.0-flash")
func extractProviderNameFromTask(taskInput string) (lower string, original string) {
	// Prefer explicit "provider X" forms; avoid the ultra-loose "add X" catch-all first.
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`(?i)provider\s+(?:name\s+)?['"]?([a-zA-Z][a-zA-Z0-9_-]*)['"]?`),
		regexp.MustCompile(`(?i)for\s+['"]?([a-zA-Z][a-zA-Z0-9_-]*)['"]?\s*(?:provider|llm|vision|video)`),
		regexp.MustCompile(`(?i)(?:add|wire|register|integrate|connect)\s+['"]?([a-zA-Z][a-zA-Z0-9_-]*)['"]?\s+(?:provider|llm|vision|video)`),
	}
	for _, re := range patterns {
		if m := re.FindStringSubmatch(taskInput); len(m) >= 2 {
			name := strings.TrimRight(strings.TrimSpace(m[1]), "',;.")
			canon := strings.ToLower(strings.ReplaceAll(name, "_", ""))
			if providerNameStopwords[canon] || len(canon) < 2 {
				continue
			}
			return canon, name
		}
	}
	return "", ""
}

func extractGoImportEdits(taskInput string) []DeterministicEdit {
	var edits []DeterministicEdit

	// Extract the target Go file from task input.
	targetFile := findTargetFile(taskInput, ".go")
	if targetFile == "" {
		return nil // Can't determine file — don't guess.
	}

	// Explicit blank imports — path must resolve on disk at apply time.
	if matches := blankImportPathRe.FindAllStringSubmatch(taskInput, -1); len(matches) > 0 {
		for _, m := range matches {
			imp := strings.TrimSpace(m[1])
			edits = append(edits, DeterministicEdit{
				File:      targetFile,
				Operation: "add_go_import",
				Params:    map[string]string{"import_path": imp},
			})
		}
		return edits
	}

	// Derive import path from provider name — resolved later against disk.
	if provLower, _ := extractProviderNameFromTask(taskInput); provLower != "" {
		edits = append(edits, DeterministicEdit{
			File:      targetFile,
			Operation: "add_go_import",
			Params:    map[string]string{"import_path": "<<provider:" + provLower + ">>"},
		})
	}

	return edits
}

func applyAddGoImport(projectRoot string, edit DeterministicEdit) (bool, error) {
	importPath := edit.Params["import_path"]
	if importPath == "" {
		return false, fmt.Errorf("add_go_import requires import_path param")
	}

	// Resolve placeholder: <<provider:NAME>> → actual import path from go.mod.
	if strings.HasPrefix(importPath, "<<provider:") && strings.HasSuffix(importPath, ">>") {
		provName := importPath[len("<<provider:") : len(importPath)-2]
		resolved := resolveProviderImportPath(projectRoot, provName)
		if resolved == "" {
			return false, fmt.Errorf("could not resolve provider import path for '%s' (no matching package on disk)", provName)
		}
		importPath = resolved
	}

	if isHarnessLeakImportPath(importPath) && !localGoImportExists(projectRoot, importPath) {
		// F28: invented harness paths (llm/providers/…) must never land in
		// user trees. Real packages that exist on disk (avatars self-wiring)
		// still pass via localGoImportExists below.
		return false, fmt.Errorf("refusing harness-leak import path %q", importPath)
	}
	if !localGoImportExists(projectRoot, importPath) {
		return false, fmt.Errorf("refusing import of missing package %q", importPath)
	}

	filePath := findFile(projectRoot, edit.File)
	content, err := os.ReadFile(filePath)
	if err != nil {
		return false, err
	}
	text := string(content)

	// Check if already imported
	if strings.Contains(text, `"`+importPath+`"`) {
		return false, nil // already present
	}

	// Find the last blank import (starting with _ ") or the last regular import
	// before the closing ")" of the import block.
	importBlockEnd := strings.LastIndex(text, "\n)")
	if importBlockEnd < 0 {
		return false, fmt.Errorf("no import block found in %s", filePath)
	}

	// Find the last import line before the closing ")"
	beforeBlock := text[:importBlockEnd]
	lastNewline := strings.LastIndex(strings.TrimRight(beforeBlock, "\n"), "\n")
	if lastNewline < 0 {
		return false, fmt.Errorf("cannot find insertion point in import block")
	}

	// Insert new import line after the last import line with proper newline
	newImport := fmt.Sprintf("\n\t_ \"%s\"", importPath)
	newText := text[:importBlockEnd] + newImport + "\n" + text[importBlockEnd:]

	if err := os.WriteFile(filePath, []byte(newText), 0644); err != nil {
		return false, err
	}
	return true, nil
}

// isHarnessLeakImportPath reports avatars-harness paths that must never land
// in user deliverables (F28).
func isHarnessLeakImportPath(importPath string) bool {
	lower := strings.ToLower(importPath)
	for _, n := range []string{"llm/providers", "skills/approved", "skills/generated", "avatars/internal"} {
		if strings.Contains(lower, n) {
			return true
		}
	}
	return false
}

// localGoImportExists is true when importPath is stdlib/external OR a
// module-local package that exists on disk under projectRoot.
func localGoImportExists(projectRoot, importPath string) bool {
	modulePath := parseGoModModule(projectRoot)
	if modulePath == "" {
		// No go.mod — only allow non-invented absolute-looking paths that exist.
		rel := strings.TrimPrefix(importPath, "/")
		abs := filepath.Join(projectRoot, filepath.FromSlash(rel))
		if info, err := os.Stat(abs); err == nil && info.IsDir() {
			return true
		}
		return false
	}
	if !strings.HasPrefix(importPath, modulePath+"/") && importPath != modulePath {
		// External / stdlib — deterministic blank-import wiring is for local
		// providers only; refuse inventing third-party paths here.
		return false
	}
	rel := strings.TrimPrefix(importPath, modulePath+"/")
	if rel == "" || rel == importPath {
		return false
	}
	abs := filepath.Join(projectRoot, filepath.FromSlash(rel))
	if info, err := os.Stat(abs); err == nil && info.IsDir() {
		return true
	}
	if _, err := os.Stat(abs + ".go"); err == nil {
		return true
	}
	return false
}

func parseGoModModule(projectRoot string) string {
	data, err := os.ReadFile(filepath.Join(projectRoot, "go.mod"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module "))
		}
	}
	return ""
}

// === Slice/List Operations ===

func extractSliceEdits(taskInput string) []DeterministicEdit {
	var edits []DeterministicEdit

	// Generic: find the target Go file from task.
	targetFile := findTargetFile(taskInput, ".go")
	if targetFile == "" || !strings.Contains(strings.ToLower(targetFile), "config") {
		return nil
	}

	// Extract the value to add: provider name from task, or any quoted non-path string.
	_, provOriginal := extractProviderNameFromTask(taskInput)
	value := `"` + strings.ToLower(strings.ReplaceAll(provOriginal, "-", "_")) + `"`
	if provOriginal == "" {
		// Fallback: find quoted strings that aren't paths.
		matches := regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(taskInput, -1)
		for _, m := range matches {
			val := m[1]
			if !strings.Contains(val, ".go") && !strings.Contains(val, "/") && len(val) > 2 {
				value = `"` + val + `"`
				break
			}
		}
	}
	if value == `""` {
		return nil
	}

	// Find which function name was mentioned in the task.
	lower := strings.ToLower(taskInput)
	funcPatterns := []string{"validate", "supported", "allowed", "enabled", "provider"}
	for _, fp := range funcPatterns {
		if strings.Contains(lower, fp) {
			// Use a generic marker — applyAddToSlice will search for the function.
			edits = append(edits, DeterministicEdit{
				File:      targetFile,
				Operation: "add_to_slice",
				Params: map[string]string{
					"function_name": fp, // keyword to search for in the file
					"value":         value,
				},
			})
			break // One edit per file is usually enough
		}
	}

	return edits
}

func applyAddToSlice(projectRoot string, edit DeterministicEdit) (bool, error) {
	funcName := edit.Params["function_name"]
	value := edit.Params["value"]
	if funcName == "" || value == "" {
		return false, fmt.Errorf("add_to_slice requires function_name and value params")
	}

	filePath := findFile(projectRoot, edit.File)
	content, err := os.ReadFile(filePath)
	if err != nil {
		return false, err
	}
	text := string(content)

	// Check if value already in the file
	if strings.Contains(text, value) {
		return false, nil
	}

	// Find the function body first. Support exact match and keyword-based search.
	funcIdx := strings.Index(text, "func "+funcName+"(")
	if funcIdx < 0 {
		// Keyword-based search: find any function whose name contains the keyword.
		funcRe := regexp.MustCompile(`func\s+(\w*` + regexp.QuoteMeta(funcName) + `\w*)\s*\(`)
		if m := funcRe.FindStringIndex(text); m != nil {
			funcIdx = m[0]
		}
	}
	if funcIdx < 0 {
		return false, fmt.Errorf("function matching '%s' not found in %s", funcName, filePath)
	}

	// Find the function's opening brace
	openBrace := strings.Index(text[funcIdx:], "{")
	if openBrace < 0 {
		return false, fmt.Errorf("function body not found for %s", funcName)
	}
	funcBodyStart := funcIdx + openBrace

	// Find the slice literal within the function body: []string{ ... }
	sliceStart := strings.Index(text[funcBodyStart:], "[]string{")
	if sliceStart < 0 {
		return false, fmt.Errorf("[]string slice not found in %s", funcName)
	}
	sliceStart += funcBodyStart + len("[]string{")

	// Find the matching closing brace of the slice (handle nesting)
	sliceClose := findMatchingBrace(text, sliceStart-1)
	if sliceClose < 0 {
		return false, fmt.Errorf("cannot find closing brace of slice in %s", funcName)
	}

	// Insert before the closing "}" — find last non-empty, non-comment item
	beforeBrace := strings.TrimRight(text[sliceStart:sliceClose], " \t\n\r")
	insertPos := sliceStart + len(beforeBrace)
	if beforeBrace == "" {
		insertPos = sliceClose
	}

	// Insert the new value
	comma := ","
	if strings.HasSuffix(strings.TrimSpace(text[sliceStart:sliceClose]), ",") {
		comma = ""
	}
	newText := text[:insertPos] + comma + "\n\t\t" + value + "," + text[insertPos:]

	if err := os.WriteFile(filePath, []byte(newText), 0644); err != nil {
		return false, err
	}
	return true, nil
}

// === Switch Case Operations ===

func extractSwitchCaseEdits(taskInput string) []DeterministicEdit {
	var edits []DeterministicEdit
	lower := strings.ToLower(taskInput)

	// Generic: find all .go files mentioned in task near "case" or "switch" keywords.
	targetFile := findTargetFile(taskInput, ".go")
	if targetFile == "" {
		return nil
	}

	// Only process files that look like config/apply files (common switch pattern).
	if !strings.Contains(lower, "config") && !strings.Contains(lower, "apply") &&
		!strings.Contains(lower, "_config") && !strings.Contains(targetFile, "config") {
		return nil
	}

	// Extract the case value from task description.
	_, provOriginal := extractProviderNameFromTask(taskInput)
	if provOriginal == "" {
		// Try other patterns: "add X as case", "case for X", etc.
		caseRe := regexp.MustCompile(`(?i)case\s+(?:for\s+)?['"]?([a-zA-Z][a-zA-Z0-9_-]*)['"]?`)
		if m := caseRe.FindStringSubmatch(taskInput); len(m) >= 2 {
			provOriginal = strings.TrimRight(m[1], "',;.")
		}
	}
	if provOriginal == "" {
		return nil
	}

	// Determine the switch variable by analyzing the file name.
	// Common patterns: *_config_apply.go → "provider", *_handler.go → "handler", etc.
	switchVar := "provider" // default
	if strings.Contains(lower, "vision") || strings.Contains(targetFile, "vision") {
		switchVar = "provider"
	} else if strings.Contains(lower, "video") || strings.Contains(targetFile, "video") {
		switchVar = "provider"
	}

	// Clean the case value for Go code (lowercase, no special chars).
	caseValue := strings.ToLower(strings.ReplaceAll(provOriginal, "-", "_"))

	edits = append(edits, DeterministicEdit{
		File:      targetFile,
		Operation: "add_switch_case",
		Params: map[string]string{
			"case_value":  `"` + caseValue + `"`,
			"switch_var":  switchVar,
		},
	})

	return edits
}

func applyAddSwitchCase(projectRoot string, edit DeterministicEdit) (bool, error) {
	caseValue := edit.Params["case_value"]
	if caseValue == "" {
		return false, fmt.Errorf("add_switch_case requires case_value param")
	}

	filePath := findFile(projectRoot, edit.File)
	content, err := os.ReadFile(filePath)
	if err != nil {
		return false, err
	}
	text := string(content)

	// Check if case already exists
	if strings.Contains(text, "case "+caseValue+":") {
		return false, nil
	}

	// Find the switch statement
	switchIdx := strings.Index(text, "switch provider {")
	if switchIdx < 0 {
		switchIdx = strings.Index(text, "switch "+edit.Params["switch_var"]+" {")
	}
	if switchIdx < 0 {
		return false, fmt.Errorf("switch statement not found in %s", filePath)
	}

	// Find the "default:" case
	fromSwitch := text[switchIdx:]
	defaultIdx := strings.Index(fromSwitch, "default:")
	if defaultIdx < 0 {
		return false, fmt.Errorf("default case not found in switch")
	}

	insertPos := switchIdx + defaultIdx

	// Generate the new case block based on file context
	newCase := fmt.Sprintf("\tcase %s:\n\t\treturn nil\n\t", caseValue)
	newText := text[:insertPos] + newCase + "\n" + text[insertPos:]

	if err := os.WriteFile(filePath, []byte(newText), 0644); err != nil {
		return false, err
	}
	return true, nil
}

// === JSX MenuItem Operations ===

func extractJSXEdits(taskInput string) []DeterministicEdit {
	var edits []DeterministicEdit

	// Generic: find the target JSX/TSX file from task input.
	targetFile := findTargetFile(taskInput, ".jsx")
	if targetFile == "" {
		targetFile = findTargetFile(taskInput, ".tsx")
	}
	if targetFile == "" {
		return nil
	}

	// Extract provider display name and value from task.
	_, provOriginal := extractProviderNameFromTask(taskInput)
	if provOriginal == "" {
		return nil
	}
	provValue := strings.ToLower(strings.ReplaceAll(provOriginal, "-", "_"))
	provLabel := strings.ReplaceAll(provOriginal, "_", " ") // Human-readable

	// Determine which dropdown types are mentioned in the task.
	lower := strings.ToLower(taskInput)
	providerTypes := []string{}
	for _, pt := range []string{"llm", "vision", "video", "image"} {
		if strings.Contains(lower, pt) {
			providerTypes = append(providerTypes, pt)
		}
	}
	// If no specific type mentioned, add items for all common types.
	if len(providerTypes) == 0 {
		providerTypes = []string{"llm"}
	}

	for _, pt := range providerTypes {
		edits = append(edits, DeterministicEdit{
			File:      targetFile,
			Operation: "add_jsx_menuitem",
			Params: map[string]string{
				"select_id": pt + "_provider",
				"value":     provValue,
				"label":     provLabel,
			},
		})
	}

	return edits
}

func applyAddJSXMenuItem(projectRoot string, edit DeterministicEdit) (bool, error) {
	value := edit.Params["value"]
	label := edit.Params["label"]
	if value == "" || label == "" {
		return false, fmt.Errorf("add_jsx_menuitem requires value and label params")
	}

	filePath := findFile(projectRoot, edit.File)
	content, err := os.ReadFile(filePath)
	if err != nil {
		return false, err
	}
	text := string(content)

	// Check if already present
	newItem := fmt.Sprintf(`<MenuItem value="%s">%s</MenuItem>`, value, label)
	if strings.Contains(text, newItem) {
		return false, nil
	}

	// P2-4a: Use select_id to find the correct select block, then locate
	// the last MenuItem within it. Falls back to searching the whole file.
	anchorItem := `ollama`
	searchText := text
	if selectID := edit.Params["select_id"]; selectID != "" {
		selectMarker := fmt.Sprintf(`id="%s"`, selectID)
		if idx := strings.Index(text, selectMarker); idx >= 0 {
			// Find the closing </Select> after this id.
			closeSelect := strings.Index(text[idx:], "</Select>")
			if closeSelect >= 0 {
				searchText = text[idx : idx+closeSelect]
			}
		}
	}

	anchorIdx := strings.LastIndex(searchText, anchorItem)
	if anchorIdx < 0 {
		// Try alternative anchor
		anchorIdx = strings.LastIndex(text, "MenuItem value=")
		if anchorIdx < 0 {
			return false, fmt.Errorf("cannot find insertion point in %s", filePath)
		}
		// Find the end of this MenuItem line
		lineEnd := strings.Index(text[anchorIdx:], "/>")
		if lineEnd < 0 {
			lineEnd = strings.Index(text[anchorIdx:], "</MenuItem>")
			if lineEnd < 0 {
				return false, fmt.Errorf("cannot find MenuItem end")
			}
			lineEnd += len("</MenuItem>")
		} else {
			lineEnd += len("/>")
		}
		anchorIdx += lineEnd
	} else {
		// Find end of the line containing the anchor
		lineEnd := strings.Index(text[anchorIdx:], "\n")
		if lineEnd < 0 {
			return false, fmt.Errorf("cannot find end of line")
		}
		anchorIdx += lineEnd + 1
	}

	newItemLine := fmt.Sprintf("              %s\n", newItem)
	newText := text[:anchorIdx] + newItemLine + text[anchorIdx:]

	if err := os.WriteFile(filePath, []byte(newText), 0644); err != nil {
		return false, err
	}
	return true, nil
}

// === Helpers ===

// resolveProviderImportPath reads go.mod from projectRoot and constructs
// a full Go import path for a provider package. It tries several patterns:
//  1. module/internal/llm/providers/<name>
//  2. module/pkg/providers/<name>
//  3. Searches existing import paths for similar provider patterns.
//
// Returns "" if the module path cannot be determined.
func resolveProviderImportPath(projectRoot, providerName string) string {
	gomodPath := filepath.Join(projectRoot, "go.mod")
	gomod, err := os.ReadFile(gomodPath)
	if err != nil {
		return ""
	}
	// Extract module name: first line is "module <path>"
	lines := strings.Split(string(gomod), "\n")
	modulePath := ""
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			modulePath = strings.TrimSpace(strings.TrimPrefix(line, "module "))
			break
		}
	}
	if modulePath == "" {
		return ""
	}

	// Try common provider directory patterns. F28: never invent a path that is
	// not on disk (old fallback wrote module/internal/llm/providers/<name>).
	patterns := []string{
		modulePath + "/internal/llm/providers/" + providerName,
		modulePath + "/internal/vision/providers/" + providerName,
		modulePath + "/internal/services/" + providerName,
		modulePath + "/pkg/providers/" + providerName,
	}
	for _, p := range patterns {
		rel := strings.TrimPrefix(p, modulePath+"/")
		dir := filepath.Join(projectRoot, filepath.FromSlash(rel))
		if info, statErr := os.Stat(dir); statErr == nil && info.IsDir() {
			return p
		}
		if _, statErr := os.Stat(dir + ".go"); statErr == nil {
			return p
		}
	}
	return ""
}

func findFile(projectRoot, relativePath string) string {
	if strings.Contains(relativePath, string(os.PathSeparator)) {
		return relativePath
	}
	// Search for the file by name
	var found string
	_ = filepath.WalkDir(projectRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if filepath.Base(path) == relativePath {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	if found != "" {
		return found
	}
	return relativePath // fallback
}

func findTargetFile(taskInput, ext string) string {
	re := regexp.MustCompile(`([a-zA-Z0-9_/]+\.` + ext[1:] + `)`)
	matches := re.FindAllString(taskInput, -1)
	if len(matches) > 0 {
		return matches[0]
	}
	return ""
}

func findMatchingBrace(text string, start int) int {
	if start < 0 {
		return -1
	}
	depth := 0
	for i := start; i < len(text); i++ {
		switch text[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}
