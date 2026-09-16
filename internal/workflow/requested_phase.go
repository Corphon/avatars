package workflow

import (
	"regexp"
	"strconv"
	"strings"
)

// Explicit lock: user forbids advancing past Phase N this run.
var phaseLockIntentRe = regexp.MustCompile(`(?i)(?:只做|仅做|本轮只做|本轮仅做|本轮只要|只要|only\s+(?:do\s+)?|just\s+(?:do\s+)?)\s*(?:phase|阶段)\s*(\d+)`)
var phaseLockOnlySuffixRe = regexp.MustCompile(`(?i)(?:phase|阶段)\s*(\d+)\s*(?:only|就够了|即可|就行)`)

// Target phase for implementation (not necessarily a hard lock).
// Avoid bare "做" — it matches mid-word in Chinese (e.g. 完成) and steals "阶段 N".
var phaseTargetRe = regexp.MustCompile(`(?i)(?:进入|现在做|现在进入|切换到|继续做|本轮做|按|implement(?:ing)?|resume)\s*(?:phase|阶段)\s*(\d+)`)
var phaseBareRe = regexp.MustCompile(`(?i)(?:phase|阶段)\s*(\d+)`)

var fullCourseNeedles = []string{
	"都跑通", "全部做完", "全部完成", "从头做到尾", "做到尾", "做到底",
	"允许升", "自动进入下一段", "自动进入下一", "不要停在", "不要整轮锁死",
	"直到 phase", "直到阶段", "full course", "all phases",
	"三段全部", "所有阶段", "三段都", "三段做到", "跑完全程", "跑完全部",
}

// WantsFullCourse reports whether the user wants multi-phase progress in one
// run (or across runs without locking to Active). Used to keep lockedPhase=0.
func WantsFullCourse(input string) bool {
	lower := strings.ToLower(strings.TrimSpace(input))
	if lower == "" {
		return false
	}
	for _, n := range fullCourseNeedles {
		if strings.Contains(lower, strings.ToLower(n)) {
			return true
		}
	}
	if countChineseStageMarkers(input) >= 2 {
		return true
	}
	// Multiple distinct Phase/阶段 numbers without "只做" → course intent.
	// A lone "阶段 3" must NOT count as full course (max number ≠ multi-phase).
	if countDistinctExplicitPhases(input) >= 2 && ParsePhaseLock(input) == 0 {
		return true
	}
	return false
}

// ParsePhaseLock returns N when the user explicitly locks this run to Phase N.
// Returns 0 when there is no hard lock.
func ParsePhaseLock(input string) int {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return 0
	}
	if m := phaseLockIntentRe.FindStringSubmatch(trimmed); len(m) >= 2 {
		return atoiPhase(m[1])
	}
	if m := phaseLockOnlySuffixRe.FindStringSubmatch(trimmed); len(m) >= 2 {
		return atoiPhase(m[1])
	}
	return 0
}

// ParseRequestedPhase extracts a phase number the user is talking about.
// Prefer explicit lock, then target phrasing, then first bare "Phase N".
// Full-course prompts that only mention Phase titles return 0 (no lock target).
func ParseRequestedPhase(input string) int {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return 0
	}
	if n := ParsePhaseLock(trimmed); n > 0 {
		return n
	}
	if WantsFullCourse(trimmed) {
		// Full course: bare "Phase 1" titles must not become a hard target/lock.
		if n := parsePhaseTarget(trimmed); n > 0 {
			return n
		}
		return 0
	}
	if n := parsePhaseTarget(trimmed); n > 0 {
		return n
	}
	m := phaseBareRe.FindStringSubmatch(trimmed)
	if len(m) < 2 {
		return 0
	}
	return atoiPhase(m[1])
}

func parsePhaseTarget(input string) int {
	m := phaseTargetRe.FindStringSubmatch(input)
	if len(m) < 2 {
		return 0
	}
	return atoiPhase(m[1])
}

func atoiPhase(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return 0
	}
	return n
}

// ResolveLockedPhase returns the hard phase lock for this run.
// 0 = no lock (Critic may advance). Only explicit "只做 Phase N" locks.
// Full-course intent never locks to Active.
func ResolveLockedPhase(input string, activePhase int) int {
	_ = activePhase
	if n := ParsePhaseLock(input); n > 0 {
		return n
	}
	return 0
}

// ResolveImplementPhase returns which phase Builder should implement now.
// Prefer lock, then explicit target, then Active Phase (default 1).
func ResolveImplementPhase(input string, activePhase int) int {
	if n := ParsePhaseLock(input); n > 0 {
		return n
	}
	if n := parsePhaseTarget(input); n > 0 {
		return n
	}
	// Non-full-course single "Phase N" mention still targets that phase.
	if !WantsFullCourse(input) {
		if n := ParseRequestedPhase(input); n > 0 {
			return n
		}
	}
	if activePhase < 1 {
		return 1
	}
	return activePhase
}
