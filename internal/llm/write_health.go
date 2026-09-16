package llm

import (
	"fmt"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"sync"
)

// WriteHealthChecker optionally replaces/extends built-in write rejection.
// Runtime registers checkFileHealth so tool-loop writes share Builder gates (J2-1).
type WriteHealthChecker func(path, content string) error

// WriteContentMutator optionally rewrites content before health checks / disk write
// (F63: coerce Go lib-dir *_test.go package clauses).
type WriteContentMutator func(path, content string) string

var (
	writeHealthMu     sync.Mutex
	writeHealthFn     WriteHealthChecker
	writeContentMutFn WriteContentMutator
)

// SetWriteHealthChecker registers a process-wide write health hook.
func SetWriteHealthChecker(fn WriteHealthChecker) {
	writeHealthMu.Lock()
	defer writeHealthMu.Unlock()
	writeHealthFn = fn
}

// ClearWriteHealthChecker removes the registered write health hook.
func ClearWriteHealthChecker() {
	SetWriteHealthChecker(nil)
}

// SetWriteContentMutator registers a process-wide content rewrite hook.
func SetWriteContentMutator(fn WriteContentMutator) {
	writeHealthMu.Lock()
	defer writeHealthMu.Unlock()
	writeContentMutFn = fn
}

// ClearWriteContentMutator removes the registered content mutator.
func ClearWriteContentMutator() {
	SetWriteContentMutator(nil)
}

// prepareWriteContent applies optional mutation then health rejection.
// Callers must persist the returned content (may differ from input).
func prepareWriteContent(path, content string) (string, error) {
	writeHealthMu.Lock()
	mut := writeContentMutFn
	writeHealthMu.Unlock()
	if mut != nil {
		if next := mut(path, content); next != "" {
			content = next
		}
	}
	return content, rejectUnhealthyWrite(path, content)
}

// rejectUnhealthyWrite returns an error if content looks truncated/broken
// for the given path. Used by write_file / edit_file tool paths so bad
// LLM output never lands on disk (NL smoke: store_test.go / auth.go truncations).
// J2-1: covers JS/TS/Py/Rs brace+paren completeness, not only Go.
func rejectUnhealthyWrite(path, content string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	writeHealthMu.Lock()
	fn := writeHealthFn
	writeHealthMu.Unlock()
	if fn != nil {
		if err := fn(path, content); err != nil {
			return err
		}
		return nil
	}
	return builtinRejectUnhealthyWrite(path, content)
}

func builtinRejectUnhealthyWrite(path, content string) error {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".go":
		trimmed := strings.TrimSpace(content)
		base := strings.ToLower(filepath.Base(path))
		slash := filepath.ToSlash(strings.ToLower(path))
		isEntrypoint := base == "main.go" || strings.Contains(slash, "/cmd/")
		if isEntrypoint && strings.HasPrefix(trimmed, "package main") && !strings.Contains(trimmed, "func main") {
			return fmt.Errorf("write rejected: %s is a hollow package main with no func main", path)
		}
		if isEntrypoint && strings.HasPrefix(trimmed, "package main") && strings.Contains(trimmed, "func main") {
			expectHTTP := strings.Contains(slash, "/server/") || strings.Contains(slash, "/api/")
			if lightweightHollowHTTPMain(path, trimmed, expectHTTP) {
				return fmt.Errorf("write rejected: %s has empty/unwired func main (hollow HTTP entrypoint)", path)
			}
		}
		if len(trimmed) < 50 && !strings.HasSuffix(path, "_test.go") && !strings.Contains(trimmed, "func ") {
			return fmt.Errorf("write rejected: Go file %s is too small (%d bytes) — likely empty or stub", path, len(trimmed))
		}
		fset := token.NewFileSet()
		if _, err := parser.ParseFile(fset, path, content, parser.ParseComments); err != nil {
			return fmt.Errorf("write rejected: Go syntax error in %s: %w", path, err)
		}
		if reason := lightweightHarnessLeakReject(path, content); reason != "" {
			return fmt.Errorf("write rejected: %s", reason)
		}
	case ".js", ".mjs", ".cjs", ".ts", ".tsx", ".jsx", ".py", ".rs":
		if reason := lightweightHollowHTTPEntrypoint(path, content); reason != "" {
			return fmt.Errorf("write rejected: %s", reason)
		}
		if reason := lightweightCompletenessReject(path, content); reason != "" {
			return fmt.Errorf("write rejected: %s", reason)
		}
		if reason := lightweightHarnessLeakReject(path, content); reason != "" {
			return fmt.Errorf("write rejected: %s", reason)
		}
	case ".toml":
		if reason := lightweightTomlDuplicateReject(path, content); reason != "" {
			return fmt.Errorf("write rejected: %s", reason)
		}
	}
	return nil
}

// lightweightTomlDuplicateReject mirrors runtime tomlDuplicateKeyReason.
func lightweightTomlDuplicateReject(path, content string) string {
	section := ""
	seen := map[string]map[string]bool{}
	for i, line := range strings.Split(content, "\n") {
		trim := strings.TrimSpace(line)
		if trim == "" || strings.HasPrefix(trim, "#") {
			continue
		}
		if strings.HasPrefix(trim, "[") && strings.Contains(trim, "]") {
			end := strings.Index(trim, "]")
			section = strings.TrimSpace(trim[1:end])
			if seen[section] == nil {
				seen[section] = map[string]bool{}
			}
			continue
		}
		eq := strings.Index(trim, "=")
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(trim[:eq])
		if key == "" || strings.ContainsAny(key, " \t") {
			continue
		}
		if seen[section] == nil {
			seen[section] = map[string]bool{}
		}
		if seen[section][key] {
			return fmt.Sprintf("%s has duplicate TOML key %q (line %d)", path, key, i+1)
		}
		seen[section][key] = true
	}
	return ""
}

// lightweightCompletenessReject catches truncated sources without importing runtime.
func lightweightCompletenessReject(path, content string) string {
	trimmed := strings.TrimSpace(content)
	ext := strings.ToLower(filepath.Ext(path))
	// T2/T3 mirror (runtime/poison_stub.go) — keep builtin path useful when
	// WriteHealthChecker is not registered (unit tests / early boot).
	if reason := lightweightPoisonStubReject(path, trimmed, ext); reason != "" {
		return reason
	}
	if len(trimmed) < 20 {
		return fmt.Sprintf("%s is too small (%d bytes) — likely truncated", path, len(trimmed))
	}
	stripped, unclosed := SourceWithoutLiterals(content)
	if unclosed {
		return fmt.Sprintf("%s has an unclosed string or comment", path)
	}
	if open, close := strings.Count(stripped, "{"), strings.Count(stripped, "}"); open != close {
		return fmt.Sprintf("%s has unbalanced braces: %d { vs %d }", path, open, close)
	}
	lines := strings.Split(content, "\n")
	last := ""
	for i := len(lines) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(lines[i]); t != "" && !strings.HasPrefix(t, "//") && !strings.HasPrefix(t, "#") {
			last = t
			break
		}
	}
	if last == "" {
		return ""
	}
	// Mid-expression cutoffs: "Math.abs(now - createdTime" / "assert.ok("
	if strings.HasSuffix(last, ",") || strings.HasSuffix(last, "(") ||
		strings.HasSuffix(last, "&&") || strings.HasSuffix(last, "||") ||
		strings.HasSuffix(last, "?") ||
		(strings.HasSuffix(last, ":") && !strings.HasSuffix(last, "::")) {
		return fmt.Sprintf("%s ends with truncated expression: %q", path, truncateForReject(last, 80))
	}
	if !strings.HasSuffix(last, "}") && !strings.HasSuffix(last, ")") &&
		!strings.HasSuffix(last, ";") && !strings.HasSuffix(last, "`") &&
		!strings.HasSuffix(last, "\"") && !strings.HasSuffix(last, "'") &&
		!strings.HasSuffix(last, "*/") && len(content) > 200 {
		// Incomplete call like "Math.abs(now - createdTime" without closing paren
		// already caught by paren balance; flag other abrupt endings.
		if strings.ContainsAny(last, "([{") && !strings.ContainsAny(last, ")]}") {
			return fmt.Sprintf("%s last line looks incomplete: %q", path, truncateForReject(last, 80))
		}
	}
	return ""
}

func truncateForReject(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// lightweightPoisonStubReject mirrors runtime looksLikePoisonStubContent /
// isJunkScaffoldPath without importing runtime (cycle avoidance).
func lightweightPoisonStubReject(path, trimmed, ext string) string {
	base := strings.ToLower(filepath.Base(path))
	slash := filepath.ToSlash(strings.ToLower(path))
	switch base {
	case "d.ts", "tsconfig.js", "package.js", "typescript.js",
		"sql.js", "node.js", "express.js", "test.js":
		return fmt.Sprintf("%s is a junk/dependency scaffold path", path)
	}
	if (slash == "test.ts" || strings.HasSuffix(slash, "/test/test.ts") ||
		strings.HasSuffix(slash, "/test/test.js")) &&
		(ext == ".ts" || ext == ".js" || ext == ".tsx" || ext == ".jsx") {
		return fmt.Sprintf("%s is a junk scaffold path", path)
	}
	for _, part := range strings.Split(slash, "/") {
		switch part {
		case "zzdiag", "zzprobe", "_diag", "__diag__", "tmpdiag":
			return fmt.Sprintf("%s is a junk diagnostic probe path", path)
		}
	}
	baseName := strings.ToLower(filepath.Base(slash))
	if strings.HasPrefix(baseName, "zzdiag") || strings.HasPrefix(baseName, "zzprobe") {
		return fmt.Sprintf("%s is a junk diagnostic probe path", path)
	}
	switch ext {
	case ".js", ".mjs", ".cjs", ".jsx", ".ts", ".tsx":
		if strings.HasPrefix(trimmed, "#") {
			return fmt.Sprintf("%s starts with '#' (invalid JS/TS — poison stub)", path)
		}
	}
	lower := strings.ToLower(trimmed)
	if !strings.Contains(lower, "stub") {
		return ""
	}
	hasCode := strings.Contains(trimmed, "function") || strings.Contains(trimmed, "=>") ||
		strings.Contains(trimmed, "export ") || strings.Contains(trimmed, "import ") ||
		strings.Contains(trimmed, "const ") || strings.Contains(trimmed, "def ") ||
		strings.Contains(trimmed, "func ") || strings.Contains(trimmed, "fn ") ||
		strings.Contains(trimmed, "class ")
	if hasCode {
		return ""
	}
	if strings.Contains(lower, "— stub") || strings.Contains(lower, "- stub") ||
		strings.Contains(lower, "llm did not") {
		return fmt.Sprintf("%s looks like a comment-only poison stub", path)
	}
	return ""
}

// lightweightHarnessLeakReject mirrors runtime harnessContextLeakReason (G1).
func lightweightHarnessLeakReject(path, content string) string {
	slash := strings.ToLower(filepath.ToSlash(path))
	if strings.Contains(slash, "/llm/providers/") ||
		strings.Contains(slash, "/skills/approved/") ||
		strings.Contains(slash, "/skills/generated/") {
		return fmt.Sprintf("%s path looks like avatars harness leak — refuse", path)
	}
	for i, line := range strings.Split(content, "\n") {
		trim := strings.TrimSpace(line)
		if trim == "" || strings.HasPrefix(trim, "//") || strings.HasPrefix(trim, "#") {
			continue
		}
		lower := strings.ToLower(trim)
		ext := strings.ToLower(filepath.Ext(path))
		looksImportish := strings.Contains(lower, "import") || strings.Contains(lower, "require") ||
			strings.Contains(lower, "from ") ||
			(ext == ".go" && strings.Contains(trim, `"`) && (strings.Contains(lower, "/") || strings.HasPrefix(trim, "_")))
		if !looksImportish {
			continue
		}
		for _, n := range []string{"llm/providers", "skills/approved", "skills/generated", "avatars/internal"} {
			if strings.Contains(lower, n) {
				return fmt.Sprintf("%s line %d: harness context leak — refuse", path, i+1)
			}
		}
	}
	return ""
}

// lightweightHollowHTTPMain mirrors runtime hollowHTTPEntrypointReason for empty
// or unwired Go mains under server/api paths (W3). Cross-lang listen tokens
// keep Node/Python-shaped bodies from being false-rejected.
func lightweightHollowHTTPMain(path, body string, expectHTTP bool) bool {
	rel := strings.ToLower(filepath.ToSlash(path))
	serverish := expectHTTP || strings.Contains(rel, "/server/") || strings.Contains(rel, "/api/")
	if !serverish {
		return false
	}
	lower := strings.ToLower(body)
	for _, t := range []string{
		"listenandserve", "listenandservetls", "http.server", ".listen(",
		"handlefunc(", "servemux", "express()", "createserver", "fastapi(", "app.run(",
		"uvicorn", "chi.newrouter", "gin.default",
	} {
		if strings.Contains(lower, t) {
			return false
		}
	}
	idx := strings.Index(body, "func main")
	if idx < 0 {
		return true
	}
	rest := body[idx:]
	brace := strings.Index(rest, "{")
	if brace < 0 {
		return true
	}
	depth := 0
	var inner strings.Builder
	for i := brace; i < len(rest); i++ {
		c := rest[i]
		if c == '{' {
			depth++
			if depth == 1 {
				continue
			}
		}
		if c == '}' {
			depth--
			if depth == 0 {
				break
			}
		}
		if depth >= 1 {
			inner.WriteByte(c)
		}
	}
	core := strings.TrimSpace(inner.String())
	if core == "" || core == "return" || core == "return nil" {
		return true
	}
	for _, line := range strings.Split(core, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "//") {
			continue
		}
		return false
	}
	return true
}

// lightweightHollowHTTPEntrypoint rejects near-empty Node/Python HTTP entry files (W3).
func lightweightHollowHTTPEntrypoint(path, content string) string {
	slash := strings.ToLower(filepath.ToSlash(path))
	base := strings.ToLower(filepath.Base(path))
	ext := strings.ToLower(filepath.Ext(path))
	entryName := false
	switch base {
	case "server.js", "server.mjs", "server.cjs", "server.ts", "app.js", "app.ts",
		"index.js", "index.ts", "main.js", "main.ts",
		"server.py", "app.py", "main.py", "__main__.py":
		entryName = true
	}
	serverish := strings.Contains(slash, "/server/") || strings.Contains(slash, "/api/") ||
		base == "server.js" || base == "server.ts" || base == "server.py"
	if !entryName || !serverish {
		return ""
	}
	switch ext {
	case ".js", ".mjs", ".cjs", ".ts", ".tsx", ".py":
	default:
		return ""
	}
	trimmed := strings.TrimSpace(content)
	lower := strings.ToLower(trimmed)
	for _, t := range []string{
		".listen(", "createserver", "express()", "fastapi(", "app.run(",
		"uvicorn", "http.server", "listenandserve",
	} {
		if strings.Contains(lower, t) {
			return ""
		}
	}
	if len(trimmed) < 80 {
		return fmt.Sprintf("%s looks like an empty/unwired HTTP entrypoint", path)
	}
	return ""
}

// SourceWithoutLiterals drops quotes, raw strings, and comments so delimiter
// counts see only program structure. Shared by write gates and truncation
// recovery. Common across Go/Python/JS/TS/Rust/Java.
func SourceWithoutLiterals(src string) (string, bool) {
	var b strings.Builder
	b.Grow(len(src))
	n := len(src)
	unclosed := false
	i := 0
	for i < n {
		c := src[i]
		if c == '/' && i+1 < n {
			next := src[i+1]
			if next == '/' {
				for i < n && src[i] != '\n' {
					i++
				}
				continue
			}
			if next == '*' {
				i += 2
				for i+1 < n && !(src[i] == '*' && src[i+1] == '/') {
					i++
				}
				if i+1 >= n {
					return b.String(), true
				}
				i += 2
				continue
			}
		}
		if c == '#' {
			for i < n && src[i] != '\n' {
				i++
			}
			continue
		}
		if c == '"' || c == '\'' || c == '`' {
			quote := c
			i++
			for i < n {
				if src[i] == '\\' && quote != '`' && i+1 < n {
					i += 2
					continue
				}
				if src[i] == quote {
					i++
					quote = 0
					break
				}
				i++
			}
			if quote != 0 {
				return b.String(), true
			}
			continue
		}
		b.WriteByte(c)
		i++
	}
	return b.String(), unclosed
}
