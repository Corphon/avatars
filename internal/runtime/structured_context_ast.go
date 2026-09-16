package runtime

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
)

// === go/parser-based Structured Context Extraction ===
//
// The regex-based extraction in structured_context.go works for simple cases
// but silently drops information for:
//   - Embedded interfaces with lowercase package names (io.Reader, http.Handler)
//   - Function-typed fields (the type gets truncated to just "func(ctx")
//   - Generic types with brackets
//   - Multi-line struct tags
//
// This file provides a go/parser + go/ast based extraction that is
// deterministic and complete. It serves as the PRIMARY extraction for .go
// files, with regex as fallback only when the file fails to parse.
//
// Design principle (from Claude Code): read-before-write. Before we tell
// Builder what types exist, we must correctly read them ourselves.

// extractViaAST extracts struct, interface, and function declarations from
// a parsed Go file. This is the authoritative source for Go type information.
func extractViaAST(f *ast.File, fset *token.FileSet) *StructuredContext {
	ctx := &StructuredContext{}
	pkg := f.Name.Name

	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.GenDecl:
			if d.Tok != token.TYPE {
				continue
			}
			for _, spec := range d.Specs {
				typeSpec, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				name := typeSpec.Name.Name
				if !ast.IsExported(name) {
					continue
				}
				ctx.extractTypeSpecViaAST(typeSpec, name, pkg, fset)
			}
		case *ast.FuncDecl:
			if !ast.IsExported(d.Name.Name) {
				continue
			}
			// Skip methods (they have a receiver).
			if d.Recv != nil {
				continue
			}
			fd := extractFuncDeclViaAST(d)
			fd.Package = pkg
			fd.File = fset.Position(d.Pos()).Filename
			ctx.FuncDecls = append(ctx.FuncDecls, fd)
		}
	}
	return ctx
}

// extractTypeSpecViaAST handles a single type declaration: struct or interface.
func (ctx *StructuredContext) extractTypeSpecViaAST(spec *ast.TypeSpec, name, pkg string, fset *token.FileSet) {
	filePath := fset.Position(spec.Pos()).Filename

	switch t := spec.Type.(type) {
	case *ast.StructType:
		sd := StructDecl{
			Name:    name,
			Package: pkg,
			File:    filePath,
			Fields:  extractStructFieldsViaAST(t),
		}
		ctx.StructDecls = append(ctx.StructDecls, sd)

	case *ast.InterfaceType:
		id := InterfaceDecl{
			Name:    name,
			Package: pkg,
			File:    filePath,
			Methods: extractInterfaceMethodsViaAST(t),
		}
		ctx.InterfaceDecls = append(ctx.InterfaceDecls, id)
	}
}

// extractStructFieldsViaAST extracts all fields from a struct type,
// including embedded types and fields of any complexity.
func extractStructFieldsViaAST(st *ast.StructType) []FieldSig {
	var fields []FieldSig
	if st.Fields == nil {
		return fields
	}
	for _, f := range st.Fields.List {
		typeStr := typeExprToString(f.Type)

		if len(f.Names) == 0 {
			// Embedded field (no explicit name) — use type as name.
			// Only include exported embedded types.
			if looksLikeExported(typeStr) {
				fields = append(fields, FieldSig{
					Name: typeStr,
					Type: typeStr,
				})
			}
		} else {
			// Named field(s). A single field line can declare multiple names
			// sharing the same type (e.g., "x, y int").
			for _, ident := range f.Names {
				if ast.IsExported(ident.Name) {
					fields = append(fields, FieldSig{
						Name: ident.Name,
						Type: typeStr,
					})
				}
			}
		}
	}
	return fields
}

// extractInterfaceMethodsViaAST extracts method signatures from an interface.
func extractInterfaceMethodsViaAST(it *ast.InterfaceType) []MethodSig {
	var methods []MethodSig
	if it.Methods == nil {
		return methods
	}
	for _, m := range it.Methods.List {
		if len(m.Names) == 0 {
			// Embedded interface — skip for method listing.
			continue
		}
		for _, ident := range m.Names {
			ft, ok := m.Type.(*ast.FuncType)
			if !ok {
				continue
			}
			methods = append(methods, MethodSig{
				Name:    ident.Name,
				Params:  fieldListToString(ft.Params),
				Returns: fieldListToString(ft.Results),
			})
		}
	}
	return methods
}

// extractFuncDeclViaAST extracts a standalone function declaration.
func extractFuncDeclViaAST(fd *ast.FuncDecl) FuncDecl {
	return FuncDecl{
		Name:    fd.Name.Name,
		Params:  fieldListToString(fd.Type.Params),
		Returns: fieldListToString(fd.Type.Results),
	}
}

// === Type expression formatting ===

// typeExprToString converts an AST type expression to its Go source string.
// Handles: identifiers, selectors (pkg.Type), pointers, arrays, slices, maps,
// functions, channels, interfaces, generics (IndexExpr, IndexListExpr).
func typeExprToString(expr ast.Expr) string {
	if expr == nil {
		return ""
	}
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name

	case *ast.SelectorExpr:
		return typeExprToString(t.X) + "." + t.Sel.Name

	case *ast.StarExpr:
		return "*" + typeExprToString(t.X)

	case *ast.ArrayType:
		if t.Len == nil {
			return "[]" + typeExprToString(t.Elt)
		}
		return fmt.Sprintf("[%s]%s", typeExprToString(t.Len), typeExprToString(t.Elt))

	case *ast.MapType:
		return fmt.Sprintf("map[%s]%s", typeExprToString(t.Key), typeExprToString(t.Value))

	case *ast.FuncType:
		return funcTypeToString(t)

	case *ast.ChanType:
		switch t.Dir {
		case ast.SEND:
			return "chan<- " + typeExprToString(t.Value)
		case ast.RECV:
			return "<-chan " + typeExprToString(t.Value)
		default:
			return "chan " + typeExprToString(t.Value)
		}

	case *ast.InterfaceType:
		if t.Methods == nil || len(t.Methods.List) == 0 {
			return "interface{}"
		}
		return "interface{...}"

	case *ast.StructType:
		return "struct{...}"

	case *ast.IndexExpr:
		// Single type parameter: Foo[T]
		return fmt.Sprintf("%s[%s]", typeExprToString(t.X), typeExprToString(t.Index))

	case *ast.IndexListExpr:
		// Multiple type parameters: Foo[A, B]
		indices := make([]string, len(t.Indices))
		for i, idx := range t.Indices {
			indices[i] = typeExprToString(idx)
		}
		return fmt.Sprintf("%s[%s]", typeExprToString(t.X), strings.Join(indices, ", "))

	case *ast.BasicLit:
		return t.Value

	case *ast.Ellipsis:
		return "..." + typeExprToString(t.Elt)

	case *ast.ParenExpr:
		return "(" + typeExprToString(t.X) + ")"

	default:
		// Fallback: use a best-effort representation.
		return fmt.Sprintf("<%T>", expr)
	}
}

// funcTypeToString formats a function type including parameters and returns.
func funcTypeToString(ft *ast.FuncType) string {
	var b strings.Builder
	b.WriteString("func(")
	b.WriteString(fieldListToString(ft.Params))
	b.WriteString(")")
	if ft.Results != nil && len(ft.Results.List) > 0 {
		results := fieldListToString(ft.Results)
		if ft.Results.NumFields() > 1 || len(ft.Results.List) > 0 {
			// Check if we need parens: multiple results or named returns.
			needParens := ft.Results.NumFields() > 1
			if !needParens && len(ft.Results.List) == 1 && len(ft.Results.List[0].Names) > 0 {
				needParens = true
			}
			if needParens {
				b.WriteString(" (")
				b.WriteString(results)
				b.WriteString(")")
			} else {
				b.WriteString(" ")
				b.WriteString(results)
			}
		}
	}
	return b.String()
}

// fieldListToString formats a parameter or result list.
func fieldListToString(fl *ast.FieldList) string {
	if fl == nil || len(fl.List) == 0 {
		return ""
	}
	parts := make([]string, 0, len(fl.List))
	for _, f := range fl.List {
		typeStr := typeExprToString(f.Type)
		if len(f.Names) == 0 {
			parts = append(parts, typeStr)
		} else {
			for _, n := range f.Names {
				if n.Name == "" || n.Name == "_" {
					parts = append(parts, typeStr)
				} else {
					parts = append(parts, n.Name+" "+typeStr)
				}
			}
		}
	}
	return strings.Join(parts, ", ")
}

// looksLikeExported checks if a type string (like "io.Reader" or "Reader")
// refers to an exported identifier. For simple names, checks first char.
// For qualified names like "pkg.Type", checks the last segment.
func looksLikeExported(typeStr string) bool {
	// For qualified names, extract the last segment.
	if idx := strings.LastIndex(typeStr, "."); idx >= 0 {
		typeStr = typeStr[idx+1:]
	}
	if len(typeStr) == 0 {
		return false
	}
	// Strip pointer/receiver indicators.
	typeStr = strings.TrimPrefix(typeStr, "*")
	if len(typeStr) == 0 {
		return false
	}
	return typeStr[0] >= 'A' && typeStr[0] <= 'Z'
}

// === Validation layer: compare regex vs AST results ===

// validateStructuredContext compares regex-based extraction with AST-based
// extraction for a single Go file. If AST found MORE type declarations than
// regex, it means regex silently dropped information — return the AST result.
// Otherwise return the regex result (which may include heuristic detections
// like registry patterns that AST doesn't capture).
//
// Returns: (preferred result, warning message if regex missed declarations).
func validateStructuredContext(regexCtx *StructuredContext, astCtx *StructuredContext, filePath string) (*StructuredContext, string) {
	if astCtx == nil || astCtx.Empty() {
		return regexCtx, ""
	}

	var warnings []string

	// Compare struct declarations.
	if len(astCtx.StructDecls) > len(regexCtx.StructDecls) {
		missing := findMissingStructs(astCtx.StructDecls, regexCtx.StructDecls)
		warnings = append(warnings, fmt.Sprintf(
			"%s: struct: AST=%d regex=%d, missed: %s",
			filePath, len(astCtx.StructDecls), len(regexCtx.StructDecls), strings.Join(missing, ", ")))
	}
	// Compare interface declarations.
	if len(astCtx.InterfaceDecls) > len(regexCtx.InterfaceDecls) {
		missing := findMissingInterfaces(astCtx.InterfaceDecls, regexCtx.InterfaceDecls)
		warnings = append(warnings, fmt.Sprintf(
			"%s: interface: AST=%d regex=%d, missed: %s",
			filePath, len(astCtx.InterfaceDecls), len(regexCtx.InterfaceDecls), strings.Join(missing, ", ")))
	}
	// Compare function declarations.
	if len(astCtx.FuncDecls) > len(regexCtx.FuncDecls) {
		missing := findMissingFuncs(astCtx.FuncDecls, regexCtx.FuncDecls)
		warnings = append(warnings, fmt.Sprintf(
			"%s: func: AST=%d regex=%d, missed: %s",
			filePath, len(astCtx.FuncDecls), len(regexCtx.FuncDecls), strings.Join(missing, ", ")))
	}

	// Compare field counts within matched structs.
	for _, astSD := range astCtx.StructDecls {
		for i, regexSD := range regexCtx.StructDecls {
			if astSD.Name == regexSD.Name && astSD.Package == regexSD.Package {
				if len(astSD.Fields) > len(regexSD.Fields) {
					missing := findMissingFields(astSD.Fields, regexSD.Fields)
					warnings = append(warnings, fmt.Sprintf(
						"%s: %s.%s fields: AST=%d regex=%d, missed: %s",
						filePath, astSD.Package, astSD.Name, len(astSD.Fields), len(regexSD.Fields),
						strings.Join(missing, ", ")))
				}
				// Always use AST field list if it has more fields.
				if len(astSD.Fields) > len(regexSD.Fields) {
					regexCtx.StructDecls[i].Fields = astSD.Fields
				}
				break
			}
		}
	}

	// If AST found more declarations, emit warning and merge.
	if len(warnings) > 0 {
		merged := mergeASTIntoRegex(regexCtx, astCtx)
		return merged, strings.Join(warnings, "; ")
	}

	return regexCtx, ""
}

// mergeASTIntoRegex returns a new StructuredContext that has all declarations
// from regexCtx, with any additional declarations from astCtx appended.
// For structs that exist in both, the AST field list (which is more complete)
// replaces the regex one.
func mergeASTIntoRegex(regexCtx *StructuredContext, astCtx *StructuredContext) *StructuredContext {
	merged := &StructuredContext{
		Summary:           regexCtx.Summary,
		RegistryPatterns:  regexCtx.RegistryPatterns,
		LayoutConventions: regexCtx.LayoutConventions,
	}

	// Use AST structs as the base (more complete), add regex-only structs.
	astStructNames := make(map[string]bool)
	for _, sd := range astCtx.StructDecls {
		key := sd.Package + "." + sd.Name
		astStructNames[key] = true
		merged.StructDecls = append(merged.StructDecls, sd)
	}
	for _, sd := range regexCtx.StructDecls {
		key := sd.Package + "." + sd.Name
		if !astStructNames[key] {
			merged.StructDecls = append(merged.StructDecls, sd)
		}
	}

	// Same for interfaces.
	astIfaceNames := make(map[string]bool)
	for _, id := range astCtx.InterfaceDecls {
		key := id.Package + "." + id.Name
		astIfaceNames[key] = true
		merged.InterfaceDecls = append(merged.InterfaceDecls, id)
	}
	for _, id := range regexCtx.InterfaceDecls {
		key := id.Package + "." + id.Name
		if !astIfaceNames[key] {
			merged.InterfaceDecls = append(merged.InterfaceDecls, id)
		}
	}

	// Same for functions.
	astFuncNames := make(map[string]bool)
	for _, fd := range astCtx.FuncDecls {
		key := fd.Package + "." + fd.Name
		astFuncNames[key] = true
		merged.FuncDecls = append(merged.FuncDecls, fd)
	}
	for _, fd := range regexCtx.FuncDecls {
		key := fd.Package + "." + fd.Name
		if !astFuncNames[key] {
			merged.FuncDecls = append(merged.FuncDecls, fd)
		}
	}

	return merged
}

// === Diff helpers for warning messages ===

func findMissingStructs(astList, regexList []StructDecl) []string {
	have := make(map[string]bool)
	for _, sd := range regexList {
		have[sd.Package+"."+sd.Name] = true
	}
	var missing []string
	for _, sd := range astList {
		key := sd.Package + "." + sd.Name
		if !have[key] {
			missing = append(missing, key)
		}
	}
	return missing
}

func findMissingInterfaces(astList, regexList []InterfaceDecl) []string {
	have := make(map[string]bool)
	for _, id := range regexList {
		have[id.Package+"."+id.Name] = true
	}
	var missing []string
	for _, id := range astList {
		key := id.Package + "." + id.Name
		if !have[key] {
			missing = append(missing, key)
		}
	}
	return missing
}

func findMissingFuncs(astList, regexList []FuncDecl) []string {
	have := make(map[string]bool)
	for _, fd := range regexList {
		have[fd.Package+"."+fd.Name] = true
	}
	var missing []string
	for _, fd := range astList {
		key := fd.Package + "." + fd.Name
		if !have[key] {
			missing = append(missing, key)
		}
	}
	return missing
}

func findMissingFields(astFields, regexFields []FieldSig) []string {
	have := make(map[string]bool)
	for _, f := range regexFields {
		have[f.Name] = true
	}
	var missing []string
	for _, f := range astFields {
		if !have[f.Name] {
			missing = append(missing, f.Name+" "+f.Type)
		}
	}
	return missing
}

// === Entry point: AST-based extraction for a single Go file ===

// extractStructuredContextViaAST parses a single Go file and extracts
// structured context using go/parser + go/ast. Returns nil if the file
// cannot be parsed.
func extractStructuredContextViaAST(filePath string) *StructuredContext {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filePath, nil, parser.ParseComments)
	if err != nil {
		return nil
	}
	return extractViaAST(f, fset)
}
