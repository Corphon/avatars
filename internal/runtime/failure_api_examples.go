package runtime

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

// detectMismatchedUsageExamples compares synthesizer usage snippets against
// on-disk exports (F93). Cross-language: Go AST for signatures; Python/JS
// arity heuristics for the same "first arg is a config object" mistake.
func detectMismatchedUsageExamples(wd, synthText string) []string {
	synthText = strings.TrimSpace(synthText)
	if synthText == "" || strings.TrimSpace(wd) == "" {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	add := func(line string) {
		line = strings.TrimSpace(line)
		if line == "" || seen[line] {
			return
		}
		seen[line] = true
		out = append(out, line)
	}
	for _, line := range detectMismatchedGoUsage(wd, synthText) {
		add(line)
	}
	for _, line := range detectMismatchedDefUsage(wd, synthText, ".py", pythonDefRe) {
		add(line)
	}
	for _, line := range detectMismatchedDefUsage(wd, synthText, ".js", jsExportFnRe) {
		add(line)
	}
	for _, line := range detectMismatchedDefUsage(wd, synthText, ".ts", jsExportFnRe) {
		add(line)
	}
	for _, line := range detectUnevidencedClaimedSymbols(wd, synthText) {
		add(line)
	}
	if len(out) > 8 {
		out = out[:8]
	}
	return out
}

var (
	pythonDefRe    = regexp.MustCompile(`(?m)^def\s+([A-Za-z_][\w]*)\s*\(([^)]*)\)\s*:`)
	jsExportFnRe   = regexp.MustCompile(`(?m)(?:export\s+)?(?:async\s+)?function\s+([A-Za-z_][\w]*)\s*\(([^)]*)\)`)
	compositeArgRe = regexp.MustCompile(`^&?([A-Za-z_][\w.]*)\s*\{`)
)

type exportedGoFunc struct {
	Name       string
	ParamTypes []string
	Sig        string
	File       string
}

func formatAuthoritativeExportSignatures(wd string) string {
	wd = strings.TrimSpace(wd)
	if wd == "" {
		wd = "."
	}
	var lines []string
	for _, exp := range listExportedGoFuncs(wd) {
		lines = append(lines, exp.File+": "+exp.Sig)
	}
	if len(lines) > 24 {
		lines = lines[:24]
	}
	return strings.Join(lines, "\n")
}

func listExportedGoFuncs(wd string) []exportedGoFunc {
	var exports []exportedGoFunc
	_ = filepath.WalkDir(wd, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".avatars", "vendor", "node_modules", "testdata":
				if path != wd {
					return filepath.SkipDir
				}
			}
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			return nil
		}
		rel := path
		if r, err := filepath.Rel(wd, path); err == nil {
			rel = filepath.ToSlash(r)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || fn.Name == nil || !fn.Name.IsExported() {
				return true
			}
			if fn.Recv != nil {
				return true
			}
			exp := exportedGoFunc{
				Name: fn.Name.Name,
				File: rel,
				Sig:  formatGoFuncSig(fn),
			}
			if fn.Type != nil && fn.Type.Params != nil {
				for _, field := range fn.Type.Params.List {
					typ := exprString(field.Type)
					nNames := len(field.Names)
					if nNames == 0 {
						nNames = 1
					}
					for i := 0; i < nNames; i++ {
						exp.ParamTypes = append(exp.ParamTypes, typ)
					}
				}
			}
			if len(exp.ParamTypes) == 0 {
				return true
			}
			exports = append(exports, exp)
			return true
		})
		return nil
	})
	return exports
}

func detectMismatchedGoUsage(wd, synth string) []string {
	exports := listExportedGoFuncs(wd)

	var out []string
	for _, exp := range exports {
		arg := firstCallArgForName(synth, exp.Name)
		if arg == "" {
			continue
		}
		if !usageArgMismatchesFirstParam(arg, exp.ParamTypes) {
			continue
		}
		out = append(out, fmt.Sprintf("`%s` exports `%s`; model example used `%s(%s)`",
			exp.File, exp.Sig, exp.Name, compactArgPreview(arg)))
	}
	return out
}

func usageArgMismatchesFirstParam(arg string, paramTypes []string) bool {
	if len(paramTypes) == 0 {
		return false
	}
	first := strings.TrimSpace(paramTypes[0])
	m := compositeArgRe.FindStringSubmatch(strings.TrimSpace(arg))
	if m == nil {
		return false
	}
	used := strings.TrimPrefix(m[1], "*")
	if idx := strings.LastIndex(used, "."); idx >= 0 {
		used = used[idx+1:]
	}
	firstBare := strings.TrimPrefix(first, "*")
	if idx := strings.LastIndex(firstBare, "."); idx >= 0 {
		firstBare = firstBare[idx+1:]
	}
	if used != "" && strings.EqualFold(used, firstBare) {
		return false
	}
	if isScalarGoType(first) {
		return true
	}
	for _, later := range paramTypes[1:] {
		laterBare := strings.TrimPrefix(later, "*")
		if idx := strings.LastIndex(laterBare, "."); idx >= 0 {
			laterBare = laterBare[idx+1:]
		}
		if used != "" && strings.EqualFold(used, laterBare) {
			return true
		}
	}
	return false
}

func isScalarGoType(t string) bool {
	t = strings.TrimPrefix(strings.TrimSpace(t), "*")
	switch t {
	case "string", "int", "int8", "int16", "int32", "int64",
		"uint", "uint8", "uint16", "uint32", "uint64", "uintptr",
		"float32", "float64", "bool", "byte", "rune", "error",
		"context.Context":
		return true
	}
	return false
}

func firstCallArgForName(synth, name string) string {
	needle := name + "("
	start := 0
	for {
		idx := strings.Index(synth[start:], needle)
		if idx < 0 {
			return ""
		}
		idx += start
		if idx > 0 {
			prev, _ := utf8.DecodeLastRuneInString(synth[:idx])
			if (prev >= 'A' && prev <= 'Z') || (prev >= 'a' && prev <= 'z') ||
				(prev >= '0' && prev <= '9') || prev == '_' {
				start = idx + len(needle)
				continue
			}
		}
		return firstCallArg(synth[idx+len(needle):])
	}
}

func firstCallArg(afterOpenParen string) string {
	depth, brace := 0, 0
	for i, r := range afterOpenParen {
		switch r {
		case '(':
			depth++
		case ')':
			if depth == 0 && brace == 0 {
				return strings.TrimSpace(afterOpenParen[:i])
			}
			if depth > 0 {
				depth--
			}
		case '{':
			brace++
		case '}':
			if brace > 0 {
				brace--
			}
		case ',':
			if depth == 0 && brace == 0 {
				return strings.TrimSpace(afterOpenParen[:i])
			}
		}
	}
	return strings.TrimSpace(afterOpenParen)
}

func compactArgPreview(arg string) string {
	arg = strings.Join(strings.Fields(arg), " ")
	if strings.Contains(arg, "{") {
		if i := strings.Index(arg, "{"); i >= 0 {
			return strings.TrimSpace(arg[:i]) + "{...}"
		}
	}
	if len([]rune(arg)) > 48 {
		return string([]rune(arg)[:48]) + "…"
	}
	return arg
}

func formatGoFuncSig(fn *ast.FuncDecl) string {
	var b strings.Builder
	b.WriteString("func ")
	b.WriteString(fn.Name.Name)
	b.WriteByte('(')
	if fn.Type != nil && fn.Type.Params != nil {
		for i, field := range fn.Type.Params.List {
			if i > 0 {
				b.WriteString(", ")
			}
			names := make([]string, 0, len(field.Names))
			for _, n := range field.Names {
				names = append(names, n.Name)
			}
			if len(names) > 0 {
				b.WriteString(strings.Join(names, ", "))
				b.WriteByte(' ')
			}
			b.WriteString(exprString(field.Type))
		}
	}
	b.WriteByte(')')
	if fn.Type != nil && fn.Type.Results != nil && len(fn.Type.Results.List) > 0 {
		results := fn.Type.Results.List
		b.WriteByte(' ')
		needParen := len(results) > 1 || (len(results) == 1 && len(results[0].Names) > 0)
		if needParen {
			b.WriteByte('(')
		}
		for i, field := range results {
			if i > 0 {
				b.WriteString(", ")
			}
			names := make([]string, 0, len(field.Names))
			for _, n := range field.Names {
				names = append(names, n.Name)
			}
			if len(names) > 0 {
				b.WriteString(strings.Join(names, ", "))
				b.WriteByte(' ')
			}
			b.WriteString(exprString(field.Type))
		}
		if needParen {
			b.WriteByte(')')
		}
	}
	return b.String()
}

func exprString(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return "*" + exprString(t.X)
	case *ast.SelectorExpr:
		return exprString(t.X) + "." + t.Sel.Name
	case *ast.ArrayType:
		return "[]" + exprString(t.Elt)
	case *ast.Ellipsis:
		return "..." + exprString(t.Elt)
	case *ast.MapType:
		return "map[" + exprString(t.Key) + "]" + exprString(t.Value)
	case *ast.InterfaceType:
		return "interface{}"
	default:
		return "any"
	}
}

func detectMismatchedDefUsage(wd, synth, ext string, defRe *regexp.Regexp) []string {
	var out []string
	_ = filepath.WalkDir(wd, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".avatars", "vendor", "node_modules", "testdata":
				if path != wd {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), ext) {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		rel := path
		if r, err := filepath.Rel(wd, path); err == nil {
			rel = filepath.ToSlash(r)
		}
		for _, m := range defRe.FindAllStringSubmatch(string(src), -1) {
			name := m[1]
			params := splitDefParams(m[2])
			if len(params) < 2 {
				continue
			}
			if isScalarishDefParam(params[0]) && compositeCall(synth, name) {
				out = append(out, fmt.Sprintf("`%s` defines `%s(%s)`; model example called it with an object literal first",
					rel, name, strings.Join(params, ", ")))
			}
		}
		return nil
	})
	return out
}

func splitDefParams(raw string) []string {
	parts := strings.Split(raw, ",")
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" || p == "self" || p == "cls" {
			continue
		}
		p = strings.TrimPrefix(p, "...")
		p = strings.TrimPrefix(p, "*")
		p = strings.TrimPrefix(p, "*")
		if eq := strings.Index(p, "="); eq >= 0 {
			p = strings.TrimSpace(p[:eq])
		}
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func isScalarishDefParam(p string) bool {
	p = strings.ToLower(strings.TrimSpace(p))
	if p == "" || strings.Contains(p, "config") || strings.Contains(p, "opts") ||
		strings.Contains(p, "options") || strings.Contains(p, "dict") ||
		strings.Contains(p, "kwargs") {
		return false
	}
	return true
}

func compositeCall(synth, name string) bool {
	arg := firstCallArgForName(synth, name)
	if arg == "" {
		return false
	}
	trimmed := strings.TrimSpace(arg)
	return strings.HasPrefix(trimmed, "{") || compositeArgRe.MatchString(trimmed)
}

var claimedExportedCallRe = regexp.MustCompile(`(?:\b|\.|/)([A-Z][A-Za-z0-9]{2,})(?:\s*\(|\s*/)`)
var claimedSlashIdentRe = regexp.MustCompile(`/([A-Z][A-Za-z0-9]{2,})\b`)
var claimedDotIdentRe = regexp.MustCompile(`\.([A-Z][A-Za-z0-9]{2,})\b`)

var unevidencedAPISkip = map[string]bool{
	"New": true, "Get": true, "Set": true, "Len": true, "Cap": true, "Err": true,
	"HTTP": true, "JSON": true, "URL": true, "URI": true, "UTF": true, "ASCII": true,
	"Error": true, "String": true, "Close": true, "Context": true, "Config": true,
	"Option": true, "Result": true, "Type": true, "Int": true, "Bool": true,
	"True": true, "False": true, "None": true, "Null": true, "Main": true,
	"Test": true, "Init": true, "Run": true, "Stop": true, "Start": true,
	"Read": true, "Write": true, "Open": true, "Wait": true, "Lock": true,
	"Unlock": true, "Size": true, "Name": true, "Time": true, "Duration": true,
	"Queue": true, "Item": true, "Clock": true, "Worker": true,
}

// detectUnevidencedClaimedSymbols flags exported-looking APIs the model named
// that are not on disk (F110). Cross-lang: Go exported methods/funcs, Python
// def, JS/TS function. Suffix-only note for the synthesizer — not a prefix.
func detectUnevidencedClaimedSymbols(wd, synth string) []string {
	disk := collectOnDiskAPINames(wd)
	if len(disk) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	addClaim := func(m []string) {
		if len(m) < 2 {
			return
		}
		name := m[1]
		if unevidencedAPISkip[name] || seen[name] {
			return
		}
		if disk[name] || disk[strings.ToLower(name)] {
			return
		}
		seen[name] = true
		out = append(out, fmt.Sprintf("`%s` is not an on-disk export — do not document or call it (F110)", name))
	}
	for _, m := range claimedExportedCallRe.FindAllStringSubmatch(synth, -1) {
		addClaim(m)
	}
	for _, m := range claimedSlashIdentRe.FindAllStringSubmatch(synth, -1) {
		addClaim(m)
	}
	for _, m := range claimedDotIdentRe.FindAllStringSubmatch(synth, -1) {
		addClaim(m)
	}
	if len(out) > 8 {
		out = out[:8]
	}
	return out
}

func collectOnDiskAPINames(wd string) map[string]bool {
	names := map[string]bool{}
	_ = filepath.WalkDir(wd, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".avatars", "vendor", "node_modules", "testdata":
				if path != wd {
					return filepath.SkipDir
				}
			}
			return nil
		}
		name := d.Name()
		src, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		switch {
		case strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go"):
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, path, src, 0)
			if err != nil {
				return nil
			}
			ast.Inspect(file, func(n ast.Node) bool {
				fn, ok := n.(*ast.FuncDecl)
				if !ok || fn.Name == nil || !fn.Name.IsExported() {
					return true
				}
				names[fn.Name.Name] = true
				return true
			})
		case strings.HasSuffix(name, ".py"):
			for _, m := range pythonDefRe.FindAllStringSubmatch(string(src), -1) {
				if len(m) >= 2 {
					names[m[1]] = true
					names[strings.ToLower(m[1])] = true
				}
			}
		case strings.HasSuffix(name, ".js"), strings.HasSuffix(name, ".ts"),
			strings.HasSuffix(name, ".mjs"), strings.HasSuffix(name, ".cjs"):
			for _, m := range jsExportFnRe.FindAllStringSubmatch(string(src), -1) {
				if len(m) >= 2 {
					names[m[1]] = true
					names[strings.ToLower(m[1])] = true
				}
			}
		}
		return nil
	})
	return names
}
