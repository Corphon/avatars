package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	var cssFiles []string
	filepath.Walk(".", func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if strings.HasSuffix(strings.ToLower(info.Name()), ".css") {
			cssFiles = append(cssFiles, path)
		}
		return nil
	})

	if len(cssFiles) == 0 {
		fmt.Println("0 CSS files checked, 0 passed, 0 failed")
		return
	}

	var failures []string
	passed := 0
	for _, file := range cssFiles {
		if errs := checkCSS(file); len(errs) > 0 {
			for _, e := range errs {
				failures = append(failures, file+": "+e)
			}
		} else {
			passed++
		}
	}

	for _, f := range failures {
		fmt.Println(f)
	}
	fmt.Printf("%d CSS files checked, %d passed, %d failed\n", len(cssFiles), passed, len(failures))
	if len(failures) > 0 {
		os.Exit(1)
	}
}

// checkCSS checks a single CSS file and returns a list of error messages.
func checkCSS(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return []string{fmt.Sprintf("cannot read: %v", err)}
	}
	// Remove CSS comments before analysis.
	content := removeComments(string(data))

	var errs []string

	// 1. Balanced braces — count { and } in the comment-free content.
	opens := strings.Count(content, "{")
	closes := strings.Count(content, "}")
	if opens != closes {
		errs = append(errs, fmt.Sprintf("unbalanced braces (%d {, %d })", opens, closes))
	}

	lines := strings.Split(content, "\n")

	// 2. Empty rulesets — selectors with nothing between { and }.
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// Single-line: selector { }
		if si := strings.Index(trimmed, "{"); si >= 0 {
			if ei := strings.LastIndex(trimmed, "}"); ei > si {
				inner := strings.TrimSpace(trimmed[si+1 : ei])
				if inner == "" && si > 0 {
					errs = append(errs, fmt.Sprintf("empty ruleset near line %d", i+1))
				}
			}
		}
		// Multi-line: line ends with { and next non-empty is }
		if strings.HasSuffix(trimmed, "{") || trimmed == "{" {
			for j := i + 1; j < len(lines); j++ {
				next := strings.TrimSpace(lines[j])
				if next == "" {
					continue
				}
				if next == "}" {
					errs = append(errs, fmt.Sprintf("empty ruleset near line %d", i+1))
				}
				break
			}
		}
	}

	// 3. Missing semicolons — property declarations that lack a trailing ;.
	// Handles multi-line values (e.g. font-family with fallbacks) by scanning
	// forward within the block before flagging.
	braceDepth := 0
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		opening := strings.Count(trimmed, "{")
		closing := strings.Count(trimmed, "}")
		wasInBlock := braceDepth > 0
		braceDepth += opening - closing

		if wasInBlock && opening == 0 && closing == 0 && strings.Contains(trimmed, ":") {
			colIdx := strings.Index(trimmed, ":")
			prop := strings.TrimSpace(trimmed[:colIdx])
			if prop != "" && !strings.Contains(prop, " ") && !strings.HasPrefix(prop, "@") {
				if strings.HasSuffix(trimmed, ";") {
					continue
				}
				// Line doesn't end with ; — could be a multi-line value.
				// Scan forward for a ; before the block closes or a new property starts.
				foundSC := false
				for j := i + 1; j < len(lines); j++ {
					next := strings.TrimSpace(lines[j])
					if next == "" || strings.HasPrefix(next, "/*") {
						continue
					}
					if next == "}" {
						break
					}
					// If next line starts a new property (has : with no space before it),
					// the original line is genuinely missing its semicolon.
					if nc := strings.Index(next, ":"); nc > 0 {
						np := strings.TrimSpace(next[:nc])
						if np != "" && !strings.Contains(np, " ") && !strings.HasPrefix(np, "@") {
							break
						}
					}
					if strings.HasSuffix(next, ";") {
						foundSC = true
						break
					}
				}
				if !foundSC {
					errs = append(errs, fmt.Sprintf("missing semicolon near line %d", i+1))
				}
			}
		}
	}

	return errs
}

// removeComments strips CSS block comments /* ... */ from the input.
func removeComments(s string) string {
	var out strings.Builder
	for i := 0; i < len(s); i++ {
		if i+1 < len(s) && s[i] == '/' && s[i+1] == '*' {
			end := strings.Index(s[i+2:], "*/")
			if end >= 0 {
				i += end + 3
				continue
			}
		}
		out.WriteByte(s[i])
	}
	return out.String()
}
