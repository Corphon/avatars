package verification

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// InterfaceMethod describes one method signature extracted from a Go interface.
type InterfaceMethod struct {
	Name    string
	Params  string // e.g. "ctx context.Context, req CompletionRequest"
	Returns string // e.g. "(*CompletionResponse, error)" or ""
}

// InterfaceDecl describes a Go interface type found in source.
type InterfaceDecl struct {
	Name    string
	Package string
	File    string
	Methods []InterfaceMethod
}

// MissingMethodReport describes methods that a generated file fails to implement.
type MissingMethodReport struct {
	InterfaceFile string
	InterfaceName string
	Methods       []InterfaceMethod
}

// ExtractInterfaceMethods parses a Go source file and returns all interface
// declarations with their method signatures (P14-3a / P10-1).
// It handles standard Go interface syntax:
//
//	type Name interface {
//	    MethodName(params) (returns)
//	    ...
//	}
func ExtractInterfaceMethods(filePath string) ([]InterfaceDecl, error) {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	return extractInterfaces(string(content), filePath), nil
}

func extractInterfaces(source string, filePath string) []InterfaceDecl {
	var decls []InterfaceDecl

	// Find type XXX interface { blocks.
	// Simpler state-machine approach: find "type " then scan.
	lines := strings.Split(source, "\n")

	for i := 0; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		// Match: type Name interface {
		if !strings.HasPrefix(trimmed, "type ") {
			continue
		}
		rest := strings.TrimPrefix(trimmed, "type ")
		// Split at "interface"
		ifaceIdx := strings.Index(rest, " interface")
		if ifaceIdx < 0 {
			// Could be type Name interface{
			ifaceIdx = strings.Index(rest, " interface{")
		}
		if ifaceIdx < 0 {
			continue
		}

		name := strings.TrimSpace(rest[:ifaceIdx])
		if name == "" {
			continue
		}

		// Scan forward for methods until "}"
		var methods []InterfaceMethod
		j := i + 1
		for ; j < len(lines); j++ {
			line := strings.TrimSpace(lines[j])
			if line == "}" || strings.HasPrefix(line, "}") {
				break
			}
			if line == "" || strings.HasPrefix(line, "//") {
				continue
			}
			// Try to extract method signature: Name(params) (returns)
			m := parseInterfaceMethodLine(line)
			if m != nil {
				methods = append(methods, *m)
			}
		}

		if len(methods) > 0 {
			decls = append(decls, InterfaceDecl{
				Name:    name,
				File:    filePath,
				Methods: methods,
			})
		}
	}

	return decls
}

var methodLineRe = regexp.MustCompile(`^\s*([A-Za-z_]\w*)\s*\((.*)\)\s*(\(.*\))?\s*$`)

func parseInterfaceMethodLine(line string) *InterfaceMethod {
	// Remove trailing comment
	if idx := strings.Index(line, "//"); idx >= 0 {
		line = line[:idx]
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}

	matches := methodLineRe.FindStringSubmatch(line)
	if matches == nil {
		return nil
	}

	name := matches[1]
	params := strings.TrimSpace(matches[2])
	returns := ""
	if len(matches) > 3 {
		returns = strings.TrimSpace(matches[3])
	}

	return &InterfaceMethod{
		Name:    name,
		Params:  params,
		Returns: returns,
	}
}

// CheckInterfaceCompliance scans a generated Go file and checks whether it
// implements all methods from the given interface declarations.
// Returns the list of missing methods per interface (P14-3a / P10-2).
func CheckInterfaceCompliance(generatedFile string, interfaces []InterfaceDecl) []MissingMethodReport {
	content, err := os.ReadFile(generatedFile)
	if err != nil {
		return nil
	}
	source := string(content)

	var reports []MissingMethodReport
	for _, iface := range interfaces {
		var missing []InterfaceMethod
		for _, m := range iface.Methods {
			if !hasMethodImplementation(source, m) {
				missing = append(missing, m)
			}
		}
		if len(missing) > 0 {
			reports = append(reports, MissingMethodReport{
				InterfaceFile: iface.File,
				InterfaceName: iface.Name,
				Methods:       missing,
			})
		}
	}
	return reports
}

// hasMethodImplementation checks whether a Go source contains a method
// that matches the given interface method signature.
func hasMethodImplementation(source string, m InterfaceMethod) bool {
	// Search for method name in receiver method declarations: func (r *T) MethodName(
	lines := strings.Split(source, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "func (") {
			continue
		}
		// Extract method name: func (receiver) MethodName(
		rest := trimmed[len("func ("):]
		// Skip past receiver type: *Type) or Type)
		parenIdx := strings.Index(rest, ") ")
		if parenIdx < 0 {
			parenIdx = strings.Index(rest, ")\t")
		}
		if parenIdx < 0 {
			continue
		}
		rest = rest[parenIdx+2:] // skip ") " or ")\t"
		// Now rest should start with MethodName(
		methodNameEnd := strings.Index(rest, "(")
		if methodNameEnd < 0 {
			continue
		}
		methodName := strings.TrimSpace(rest[:methodNameEnd])
		if methodName == m.Name {
			return true
		}
	}
	return false
}

// InterfaceCheckForGeneratedFiles runs P14-3a compliance check on generated files.
// It searches for interface.go files in the working directory, extracts
// interface declarations, and checks each generated Go file for implementation.
func InterfaceCheckForGeneratedFiles(workingDir string, generatedFiles []string) ([]MissingMethodReport, error) {
	// Find interface files
	var interfaceFiles []string
	_ = filepath.WalkDir(workingDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		base := strings.ToLower(filepath.Base(path))
		if base == "interface.go" || strings.HasSuffix(base, "_interface.go") {
			interfaceFiles = append(interfaceFiles, path)
		}
		return nil
	})

	if len(interfaceFiles) == 0 {
		return nil, nil
	}

	// Extract all interface declarations
	var allInterfaces []InterfaceDecl
	for _, ifaceFile := range interfaceFiles {
		decls, err := ExtractInterfaceMethods(ifaceFile)
		if err != nil {
			continue
		}
		allInterfaces = append(allInterfaces, decls...)
	}

	if len(allInterfaces) == 0 {
		return nil, nil
	}

	// Check each generated file
	var allReports []MissingMethodReport
	for _, genFile := range generatedFiles {
		if !strings.HasSuffix(genFile, ".go") {
			continue
		}
		fullPath := genFile
		if !filepath.IsAbs(fullPath) {
			fullPath = filepath.Join(workingDir, fullPath)
		}
		reports := CheckInterfaceCompliance(fullPath, allInterfaces)
		allReports = append(allReports, reports...)
	}

	return allReports, nil
}

// FormatMissingMethodReport formats interface compliance failures for Builder feedback.
func FormatMissingMethodReport(reports []MissingMethodReport) string {
	if len(reports) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("INTERFACE COMPLIANCE FAILURES:\n")
	for _, report := range reports {
		b.WriteString(fmt.Sprintf("\nFile: %s\n", report.InterfaceFile))
		b.WriteString(fmt.Sprintf("Interface: %s\n", report.InterfaceName))
		b.WriteString("Missing methods:\n")
		for _, m := range report.Methods {
			sig := fmt.Sprintf("  %s(%s)", m.Name, m.Params)
			if m.Returns != "" {
				sig += fmt.Sprintf(" %s", m.Returns)
			}
			b.WriteString(sig + "\n")
		}
	}
	b.WriteString("\nACTION: Implement the missing methods listed above with EXACT matching signatures.\n")
	return b.String()
}
