package runtime

import (
	"path/filepath"
	"regexp"
	"strings"
)

// clipCommandOutput shortens compile/test command output for gate errors (P1).
// Prefer the tail and/or a window around failure markers — leading deprecation
// warnings must not hide the real FAILED/ERROR body (cross-language).
func clipCommandOutput(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 || len(s) <= max {
		return s
	}
	lower := strings.ToLower(s)
	markers := []string{
		"\nfailed ",
		"\nerror:",
		"\nerror ",
		"\ne   ",
		"=== failures ===",
		"short test summary",
		"cannot overwrite",
		"modulenotfounderror",
		"cannot find name",
		"ts2591",
		"ts2307",
		"panic:",
		"go: ",
		"error[e",
	}
	best := -1
	for _, m := range markers {
		if i := strings.LastIndex(lower, m); i > best {
			best = i
		}
	}
	if best >= 0 {
		start := best - max/5
		if start < 0 {
			start = 0
		}
		end := start + max
		if end > len(s) {
			start = len(s) - max
			if start < 0 {
				start = 0
			}
			end = len(s)
		}
		out := s[start:end]
		if start > 0 {
			out = "..." + out
		}
		if end < len(s) {
			out = out + "..."
		}
		return out
	}
	// Default: keep the tail (failures usually land last).
	return "..." + s[len(s)-max:]
}

var tomlKeyLineRe = regexp.MustCompile(`(?m)^\s*([A-Za-z0-9_.-]+)\s*=`)

// tomlDuplicateKeyReason reports duplicate keys within the same TOML table
// (P2: mid_py pyproject double filterwarnings → pytest cannot start).
// Heuristic — no full TOML parser dependency.
func tomlDuplicateKeyReason(path, content string) string {
	base := strings.ToLower(filepath.Base(path))
	if !strings.HasSuffix(base, ".toml") {
		return ""
	}
	section := ""
	seen := map[string]map[string]bool{} // section -> key -> true
	lines := strings.Split(content, "\n")
	for i, line := range lines {
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
		m := tomlKeyLineRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		key := m[1]
		if seen[section] == nil {
			seen[section] = map[string]bool{}
		}
		if seen[section][key] {
			secLabel := section
			if secLabel == "" {
				secLabel = "(root)"
			}
			return "duplicate TOML key " + key + " in [" + secLabel + "] (line " +
				itoa(i+1) + ") — refuse"
		}
		seen[section][key] = true
	}
	return ""
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

var noModuleNamedRe = regexp.MustCompile(`(?i)no module named ['\"]([^'\"]+)['\"]`)

// isLocalProjectModuleName reports first-party import roots that must NOT
// trigger package-manager install (R1: No module named 'app').
func isLocalProjectModuleName(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return false
	}
	root := strings.Split(name, ".")[0]
	switch root {
	case "app", "src", "lib", "tests", "test", "main", "pkg", "internal",
		"cmd", "api", "core", "models", "routers", "schemas", "services",
		"db", "crud", "database", "dependencies", "config", "utils", "common",
		"expensebook", "noteboard", "linkly", "server", "backend", "frontend":
		return true
	default:
		return false
	}
}
