// Package runtime — external dependency blocking for Go file writes.
//
// GAP-2 fix: Prevents Builder from injecting external frameworks (cobra, gin,
// gorm, etc.) without user awareness. Runs BEFORE any Go file is written.
//
// Strategy:
//   1. Maintain a blocklist of known external frameworks (mirrors stdlibAlternative)
//   2. Before writing, scan content for blocked imports
//   3. If found, strip them and emit a warning event
//   4. The Builder can still use the import IF it was already in go.mod before the run
//
// This is the TOOL-LEVEL enforcement of PlannerStablePrefix rule #1
// ("Do NOT specify external frameworks unless the project already imports them").

package runtime

import (
	"strings"
)

// blockedExternalImports is the set of external frameworks that the Builder
// should NOT inject unless the project already uses them.
// This is the tool-level enforcement of the Planner prompt constraint.
var blockedExternalImports = map[string]string{
	"github.com/spf13/cobra":                "flag + os.Args (stdlib) for simple CLIs",
	"github.com/urfave/cli":                 "flag (stdlib)",
	"github.com/spf13/viper":                "os + encoding/json for simple config",
	"github.com/sirupsen/logrus":            "log (stdlib)",
	"go.uber.org/zap":                       "log (stdlib)",
	"github.com/gorilla/mux":                "net/http (stdlib)",
	"github.com/go-chi/chi":                 "net/http (stdlib)",
	"github.com/gin-gonic/gin":              "net/http (stdlib)",
	"github.com/labstack/echo":              "net/http (stdlib)",
	"github.com/jmoiron/sqlx":               "database/sql (stdlib)",
	"gorm.io/gorm":                          "database/sql (stdlib)",
	"github.com/stretchr/testify":           "testing (stdlib)",
	"github.com/google/uuid":                "crypto/rand + fmt.Sprintf",
	"golang.org/x/text":                     "strings + unicode (stdlib)",
}

// IsBlockedExternalImport returns true and the stdlib alternative if the
// given import path is a blocked external framework.
func IsBlockedExternalImport(importPath string) (bool, string) {
	alt, ok := blockedExternalImports[importPath]
	return ok, alt
}

// BlockExternalImports scans Go source content for blocked external imports
// and removes them. Returns the cleaned content and the list of imports
// that were blocked (with their stdlib alternatives).
//
// This is called BEFORE writing any Go file. It is a deterministic,
// zero-LLM-cost guard against framework injection.
func BlockExternalImports(content string) (cleaned string, blocked []BlockedImport) {
	imports := extractGoImports(content)
	if len(imports) == 0 {
		return content, nil
	}

	// Build the set of blocked imports found in this content.
	blockedSet := make(map[string]string) // importPath -> stdlibAlt
	for _, imp := range imports {
		if blocked, alt := IsBlockedExternalImport(imp); blocked {
			blockedSet[imp] = alt
		}
	}
	if len(blockedSet) == 0 {
		return content, nil
	}

	// Remove blocked imports from the content.
	lines := strings.Split(content, "\n")
	var result []string
	skipGroupImport := false
	skipCount := 0

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Handle grouped imports: import ( ... )
		if trimmed == "import (" {
			result = append(result, line)
			skipGroupImport = true
			continue
		}
		if skipGroupImport {
			if trimmed == ")" {
				result = append(result, line)
				skipGroupImport = false
				continue
			}
			impPath := extractImportPathFromLine(trimmed)
			if impPath != "" && blockedSet[impPath] != "" {
				skipCount++
				continue // skip this blocked import
			}
			result = append(result, line)
			continue
		}

		// Single-line imports: import "path"
		if strings.HasPrefix(trimmed, "import ") {
			impPath := extractImportPathFromLine(trimmed)
			if impPath != "" && blockedSet[impPath] != "" {
				skipCount++
				continue
			}
		}

		result = append(result, line)
	}

	// Build the blocked list for event emission.
	for imp, alt := range blockedSet {
		blocked = append(blocked, BlockedImport{
			ImportPath:    imp,
			StdlibAlt:     alt,
			LinesRemoved:  skipCount,
		})
	}

	return strings.Join(result, "\n"), blocked
}

// BlockedImport records a blocked external import.
type BlockedImport struct {
	ImportPath   string
	StdlibAlt    string
	LinesRemoved int
}

// IsNewDependencyInjection checks if the content would introduce any
// external dependency not already present in go.mod. Returns the list
// of new imports that would be added.
//
// Unlike BlockExternalImports (which uses a static blocklist), this
// function compares against the ACTUAL go.mod to detect ANY new
// dependency, not just known frameworks.
func IsNewDependencyInjection(content string, modulePath string, knownDeps map[string]bool) []string {
	imports := extractGoImports(content)
	stdlib := stdlibPackageSet()
	var newDeps []string
	for _, imp := range imports {
		if stdlib[imp] {
			continue
		}
		if strings.HasPrefix(imp, modulePath) {
			continue
		}
		if knownDeps[imp] {
			continue
		}
		newDeps = append(newDeps, imp)
	}
	return newDeps
}
