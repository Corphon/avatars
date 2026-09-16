package runtime

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// DependencyWarning represents an external import that Builder introduced
// but is not present in go.mod. This is the deterministic guard against
// the "cobra problem" (Test 4) — Builder reaches for heavy frameworks
// when simple standard library solutions suffice.
// Phase 4 (P4-1).
type DependencyWarning struct {
	ImportPath string // the full import path (e.g., "github.com/spf13/cobra")
	File       string // the file that imports it
	StdlibAlt  string // suggested standard library alternative, if known
}

// stdlibAlternative maps common external packages Builder might reach for
// to their standard library equivalents. This helps the Critic provide
// actionable feedback rather than just "don't use that."
var stdlibAlternative = map[string]string{
	"github.com/spf13/cobra":                "flag (standard library) for simple CLIs, or os.Args with switch-case for very simple routing",
	"github.com/urfave/cli":                 "flag (standard library)",
	"github.com/spf13/viper":                "os + encoding/json for simple config, or flag for CLI flags",
	"github.com/sirupsen/logrus":            "log (standard library)",
	"go.uber.org/zap":                       "log (standard library)",
	"github.com/gorilla/mux":                "net/http (standard library) — use http.ServeMux for simple routing",
	"github.com/go-chi/chi":                 "net/http (standard library)",
	"github.com/gin-gonic/gin":              "net/http (standard library)",
	"github.com/labstack/echo":              "net/http (standard library)",
	"github.com/jmoiron/sqlx":               "database/sql (standard library)",
	"gorm.io/gorm":                          "database/sql (standard library)",
	"github.com/stretchr/testify":           "testing (standard library) — use t.Run, t.Helper, and manual assertions",
	"github.com/google/uuid":                "crypto/rand + fmt.Sprintf for simple UUIDs, or use a sequential int ID",
	"golang.org/x/text":                     "strings + unicode (standard library) for basic text operations",
}

// CheckExternalDependencies scans Builder-generated Go files for imports
// that are not in go.mod. Returns warnings for each external dependency
// that should not be introduced without explicit approval.
//
// This is a DETERMINISTIC guard — no LLM involvement. It directly
// addresses the Test 4 failure where Builder introduced cobra for a
// simple CLI task.
func CheckExternalDependencies(projectRoot string, generatedFiles []string) []DependencyWarning {
	modulePath, knownDeps := parseGoMod(projectRoot)
	if modulePath == "" {
		// No go.mod — can't validate. This is a non-Go project or the
		// go.mod is malformed. Skip the check gracefully.
		return nil
	}

	var warnings []DependencyWarning
	// Cache stdlib list per call — this is static.
	stdlib := stdlibPackageSet()

	for _, filePath := range generatedFiles {
		if !strings.HasSuffix(filePath, ".go") {
			continue
		}
		fullPath := filePath
		if !filepath.IsAbs(fullPath) {
			fullPath = filepath.Join(projectRoot, fullPath)
		}
		content, err := os.ReadFile(fullPath)
		if err != nil {
			continue
		}
		imports := extractGoImports(string(content))
		for _, imp := range imports {
			// Standard library: always OK.
			if stdlib[imp] {
				continue
			}
			// Project's own packages: verify directory exists on disk.
			// LLM hallucinates paths like internal/llm/providers/routes.
			if strings.HasPrefix(imp, modulePath) {
				relPath := strings.TrimPrefix(imp, modulePath+"/")
				absPath := filepath.Join(projectRoot, filepath.FromSlash(relPath))
				if _, statErr := os.Stat(absPath); statErr == nil { continue }
				if _, statErr := os.Stat(absPath + ".go"); statErr == nil { continue }
				warnings = append(warnings, DependencyWarning{
					ImportPath: imp, File: filePath,
					StdlibAlt:  "(remove - hallucinated import)",
				})
				continue
			}
			// Already in go.mod: OK.
			if knownDeps[imp] {
				continue
			}
			// External package not in go.mod: FLAG.
			alt := ""
			// Check known patterns first.
			for prefix, suggestion := range stdlibAlternative {
				if strings.HasPrefix(imp, prefix) {
					alt = suggestion
					break
				}
			}
			warnings = append(warnings, DependencyWarning{
				ImportPath: imp,
				File:       filePath,
				StdlibAlt:  alt,
			})
		}
	}
	return warnings
}

// parseGoMod reads go.mod from projectRoot and returns the module path
// and the set of known dependency paths.
func parseGoMod(projectRoot string) (modulePath string, knownDeps map[string]bool) {
	knownDeps = make(map[string]bool)
	content, err := os.ReadFile(filepath.Join(projectRoot, "go.mod"))
	if err != nil {
		return "", knownDeps
	}
	lines := strings.Split(string(content), "\n")
	inRequire := false
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		// Module declaration.
		if strings.HasPrefix(line, "module ") {
			modulePath = strings.TrimSpace(strings.TrimPrefix(line, "module "))
			continue
		}
		// Require block or single-line require.
		if line == "require (" {
			inRequire = true
			continue
		}
		if inRequire {
			if line == ")" {
				inRequire = false
				continue
			}
			// Extract package path (before the version).
			depPath := extractDepPath(line)
			if depPath != "" {
				knownDeps[depPath] = true
			}
			continue
		}
		if strings.HasPrefix(line, "require ") {
			depPath := extractDepPath(strings.TrimPrefix(line, "require "))
			if depPath != "" {
				knownDeps[depPath] = true
			}
		}
	}
	return modulePath, knownDeps
}

// extractDepPath extracts the dependency path from a go.mod require line
// like "github.com/spf13/cobra v1.8.0" or "github.com/spf13/cobra v1.8.0 // indirect".
func extractDepPath(line string) string {
	line = strings.TrimSpace(line)
	// Find the first whitespace — everything before it is the package path.
	spaceIdx := strings.Index(line, " ")
	if spaceIdx < 0 {
		return line // single token, could be a path
	}
	return line[:spaceIdx]
}

// extractGoImports extracts import paths from Go source code.
// Handles both single imports and grouped imports (import ( ... )).
func extractGoImports(source string) []string {
	var imports []string
	seen := make(map[string]bool)

	// Match grouped imports: import ( "pkg1"; "pkg2" )
	groupRe := regexp.MustCompile(`import\s*\(([^)]*)\)`)
	if match := groupRe.FindStringSubmatch(source); len(match) >= 2 {
		// Extract quoted strings from the group body.
		quotedRe := regexp.MustCompile(`"([^"]+)"`)
		for _, submatch := range quotedRe.FindAllStringSubmatch(match[1], -1) {
			pkg := submatch[1]
			if !seen[pkg] {
				seen[pkg] = true
				imports = append(imports, pkg)
			}
		}
	}

	// Match single imports: import "pkg" or import alias "pkg"
	singleRe := regexp.MustCompile(`import\s+(?:\w+\s+)?\s*"([^"]+)"`)
	for _, submatch := range singleRe.FindAllStringSubmatch(source, -1) {
		pkg := submatch[1]
		if !seen[pkg] {
			seen[pkg] = true
			imports = append(imports, pkg)
		}
	}

	return imports
}

// stdlibPackageSet returns a set of all Go standard library package paths.
// Cached after first call since stdlib is static.
var _stdlibCache map[string]bool

func stdlibPackageSet() map[string]bool {
	if _stdlibCache != nil {
		return _stdlibCache
	}
	// Comprehensive list of Go 1.24 standard library packages.
	pkgs := []string{
		// Core
		"fmt", "os", "io", "bufio", "bytes", "strings", "strconv",
		"errors", "sort", "time", "sync", "context", "math", "regexp",
		"unicode", "unicode/utf8", "unicode/utf16",
		// Containers
		"container/heap", "container/list", "container/ring",
		// Crypto
		"crypto", "crypto/aes", "crypto/cipher", "crypto/des",
		"crypto/dsa", "crypto/ecdh", "crypto/ecdsa", "crypto/ed25519",
		"crypto/elliptic", "crypto/hmac", "crypto/md5", "crypto/rand",
		"crypto/rc4", "crypto/rsa", "crypto/sha1", "crypto/sha256",
		"crypto/sha512", "crypto/subtle", "crypto/tls", "crypto/x509",
		"crypto/x509/pkix",
		// Database
		"database/sql", "database/sql/driver",
		// Encoding
		"encoding", "encoding/ascii85", "encoding/asn1",
		"encoding/base32", "encoding/base64", "encoding/binary",
		"encoding/csv", "encoding/gob", "encoding/hex",
		"encoding/json", "encoding/pem", "encoding/xml",
		// Hash
		"hash", "hash/adler32", "hash/crc32", "hash/crc64",
		"hash/fnv", "hash/maphash",
		// HTML/Template
		"html", "html/template",
		// Image
		"image", "image/color", "image/color/palette",
		"image/draw", "image/gif", "image/jpeg", "image/png",
		// IO
		"io/fs", "io/ioutil",
		// Log
		"log", "log/slog", "log/syslog",
		// Math
		"math/big", "math/bits", "math/cmplx", "math/rand",
		// MIME
		"mime", "mime/multipart", "mime/quotedprintable",
		// Net
		"net", "net/http", "net/http/cgi", "net/http/cookiejar",
		"net/http/fcgi", "net/http/httptest", "net/http/httptrace",
		"net/http/httputil", "net/http/pprof",
		"net/mail", "net/netip", "net/rpc", "net/rpc/jsonrpc",
		"net/smtp", "net/textproto", "net/url",
		// OS
		"os/exec", "os/signal", "os/user",
		// Path
		"path", "path/filepath",
		// Plugin
		"plugin",
		// Reflect
		"reflect",
		// Runtime
		"runtime", "runtime/cgo", "runtime/coverage",
		"runtime/debug", "runtime/metrics", "runtime/pprof",
		"runtime/race", "runtime/trace",
		// Sync
		"sync/atomic",
		// Testing
		"testing", "testing/fstest", "testing/iotest", "testing/quick",
		// Text
		"text/scanner", "text/tabwriter", "text/template",
		"text/template/parse",
		// Go tools
		"go/ast", "go/build", "go/constant", "go/doc",
		"go/format", "go/importer", "go/parser", "go/printer",
		"go/scanner", "go/token", "go/types",
		// Other
		"archive/tar", "archive/zip",
		"compress/bzip2", "compress/flate", "compress/gzip",
		"compress/lzw", "compress/zlib",
		"debug/buildinfo", "debug/dwarf", "debug/elf",
		"debug/gosym", "debug/macho", "debug/pe", "debug/plan9obj",
		"embed",
		"expvar",
		"flag",
		"index/suffixarray",
		"maps",
		"slices",
		"cmp",
		"unique",
		"iter",
		"structs",
	}
	_cache := make(map[string]bool, len(pkgs))
	for _, p := range pkgs {
		_cache[p] = true
	}
	_stdlibCache = _cache
	return _stdlibCache
}
