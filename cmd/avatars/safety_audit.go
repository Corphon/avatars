package main

import (
	"strings"
)

// safetyAuditSeverity rates how certain we are that a request is destructive.
type safetyAuditSeverity int

const (
	safetyAuditNone    safetyAuditSeverity = 0 // clearly safe
	safetyAuditReview  safetyAuditSeverity = 1 // needs human review
	safetyAuditBlock   safetyAuditSeverity = 2 // unconditionally dangerous
)

// safetyAuditResult captures the outcome of a post-LLM safety check.
type safetyAuditResult struct {
	Severity safetyAuditSeverity
	Reason   string
}

// postLLMSafetyAudit runs AFTER the LLM has classified the request.
// It only overrides the LLM when the input + chosen command combination
// represents an UNCONDITIONALLY dangerous operation.
//
// This is a SAFETY NET, not a gate. The LLM is the primary classifier.
// We only step in when:
//   1. The LLM returned safe_run, BUT
//   2. The command + input context reveals a clearly destructive operation
//      that would cause irreversible damage BEFORE any approval step.
//
// Context-dependent keywords like "格式化" (format) are NOT checked here —
// the LLM understands context (e.g., "格式化JSON" vs "格式化磁盘").
// Only patterns that are dangerous REGARDLESS of context are checked.
func postLLMSafetyAudit(input string, decision naturalLanguageDecision) safetyAuditResult {
	lowered := strings.ToLower(strings.TrimSpace(input))

	// Only audit safe_run and guarded decisions. direct_answer and clarify
	// don't execute commands, so they're safe by construction.
	if decision.Kind != naturalLanguageDecisionSafeRun &&
		decision.Kind != naturalLanguageDecisionGuarded {
		return safetyAuditResult{Severity: safetyAuditNone}
	}

	// If the LLM already flagged it as guarded, respect that.
	if decision.Kind == naturalLanguageDecisionGuarded {
		return safetyAuditResult{Severity: safetyAuditBlock, Reason: "LLM classified as guarded"}
	}

	// === UNCONDITIONALLY DANGEROUS PATTERNS ===
	// These are destructive regardless of context. They should never
	// execute, even if the LLM misclassified them as safe_run.

	// 1. System-level file system destruction.
	if containsAnyPhrase(lowered,
		"rm -rf /",
		"rm -rf /*",
		"rm -rf ~",
		"rm -rf .",
		"del /f /s",
		"format c:",
		"format d:",
		"mkfs.",
		"> /dev/sda",
		"> /dev/hda",
		"> /dev/nvme",
		"dd if=/dev/zero",
		"dd if=/dev/urandom",
		"shred /dev/",
	) {
		return safetyAuditResult{Severity: safetyAuditBlock, Reason: "system-level file system destruction detected"}
	}

	// 2. Fork bombs and resource exhaustion attacks.
	if containsAnyPhrase(lowered,
		":(){ :|:& };:",
		"fork bomb",
		"while true; do mkdir",
	) {
		return safetyAuditResult{Severity: safetyAuditBlock, Reason: "resource exhaustion attack detected"}
	}

	// 3. Data exfiltration of sensitive system files.
	// "读取 /etc/passwd" alone is fine (read-only analysis).
	// "读取 /etc/passwd 并发送到远程服务器" is exfiltration (I9).
	if containsAnyPhrase(lowered,
		"/etc/shadow",
		"/etc/sudoers",
		"~/.ssh/id_rsa",
		"~/.ssh/id_ed25519",
		"~/.aws/credentials",
		"~/.gcp/",
	) {
		// Exfiltration: sending sensitive files externally.
		if isExfiltrationAttempt(lowered) {
			return safetyAuditResult{Severity: safetyAuditBlock, Reason: "sensitive file exfiltration detected"}
		}
	}

	// 4. /etc/passwd exfiltration specifically (from I9).
	if strings.Contains(lowered, "/etc/passwd") && isExfiltrationAttempt(lowered) {
		return safetyAuditResult{Severity: safetyAuditBlock, Reason: "/etc/passwd exfiltration detected"}
	}

	// 5. Destructive database operations via shell.
	if containsAnyPhrase(lowered,
		"drop database",
		"drop table",
		"truncate table",
		"delete from",
		"drop schema",
		"shutdown immediate",
	) {
		return safetyAuditResult{Severity: safetyAuditReview, Reason: "destructive database operation detected"}
	}

	// 6. Package/module supply-chain attacks.
	if containsAnyPhrase(lowered,
		"pip install",
		"npm install -g",
		"gem install",
		"cargo install",
	) && containsAnyPhrase(lowered, "http://", "https://", "curl", "wget", "| bash", "| sh") {
		return safetyAuditResult{Severity: safetyAuditReview, Reason: "suspicious package installation detected"}
	}

	// 7. Recursive destructive file operations targeting code or user data.
	if containsAnyPhrase(lowered,
		"remove-item -recurse -force",
		"del /s /q",
		"rm -rf node_modules",
		"rm -rf vendor",
	) {
		return safetyAuditResult{Severity: safetyAuditReview, Reason: "recursive file deletion detected"}
	}

	// 8. Mass wipe / delete-everything intents (ZH/EN). Phrase-level only —
	// "删除某个函数" / "delete one file" must NOT match.
	if looksLikeMassDestructiveWipe(lowered) {
		return safetyAuditResult{Severity: safetyAuditBlock, Reason: "mass delete / wipe-all intent detected"}
	}

	return safetyAuditResult{Severity: safetyAuditNone}
}

// looksLikeMassDestructiveWipe detects requests to wipe all / most project or
// system files. Used by post-LLM audit AND by LLM-down fallback so we never
// suggest `avatars run "删除所有文件"`.
func looksLikeMassDestructiveWipe(lowered string) bool {
	lowered = strings.ToLower(strings.TrimSpace(lowered))
	if lowered == "" {
		return false
	}
	return containsAnyPhrase(lowered,
		"删除所有文件",
		"删掉所有文件",
		"删除全部文件",
		"清空所有文件",
		"清空整个项目",
		"删光所有",
		"全部删掉",
		"全部删除",
		"wipe all files",
		"delete all files",
		"delete everything",
		"remove all files",
		"erase all files",
		"rm -rf *",
		"format the disk",
		"格式化磁盘",
		"格式化硬盘",
		"格式化c盘",
		"格式化 c盘",
	)
}

// auditInputForUnconditionalRisk checks the raw user text even when no
// safe_run command was chosen (LLM-down / clarify fallback). Returns a
// Block result for mass-wipe style intents.
func auditInputForUnconditionalRisk(input string) safetyAuditResult {
	lowered := strings.ToLower(strings.TrimSpace(input))
	if looksLikeMassDestructiveWipe(lowered) {
		return safetyAuditResult{Severity: safetyAuditBlock, Reason: "mass delete / wipe-all intent detected"}
	}
	// Reuse the same system-destruction phrases as postLLMSafetyAudit.
	if containsAnyPhrase(lowered,
		"rm -rf /", "rm -rf /*", "rm -rf ~", "format c:", "format d:",
		":(){ :|:& };:", "fork bomb", "dd if=/dev/zero",
	) {
		return safetyAuditResult{Severity: safetyAuditBlock, Reason: "system-level destruction detected"}
	}
	return safetyAuditResult{Severity: safetyAuditNone}
}

// isExfiltrationAttempt checks whether a request involving sensitive files
// is trying to SEND those files externally (exfiltration) rather than just
// read/analyze them locally.
func isExfiltrationAttempt(lowered string) bool {
	exfilSignals := []string{
		"send to",
		"发送",
		"upload",
		"上传",
		"curl",
		"wget --post",
		"http post",
		"http put",
		"nc ",
		"netcat",
		"telegram",
		"discord webhook",
		"slack webhook",
		"remote server",
		"远程",
		"外发",
		"post to",
		"api endpoint",
	}
	for _, signal := range exfilSignals {
		if strings.Contains(lowered, signal) {
			return true
		}
	}
	return false
}

// containsAnyPhrase checks whether lowered contains any of the given phrases.
// Unlike keyword matching, phrases have embedded context (e.g., "rm -rf /"
// is clearly destructive, "delete" alone is not).
func containsAnyPhrase(lowered string, phrases ...string) bool {
	for _, phrase := range phrases {
		if strings.Contains(lowered, phrase) {
			return true
		}
	}
	return false
}

// shouldOverrideLLMDecision returns true when the safety audit should
// override the LLM's classification. This happens only for
// safetyAuditBlock — patterns where the risk is so high that we
// must block regardless of the LLM's judgment.
//
// safetyAuditReview does NOT override — it only adds a warning.
func shouldOverrideLLMDecision(result safetyAuditResult) bool {
	return result.Severity == safetyAuditBlock
}
