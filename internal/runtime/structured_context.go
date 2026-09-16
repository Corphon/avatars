package runtime

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// === Structured Avatar Communication Protocol ===
//
// Instead of passing raw text strings between avatars (Researcher→Builder→Critic),
// this protocol carries typed data that eliminates LLM guesswork.
//
// Key design principles:
//   - Extracted from source files, not LLM-generated (deterministic)
//   - Stored in skill files for cross-run reuse (learning loop)
//   - Presented prominently in Builder prompts (precision over recall)
//   - Foundation for future Verifier→Builder feedback

// StructuredContext carries typed project knowledge alongside the raw
// text summary. It is populated by the Researcher from source file
// analysis and consumed by the Builder to generate accurate code.
type StructuredContext struct {
	// Raw text summary (backward compatible with existing code paths).
	Summary string

	// InterfaceDecls contains interface declarations extracted from
	// Go source files (type X interface { ... }).
	InterfaceDecls []InterfaceDecl

	// StructDecls contains struct type declarations.
	StructDecls []StructDecl

	// FuncDecls contains exported function signatures.
	FuncDecls []FuncDecl

	// RegistryPatterns records how components are registered
	// (e.g., "registry.Register(name, constructor)").
	RegistryPatterns []string

	// LayoutConventions describes where files of each type should be
	// placed (package name, directory, file naming pattern).
	LayoutConventions []LayoutConvention
}

// LayoutConvention describes the expected location and package for
// a category of source files.
type LayoutConvention struct {
	Category    string // e.g., "plugin", "handler", "model"
	Dir         string // e.g., "internal/plugins"
	Package     string // e.g., "plugins"
	FilePattern string // e.g., "snake_case.go" or "subdir/name.go"
}

// InterfaceDecl represents a Go interface declaration.
type InterfaceDecl struct {
	Name    string      // Interface name
	Package string      // Package name
	File    string      // Source file path
	Methods []MethodSig // Method signatures
}

// StructDecl represents a Go struct type declaration.
type StructDecl struct {
	Name    string // Struct name
	Package string // Package name
	File    string // Source file path
	Fields  []FieldSig
}

// FieldSig represents a struct field.
type FieldSig struct {
	Name string
	Type string
}

// MethodSig represents a method signature.
type MethodSig struct {
	Name    string // Method name
	Params  string // Full params string e.g., "ctx context.Context, payload Payload"
	Returns string // Full returns string e.g., "(Payload, error)"
}

// FuncDecl represents an exported function declaration.
type FuncDecl struct {
	Name    string
	Package string
	File    string
	Params  string
	Returns string
}

// Empty returns true if no structured data was extracted.
func (ctx *StructuredContext) Empty() bool {
	return len(ctx.InterfaceDecls) == 0 &&
		len(ctx.StructDecls) == 0 &&
		len(ctx.FuncDecls) == 0 &&
		len(ctx.RegistryPatterns) == 0 &&
		len(ctx.LayoutConventions) == 0
}

// extractStructuredContext analyzes source files and extracts typed
// declarations. This is deterministic (no LLM call) and serves as
// the foundation for precise avatar communication.
func extractStructuredContext(summary string, sourceFiles []string) *StructuredContext {
	ctx := &StructuredContext{Summary: summary}
	if len(sourceFiles) == 0 {
		return ctx
	}

	for _, path := range sourceFiles {
		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		text := string(content)
		ext := strings.ToLower(strings.TrimPrefix(pathExt(path), "."))

		switch ext {
		case "go":
			ctx.extractGoDeclarations(text, path)
			// AST cross-check: use go/parser for more accurate extraction
			// and merge any declarations the regex missed.
			if astCtx := extractStructuredContextViaAST(path); astCtx != nil {
				ctx = mergeASTIntoRegex(ctx, astCtx)
			}
		case "ts", "tsx":
			ctx.extractTSDeclarations(text, path)
		}
	}

	// P3-3: Extract project layout conventions so the Builder
	// knows WHERE to place new files (package, directory, naming).
	ctx.LayoutConventions = extractLayoutConventions(sourceFiles)

	return ctx
}

// --- Go source extraction ---

func (ctx *StructuredContext) extractGoDeclarations(source string, filePath string) {
	pkg := extractGoPackage(source)

	// Extract interface declarations: type X interface { ... }
	ctx.extractGoInterfaces(source, pkg, filePath)

	// Extract struct declarations: type X struct { ... }
	ctx.extractGoStructs(source, pkg, filePath)

	// Extract exported function declarations
	ctx.extractGoFunctions(source, pkg, filePath)

	// Detect registry patterns
	ctx.detectGoRegistryPatterns(source, filePath)
}

func (ctx *StructuredContext) extractGoInterfaces(source string, pkg string, filePath string) {
	// Match: type InterfaceName interface { ... }
	// Handles single-level nesting in method params.
	re := regexp.MustCompile(`type\s+(\w+)\s+interface\s*\{`)
	matches := re.FindAllStringSubmatchIndex(source, -1)

	for _, match := range matches {
		name := source[match[2]:match[3]]
		bodyStart := match[1] // after the opening brace
		body := extractBraceBlock(source[bodyStart:])
		methods := parseGoInterfaceMethods(body)

		if len(methods) > 0 || len(body) > 0 {
			ctx.InterfaceDecls = append(ctx.InterfaceDecls, InterfaceDecl{
				Name:    name,
				Package: pkg,
				File:    filePath,
				Methods: methods,
			})
		}
	}
}

func (ctx *StructuredContext) extractGoStructs(source string, pkg string, filePath string) {
	re := regexp.MustCompile(`type\s+(\w+)\s+struct\s*\{`)
	matches := re.FindAllStringSubmatchIndex(source, -1)

	for _, match := range matches {
		name := source[match[2]:match[3]]
		bodyStart := match[1]
		body := extractBraceBlock(source[bodyStart:])
		fields := parseGoStructFields(body)

		if len(fields) > 0 {
			ctx.StructDecls = append(ctx.StructDecls, StructDecl{
				Name:    name,
				Package: pkg,
				File:    filePath,
				Fields:  fields,
			})
		}
	}
}

func (ctx *StructuredContext) extractGoFunctions(source string, pkg string, filePath string) {
	// Match exported functions: func FuncName(params) (returns) {
	re := regexp.MustCompile(`func\s+([A-Z]\w*)\s*\(([^)]*)\)\s*(\([^)]*\)|[\w\[\]\*\.]+)?\s*\{?`)
	matches := re.FindAllStringSubmatch(source, -1)

	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		name := m[1]
		if name == "" || isGoKeyword(name) {
			continue
		}
		params := strings.TrimSpace(m[2])
		returns := ""
		if len(m) >= 4 {
			returns = strings.TrimSpace(m[3])
		}
		ctx.FuncDecls = append(ctx.FuncDecls, FuncDecl{
			Name:    name,
			Package: pkg,
			File:    filePath,
			Params:  params,
			Returns: returns,
		})
	}
}

func (ctx *StructuredContext) detectGoRegistryPatterns(source string, filePath string) {
	// Detect common Go registration patterns.
	patterns := []struct {
		re      *regexp.Regexp
		pattern string
	}{
		{regexp.MustCompile(`\.Register\s*\(\s*`), "registry.Register(name, value)"},
		{regexp.MustCompile(`registry\s*\.\s*Register`), "registry.Register(name, value)"},
		{regexp.MustCompile(`Register\w*\s*\(\s*\"`), "RegisterName(\"name\", ...)"},
		{regexp.MustCompile(`init\s*\(\s*\)\s*\{`), "init()-based registration"},
	}
	for _, p := range patterns {
		if p.re.MatchString(source) {
			ctx.RegistryPatterns = append(ctx.RegistryPatterns,
				filePath+": "+p.pattern)
			break // one pattern per file is enough
		}
	}
}

// --- TypeScript source extraction (lightweight) ---

func (ctx *StructuredContext) extractTSDeclarations(source string, filePath string) {
	// Extract interface declarations
	re := regexp.MustCompile(`(?:export\s+)?interface\s+(\w+)\s*\{`)
	matches := re.FindAllStringSubmatchIndex(source, -1)
	for _, match := range matches {
		name := source[match[2]:match[3]]
		bodyStart := match[1]
		body := extractBraceBlock(source[bodyStart:])
		methods := parseTSInterfaceMethods(body)
		if len(methods) > 0 {
			ctx.InterfaceDecls = append(ctx.InterfaceDecls, InterfaceDecl{
				Name:    name,
				Package: filePath,
				File:    filePath,
				Methods: methods,
			})
		}
	}
}

// --- Brace block extraction ---
// extractBraceBlock consumes a balanced { ... } block from the source.
func extractBraceBlock(source string) string {
	if len(source) == 0 || source[0] != '{' {
		return source
	}
	depth := 0
	for i, c := range source {
		switch c {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return source[1:i] // content between braces
			}
		}
	}
	return source[1:] // fallback: return everything after opening brace
}

// --- Method/field parsing ---

func parseGoInterfaceMethods(body string) []MethodSig {
	lines := strings.Split(body, "\n")
	var methods []MethodSig
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "//") || strings.HasPrefix(line, "/*") {
			continue
		}
		// Match: MethodName(params) (returns)
		// or: MethodName(params)
		// Handle generic methods with type parameters
		line = stripGoTypeParams(line)
		re := regexp.MustCompile(`^(\w+)\s*\(([^)]*)\)\s*(.*?)$`)
		m := re.FindStringSubmatch(line)
		if len(m) >= 2 {
			name := m[1]
			params := strings.TrimSpace(m[2])
			returns := ""
			if len(m) >= 4 {
				returns = strings.TrimSpace(m[3])
				returns = strings.TrimPrefix(returns, "(")
				returns = strings.TrimSuffix(returns, ")")
				returns = strings.TrimSpace(returns)
			}
			if name != "" && !isGoKeyword(name) {
				methods = append(methods, MethodSig{
					Name:    name,
					Params:  params,
					Returns: returns,
				})
			}
		}
	}
	return methods
}

func parseGoStructFields(body string) []FieldSig {
	lines := strings.Split(body, "\n")
	var fields []FieldSig
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		// Match: Name Type `tag`
		// or: Name, Name2 Type
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			// Skip if it looks like an embedded type (single word, no explicit name)
			name := parts[0]
			typ := parts[1]
			if strings.HasPrefix(name, "//") {
				continue
			}
			// Embedded interface: just the type name
			if len(parts) == 1 || (len(parts) >= 2 && !isGoKeyword(name) && looksLikeTypeName(name)) {
				fields = append(fields, FieldSig{Name: name, Type: typ})
			}
		}
	}
	return fields
}

func parseTSInterfaceMethods(body string) []MethodSig {
	lines := strings.Split(body, "\n")
	var methods []MethodSig
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		// Match: methodName(params): ReturnType;
		re := regexp.MustCompile(`^(\w+)\s*\(([^)]*)\)\s*:\s*(.+?);?$`)
		m := re.FindStringSubmatch(line)
		if len(m) >= 4 {
			methods = append(methods, MethodSig{
				Name:    m[1],
				Params:  strings.TrimSpace(m[2]),
				Returns: strings.TrimSpace(strings.TrimSuffix(m[3], ";")),
			})
		}
	}
	return methods
}

// --- Helpers ---

func extractGoPackage(source string) string {
	re := regexp.MustCompile(`(?m)^package\s+(\w+)`)
	m := re.FindStringSubmatch(source)
	if len(m) >= 2 {
		return m[1]
	}
	return ""
}

func stripGoTypeParams(line string) string {
	// Remove generic type parameters: Foo[T any] -> Foo
	re := regexp.MustCompile(`^(\w+)\s*\[.*?\]`)
	if m := re.FindStringSubmatch(line); len(m) >= 2 {
		return m[1] + line[len(m[0]):]
	}
	return line
}

func isGoKeyword(s string) bool {
	switch s {
	case "break", "case", "chan", "const", "continue", "default", "defer",
		"else", "fallthrough", "for", "func", "go", "goto", "if",
		"import", "interface", "map", "package", "range", "return",
		"select", "struct", "switch", "type", "var":
		return true
	}
	return false
}

func looksLikeTypeName(s string) bool {
	if len(s) == 0 {
		return false
	}
	return s[0] >= 'A' && s[0] <= 'Z'
}

func pathExt(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '.' {
			return path[i:]
		}
	}
	return ""
}

// --- P3-3: Project Layout Extraction ---

// extractLayoutConventions walks source file paths and infers where
// components of each type should be placed (package, directory, naming).
func extractLayoutConventions(sourceFiles []string) []LayoutConvention {
	dirPackages := map[string]string{}
	dirExamples := map[string][]string{}
	for _, path := range sourceFiles {
		dir := filepath.Dir(path)
		base := filepath.Base(path)
		if strings.HasSuffix(base, "_test.go") {
			continue
		}
		dirExamples[dir] = append(dirExamples[dir], base)
		if _, ok := dirPackages[dir]; !ok {
			content, err := os.ReadFile(path)
			if err == nil {
				pkg := extractGoPackage(string(content))
				if pkg != "" {
					dirPackages[dir] = pkg
				}
			}
		}
	}
	var conventions []LayoutConvention
	seen := map[string]bool{}
	for dir, pkg := range dirPackages {
		category := dirToCategory(dir)
		if seen[category] { continue }
		seen[category] = true
		convention := LayoutConvention{Category: category, Dir: dir, Package: pkg}
		if examples, ok := dirExamples[dir]; ok && len(examples) > 0 {
			convention.FilePattern = inferFilePattern(examples)
		}
		conventions = append(conventions, convention)
	}
	return conventions
}

func dirToCategory(dir string) string {
	base := filepath.Base(dir)
	switch base {
	case "plugins", "plugin": return "plugin"
	case "handler", "handlers": return "handler"
	case "model", "models": return "model"
	case "template", "templates": return "template"
	case "output": return "output"
	case "manual": return "manual"
	case "auto": return "auto"
	case "preset", "presets": return "preset"
	case "legacy": return "legacy"
	default: return base
	}
}

func inferFilePattern(files []string) string {
	if len(files) == 0 { return "*.go" }
	for _, f := range files {
		if strings.Contains(f, string(filepath.Separator)) {
			return "subdir/name.go (one package per subdirectory)"
		}
	}
	for _, f := range files {
		base := strings.TrimSuffix(f, ".go")
		if strings.Contains(base, "_") {
			return "snake_case.go (one file per component)"
		}
	}
	return "*.go"
}

func extractPkgFromDir(files []string) string {
	if len(files) == 0 { return "" }
	return filepath.Base(filepath.Dir(files[0]))
}

func (ctx *StructuredContext) addLayoutConvention(conv LayoutConvention) {
	for _, existing := range ctx.LayoutConventions {
		if existing.Category == conv.Category { return }
	}
	ctx.LayoutConventions = append(ctx.LayoutConventions, conv)
}

func formatStructuredContextForPrompt(ctx *StructuredContext) string {
	if ctx == nil || ctx.Empty() { return "" }
	var b strings.Builder
	b.WriteString("=== STRUCTURED PROJECT CONTEXT (exact signatures - match these) ===\n\n")
	if len(ctx.LayoutConventions) > 0 {
		b.WriteString("-- PROJECT LAYOUT (where to place new files) --\n")
		for _, lc := range ctx.LayoutConventions {
			b.WriteString("- ")
			b.WriteString(lc.Category)
			b.WriteString(": dir=")
			b.WriteString(lc.Dir)
			b.WriteString(", package=")
			b.WriteString(lc.Package)
			if lc.FilePattern != "" {
				b.WriteString(", files=")
				b.WriteString(lc.FilePattern)
			}
			b.WriteString("\n")
		}
		b.WriteString("\nFILE PLACEMENT RULES (non-negotiable):\n")
		b.WriteString("1. Use the EXACT directory listed above - do NOT create subdirectories.\n")
		b.WriteString("2. Use the EXACT package name listed above.\n")
		b.WriteString("3. File names: use snake_case.go (e.g., row_counter.go).\n")
		b.WriteString("4. The FILE: header must use the full path from the layout above.\n")
		b.WriteString("Example: FILE: internal/plugins/row_counter.go\n")
		b.WriteString("WRONG: FILE: internal/plugins/rowcounter/counter.go (no subdirectories!)\n\n")
	}
	if len(ctx.InterfaceDecls) > 0 {
		b.WriteString("-- INTERFACES --\n")
		for _, iface := range ctx.InterfaceDecls {
			b.WriteString("// " + iface.File + " (package " + iface.Package + ")\n")
			b.WriteString("type " + iface.Name + " interface {\n")
			for _, m := range iface.Methods {
				b.WriteString("\t" + m.Name + "(" + m.Params + ")")
				if m.Returns != "" { b.WriteString(" (" + m.Returns + ")") }
				b.WriteString("\n")
			}
			b.WriteString("}\n\n")
		}
	}
	if len(ctx.StructDecls) > 0 {
		b.WriteString("-- KEY TYPES --\n")
		for _, s := range ctx.StructDecls {
			b.WriteString("// " + s.File + " (package " + s.Package + ")\n")
			b.WriteString("type " + s.Name + " struct {\n")
			for _, f := range s.Fields {
				b.WriteString("\t" + f.Name + " " + f.Type + "\n")
			}
			b.WriteString("}\n\n")
		}
	}
	if len(ctx.FuncDecls) > 0 {
		b.WriteString("-- PUBLIC FUNCTIONS --\n")
		for _, f := range ctx.FuncDecls {
			b.WriteString("// " + f.File + "\nfunc " + f.Name + "(" + f.Params + ")")
			if f.Returns != "" { b.WriteString(" " + f.Returns) }
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	if len(ctx.RegistryPatterns) > 0 {
		b.WriteString("-- REGISTRATION PATTERNS --\n")
		for _, p := range ctx.RegistryPatterns {
			b.WriteString("// " + p + "\n")
		}
		b.WriteString("\n")
	}
	b.WriteString("=== END STRUCTURED CONTEXT ===\n\n")
	return b.String()
}

func formatStructuredContextForSkill(ctx *StructuredContext) string {
	if ctx == nil || ctx.Empty() { return "" }
	var b strings.Builder
	b.WriteString("### Interfaces\n\n")
	for _, iface := range ctx.InterfaceDecls {
		b.WriteString("- **" + iface.Name + "** (`" + iface.Package + "`, " + iface.File + ")\n")
		for _, m := range iface.Methods {
			b.WriteString("  - `" + m.Name + "(" + m.Params + ")")
			if m.Returns != "" { b.WriteString(" (" + m.Returns + ")") }
			b.WriteString("`\n")
		}
		b.WriteString("\n")
	}
	if len(ctx.StructDecls) > 0 {
		b.WriteString("### Key Types\n\n")
		for _, s := range ctx.StructDecls {
			b.WriteString("- **" + s.Name + "** (`" + s.Package + "`, " + s.File + ")\n")
		}
		b.WriteString("\n")
	}
	if len(ctx.RegistryPatterns) > 0 {
		b.WriteString("### Registration Patterns\n\n")
		for _, p := range ctx.RegistryPatterns {
			b.WriteString("- `" + p + "`\n")
		}
		b.WriteString("\n")
	}
	return b.String()
}
