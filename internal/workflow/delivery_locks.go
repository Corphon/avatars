package workflow

import (
	"regexp"
	"strings"
)

var (
	reNamedASCII = regexp.MustCompile(`(?i)(?:名字叫|库名就叫|库名叫|named|called)\s*([A-Za-z][A-Za-z0-9_-]{1,40})`)
)

// DeliveryLocksUserNote extracts user-stated name/order constraints for the
// Planner/Builder *user* turn only (stable system prefixes stay cacheable).
// Cross-language: min vs max priority and FIFO-on-ties show up in every heap.
func DeliveryLocksUserNote(requirement string) string {
	locks := extractDeliveryLocks(requirement)
	if len(locks) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nDELIVERY LOCKS (copy into Key Decisions; tests must match, do not invert to go green):\n")
	for _, line := range locks {
		b.WriteString("- ")
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

func extractDeliveryLocks(requirement string) []string {
	text := strings.TrimSpace(requirement)
	if text == "" {
		return nil
	}
	lower := strings.ToLower(text)
	var out []string
	if m := reNamedASCII.FindStringSubmatch(text); len(m) == 2 {
		out = append(out, "Package/module/crate name: "+m[1])
	}
	switch {
	case strings.Contains(lower, "数字越小越优先") || strings.Contains(lower, "越小越先") ||
		strings.Contains(lower, "smaller first") || strings.Contains(lower, "min-heap") ||
		strings.Contains(lower, "min heap") || strings.Contains(lower, "lower number first"):
		out = append(out, "Pop order: smaller integer priority first (min-heap)")
	case strings.Contains(lower, "数字越大越优先") || strings.Contains(lower, "越大越先") ||
		strings.Contains(lower, "larger first") || strings.Contains(lower, "max-heap") ||
		strings.Contains(lower, "max heap") || strings.Contains(lower, "higher number first"):
		out = append(out, "Pop order: larger integer priority first (max-heap)")
	}
	if strings.Contains(lower, "进入顺序") || strings.Contains(lower, "同优先级") &&
		(strings.Contains(lower, "顺序") || strings.Contains(lower, "fifo")) ||
		strings.Contains(lower, "fifo") || strings.Contains(lower, "insertion order") {
		out = append(out, "Equal priority: FIFO insertion order (later equal-priority items do not jump)")
	}
	return out
}
