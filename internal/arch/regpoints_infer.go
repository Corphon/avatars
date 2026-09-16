package arch

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// routeHit is one detected registration-like site in source.
type routeHit struct {
	Path    string
	Line    int
	Kind    string // route | middleware | command
	Example string
}

var (
	reGoHandleFunc = regexp.MustCompile(`(?i)(?:http\.HandleFunc|\.HandleFunc|\.Handle)\(\s*"([^"]+)"`)
	reGoMethod     = regexp.MustCompile(`(?i)\.(GET|POST|PUT|DELETE|PATCH|OPTIONS|HEAD)\(\s*"([^"]+)"`)
	reGoMux        = regexp.MustCompile(`(?i)(?:mux\.|chi\.|router\.)(?:Handle|HandleFunc|Route)\(`)
	reGoUse        = regexp.MustCompile(`(?i)\.Use\(`)
	reGoAddCmd     = regexp.MustCompile(`(?i)\.AddCommand\(`)

	rePyDecorator = regexp.MustCompile(`(?i)@(?:app|router)\.(get|post|put|delete|patch|route)\(\s*['"]([^'"]+)['"]`)
	rePyFastAPI   = regexp.MustCompile(`(?i)(?:APIRouter|include_router)\(`)

	reJSMethod = regexp.MustCompile(`(?i)(?:app|router)\.(get|post|put|delete|patch|use)\(\s*['"]([^'"]+)['"]`)
	reJSRoute  = regexp.MustCompile(`(?i)\.route\(\s*['"]([^'"]+)['"]`)

	reRsRoute = regexp.MustCompile(`(?i)(?:\.route\(|Router::new|axum::routing::)`)
)

// InferRegistrationPoints scans sources for route/middleware/command hooks.
// Language-agnostic heuristics — no LLM. Safe to run on every refresh.
func InferRegistrationPoints(root string, scan *ProjectScan) []RegistrationPoint {
	if scan == nil {
		return nil
	}
	var hits []routeHit
	for _, rel := range scan.FileTree {
		ext := strings.ToLower(filepath.Ext(rel))
		switch ext {
		case ".go", ".py", ".js", ".jsx", ".ts", ".tsx", ".mjs", ".rs":
		default:
			continue
		}
		// Skip obvious test noise for registration maps.
		base := filepath.Base(rel)
		if strings.HasSuffix(base, "_test.go") || strings.HasPrefix(base, "test_") ||
			strings.Contains(rel, "_test.") || strings.Contains(rel, "/testdata/") {
			continue
		}
		hits = append(hits, scanFileForRegHits(root, rel)...)
	}
	return groupRegHits(hits)
}

func scanFileForRegHits(root, rel string) []routeHit {
	f, err := os.Open(filepath.Join(root, rel))
	if err != nil {
		return nil
	}
	defer f.Close()

	slash := filepath.ToSlash(rel)
	var hits []routeHit
	sc := bufio.NewScanner(f)
	// Large generated files: cap lines scanned.
	const maxLines = 4000
	lineNo := 0
	for sc.Scan() {
		lineNo++
		if lineNo > maxLines {
			break
		}
		line := sc.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "#") {
			continue
		}

		if m := reGoHandleFunc.FindStringSubmatch(line); m != nil {
			hits = append(hits, routeHit{Path: slash, Line: lineNo, Kind: "route", Example: m[0]})
			continue
		}
		if m := reGoMethod.FindStringSubmatch(line); m != nil {
			hits = append(hits, routeHit{Path: slash, Line: lineNo, Kind: "route", Example: m[0]})
			continue
		}
		if reGoMux.MatchString(line) {
			hits = append(hits, routeHit{Path: slash, Line: lineNo, Kind: "route", Example: truncateEx(trimmed, 80)})
			continue
		}
		if reGoUse.MatchString(line) {
			hits = append(hits, routeHit{Path: slash, Line: lineNo, Kind: "middleware", Example: truncateEx(trimmed, 80)})
			continue
		}
		if reGoAddCmd.MatchString(line) {
			hits = append(hits, routeHit{Path: slash, Line: lineNo, Kind: "command", Example: truncateEx(trimmed, 80)})
			continue
		}
		if m := rePyDecorator.FindStringSubmatch(line); m != nil {
			hits = append(hits, routeHit{Path: slash, Line: lineNo, Kind: "route", Example: m[0]})
			continue
		}
		if rePyFastAPI.MatchString(line) {
			hits = append(hits, routeHit{Path: slash, Line: lineNo, Kind: "route", Example: truncateEx(trimmed, 80)})
			continue
		}
		if m := reJSMethod.FindStringSubmatch(line); m != nil {
			kind := "route"
			if strings.EqualFold(m[1], "use") {
				kind = "middleware"
			}
			hits = append(hits, routeHit{Path: slash, Line: lineNo, Kind: kind, Example: m[0]})
			continue
		}
		if m := reJSRoute.FindStringSubmatch(line); m != nil {
			hits = append(hits, routeHit{Path: slash, Line: lineNo, Kind: "route", Example: m[0]})
			continue
		}
		if reRsRoute.MatchString(line) {
			hits = append(hits, routeHit{Path: slash, Line: lineNo, Kind: "route", Example: truncateEx(trimmed, 80)})
		}
	}
	return hits
}

func groupRegHits(hits []routeHit) []RegistrationPoint {
	if len(hits) == 0 {
		return nil
	}
	byKind := map[string][]routeHit{}
	for _, h := range hits {
		byKind[h.Kind] = append(byKind[h.Kind], h)
	}
	order := []string{"route", "middleware", "command"}
	var out []RegistrationPoint
	for _, kind := range order {
		group := byKind[kind]
		if len(group) == 0 {
			continue
		}
		files := dedupeRegFiles(group)
		if len(files) == 0 {
			continue
		}
		rp := RegistrationPoint{
			ChangeType: "add-" + kind,
			Note:       heuristicNote(kind),
			Files:      files,
		}
		out = append(out, rp)
	}
	return out
}

func dedupeRegFiles(hits []routeHit) []RegFile {
	seen := map[string]bool{}
	var files []RegFile
	for _, h := range hits {
		if seen[h.Path] {
			continue
		}
		seen[h.Path] = true
		hint := "register new " + h.Kind + " here"
		switch h.Kind {
		case "route":
			hint = "add route handler registration"
		case "middleware":
			hint = "mount middleware in the chain"
		case "command":
			hint = "register CLI subcommand"
		}
		lr := ""
		if h.Line > 0 {
			lr = "L" + itoa(h.Line)
		}
		files = append(files, RegFile{
			Path:       h.Path,
			LineRange:  lr,
			ChangeHint: hint,
			Example:    h.Example,
		})
		if len(files) >= 12 {
			break
		}
	}
	return files
}

func heuristicNote(kind string) string {
	switch kind {
	case "route":
		return "When adding an HTTP/API route, update every file that registers or mounts handlers (inferred from source)."
	case "middleware":
		return "When adding middleware, update the chain registration sites listed below."
	case "command":
		return "When adding a CLI command, register it at the AddCommand / command-tree sites below."
	default:
		return "Coordinated registration sites inferred from source."
	}
}

func truncateEx(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [16]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// MergeRegPoints unions inferred points into existing ones by ChangeType.
// Existing (LLM/user) wins on same ChangeType when it already has files;
// otherwise inferred fills gaps.
func MergeRegPoints(existing, inferred []RegistrationPoint) []RegistrationPoint {
	if len(inferred) == 0 {
		return existing
	}
	if len(existing) == 0 {
		return inferred
	}
	idx := map[string]int{}
	out := append([]RegistrationPoint{}, existing...)
	for i, rp := range out {
		idx[rp.ChangeType] = i
	}
	for _, inf := range inferred {
		if i, ok := idx[inf.ChangeType]; ok {
			if len(out[i].Files) == 0 {
				out[i] = inf
			}
			continue
		}
		idx[inf.ChangeType] = len(out)
		out = append(out, inf)
	}
	return out
}
