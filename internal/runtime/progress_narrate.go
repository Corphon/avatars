package runtime

import (
	"fmt"
	"path/filepath"
	"strings"

	"avatars/internal/events"
	"avatars/internal/workflow"
)

// NarrateRunProgress turns a runtime event into a lively, operator-facing line.
// Empty string means "don't print". Used by the CLI progress printer.
func NarrateRunProgress(event events.Envelope) string {
	p := event.Payload
	switch event.Type {
	case "run.started":
		if input := payloadString(p, "input"); input != "" {
			return "▶ " + truncateRunes(input, 120)
		}
		return "▶ Run started"

	case "llm.status_updated":
		provider := payloadString(p, "provider")
		model := payloadString(p, "model")
		if provider != "" && model != "" {
			return fmt.Sprintf("◎ LLM ready · %s/%s", provider, model)
		}
		if provider != "" {
			return fmt.Sprintf("◎ LLM ready · %s", provider)
		}

	case "tool.requested":
		return narrateToolRequested(p)
	case "tool.completed":
		return narrateToolCompleted(p)
	case "tool.failed", "tool.denied", "tool.awaiting_approval":
		if s := SummarizeHotEvent(event); s != "" {
			return "✗ " + s
		}

	case "avatar.spawned":
		role := payloadString(p, "role")
		responsibility := payloadString(p, "responsibility")
		if role != "" && responsibility != "" {
			return fmt.Sprintf("● %s joins · %s", role, truncateRunes(responsibility, 80))
		}
		if role != "" {
			return fmt.Sprintf("● %s joins", role)
		}

	case "avatar.assigned", "avatar.handoff", "avatar.spoke", "task.decomposed", "mcp.completed", "workflow.edge_advanced":
		if summary := SummarizeHotEvent(event); summary != "" {
			switch event.Type {
			case "avatar.handoff":
				return "→ " + summary
			case "task.decomposed":
				return "◈ " + summary
			default:
				return summary
			}
		}

	case "workflow.node_activated":
		role := payloadString(p, "assigned_role")
		title := payloadString(p, "title")
		if role != "" && title != "" {
			return fmt.Sprintf("▸ %s · %s", role, title)
		}
		if title != "" {
			return "▸ " + title
		}

	case "workflow.node_completed":
		role := payloadString(p, "assigned_role")
		title := payloadString(p, "node_title")
		if role != "" && title != "" {
			return fmt.Sprintf("✓ %s done · %s", role, title)
		}
		if title != "" {
			return "✓ " + title
		}

	case "repository.exploration_completed":
		if summary := payloadString(p, "summary"); summary != "" {
			return summary
		}
		if count, ok := p["target_count"]; ok {
			return fmt.Sprintf("Researcher picked %v files to read", count)
		}

	case "repository.exploration_round_started":
		title := payloadString(p, "title")
		round := payloadInt(p, "round")
		reason := payloadString(p, "reason")
		if title != "" {
			if round > 0 && reason != "" {
				return fmt.Sprintf("🔎 pass %d · %s (%s)", round, title, reason)
			}
			if round > 0 {
				return fmt.Sprintf("🔎 pass %d · %s", round, title)
			}
			return "🔎 " + title
		}

	case "repository.exploration_round_completed":
		title := payloadString(p, "title")
		round := payloadInt(p, "round")
		if title != "" {
			if round > 0 {
				return fmt.Sprintf("✓ pass %d done · %s", round, title)
			}
			return "✓ pass done · " + title
		}

	case "repository.exploration_empty":
		if summary := payloadString(p, "summary"); summary != "" {
			return summary
		}
		return "Researcher found no repository evidence"

	case "skill.candidate_prepared":
		if name := payloadString(p, "name"); name != "" {
			return "Skill draft ready: " + name
		}
		return "Skill draft ready"

	case "llm.started":
		provider := payloadString(p, "provider")
		role := firstNonEmpty(payloadString(p, "role"), payloadString(p, "node_role"), payloadString(p, "avatar_role"), inferRoleFromAvatar(payloadString(p, "avatar_id")))
		bytesHint := ""
		if n := payloadInt(p, "prompt_bytes_total"); n > 0 {
			bytesHint = fmt.Sprintf(" (%d prompt bytes)", n)
		}
		if provider != "" && role != "" {
			return fmt.Sprintf("⏳ %s drafting with %s…%s", role, provider, bytesHint)
		}
		if provider != "" {
			return fmt.Sprintf("⏳ LLM drafting with %s…%s", provider, bytesHint)
		}
		if role != "" {
			return fmt.Sprintf("⏳ %s drafting…%s", role, bytesHint)
		}
		if bytesHint != "" {
			return "⏳ LLM drafting…" + bytesHint
		}
		return "⏳ LLM drafting…"

	case "llm.streaming":
		if msg := payloadString(p, "message"); msg != "" {
			return "⏳ " + msg
		}
		if n := payloadInt(p, "bytes"); n > 0 {
			return fmt.Sprintf("⏳ receiving… (%d bytes)", n)
		}
		return "⏳ receiving…"

	case "llm.thinking":
		if preview := payloadString(p, "preview"); preview != "" {
			line := "💭 " + truncateRunes(preview, 160)
			if n := payloadInt(p, "chars"); n > 0 {
				return fmt.Sprintf("%s (%d chars)", line, n)
			}
			return line
		}
		if msg := payloadString(p, "message"); msg != "" {
			return "💭 " + msg
		}
		if n := payloadInt(p, "chars"); n > 0 {
			return fmt.Sprintf("💭 thinking… (%d chars)", n)
		}
		return "💭 thinking…"

	case "builder.provider_busy", "workflow.provider_busy":
		return "⚠ LLM provider busy — stop retries; switch model or try later"

	case "llm.completed":
		provider := payloadString(p, "provider")
		model := payloadString(p, "model")
		role := firstNonEmpty(payloadString(p, "role"), inferRoleFromAvatar(payloadString(p, "avatar_id")))
		prefix := "LLM"
		if role != "" {
			prefix = role
		}
		if provider != "" && model != "" {
			return fmt.Sprintf("✓ %s draft ready · %s/%s", prefix, provider, model)
		}
		return fmt.Sprintf("✓ %s draft ready", prefix)

	case "llm.failed":
		if errText := payloadString(p, "error"); errText != "" {
			return "✗ LLM failed: " + truncateRunes(errText, 100)
		}
		return "✗ LLM failed"

	case "verification.started":
		if command := payloadString(p, "command"); command != "" {
			return "🔍 verifying · " + truncateRunes(command, 80)
		}
		return "🔍 verifying…"

	case "verification.completed":
		if summary := payloadString(p, "summary"); summary != "" {
			return "🔍 " + summary
		}
		return "🔍 verification done"

	case "heartbeat.builder_generating":
		if msg := payloadString(p, "message"); msg != "" {
			return "⏳ " + msg
		}
		return "⏳ Builder generating…"
	case "heartbeat.builder_writing":
		if msg := payloadString(p, "message"); msg != "" {
			return "✍ " + msg
		}
		return "✍ Writing files…"
	case "heartbeat.critic_auditing":
		if msg := payloadString(p, "message"); msg != "" {
			return "🔍 " + msg
		}
		return "🔍 Critic auditing…"

	case "workflow.phase_advanced":
		from := payloadInt(p, "from_phase")
		to := payloadInt(p, "to_phase")
		if to > 0 {
			return fmt.Sprintf("📋 Phase %d → %d", from, to)
		}
		return "📋 Phase advanced"
	case "workflow.project_completed":
		return "🏁 Project complete — all phases done"
	case "workflow.phase_advance_blocked":
		reason := payloadString(p, "reason")
		if reason == "" {
			reason = "blocked"
		}
		return fmt.Sprintf("⛔ Phase advance blocked · %s", reason)
	case "workflow.phase_detail_expanded":
		phase := payloadInt(p, "phase")
		if phase > 0 {
			return fmt.Sprintf("📄 Expanded phase%d.md", phase)
		}
		return "📄 Phase detail expanded"
	case "workflow.tasks_marked":
		if count := payloadInt(p, "count"); count > 0 {
			return fmt.Sprintf("☑ marked %d todo item(s)", count)
		}
	case "workflow.criteria_marked":
		if count := payloadInt(p, "count"); count > 0 {
			return fmt.Sprintf("☑ met %d success criteria", count)
		}

	case "builder.cross_language_blocked":
		targetLang := payloadString(p, "target_language")
		rejectedFiles := payloadString(p, "rejected_files")
		keptFiles := payloadInt(p, "kept_files")
		if targetLang != "" && rejectedFiles != "" {
			return fmt.Sprintf("🛡 blocked %s (want %s), kept %d", rejectedFiles, targetLang, keptFiles)
		}
		return "🛡 language guard blocked files"
	case "builder.diff_apply_applied":
		file := payloadString(p, "file")
		insertions := payloadInt(p, "insertions")
		if file != "" && insertions > 0 {
			return fmt.Sprintf("✎ precise_edit · %s (+%d)", filepath.Base(file), insertions)
		}
		return "✎ precise_edit applied"
	case "builder.coordinator_edit":
		if file := payloadString(p, "file"); file != "" {
			return "✎ editing · " + filepath.Base(file)
		}
	case "builder.code_generation_converged":
		reason := payloadString(p, "reason")
		if reason != "" {
			return "✓ Builder converged · " + truncateRunes(reason, 80)
		}
		return "✓ Builder converged"
	case "builder.code_generation_retrying":
		attempt := payloadInt(p, "attempt")
		if attempt > 0 {
			return fmt.Sprintf("↻ Builder retrying code gen (attempt %d)", attempt)
		}
		return "↻ Builder retrying code gen"
	case "builder.post_build_failed", "builder.health_route_missing", "builder.missing_layout":
		if errText := payloadString(p, "error"); errText != "" {
			return "✗ Builder health · " + truncateRunes(errText, 100)
		}
		return "✗ Builder health check failed"
	case "builder.project_health_verified":
		return "✓ project health green"
	case "builder.code_implementation_started":
		return "✍ Builder implementing…"
	case "builder.code_implementation_completed":
		return "✓ Builder implementation pass done"
	case "builder.path_sanitized":
		kept := payloadInt(p, "kept")
		if kept > 0 {
			return fmt.Sprintf("🛡 sanitized paths · kept %d", kept)
		}
		return "🛡 sanitized paths"
	case "builder.go_mod_tidy", "critic.go_mod_tidy":
		return "◎ go mod tidy"
	case "builder.before_write_snapshot":
		if n := payloadInt(p, "files"); n > 0 {
			return fmt.Sprintf("✍ writing %d file(s)…", n)
		}
		return "✍ writing files…"
	case "builder.node_retry_limit_exceeded":
		nodeID := payloadString(p, "node_id")
		retries := payloadInt(p, "retries")
		if nodeID != "" {
			return fmt.Sprintf("✗ Builder gave up on %s after %d retries", nodeID, retries)
		}
		return "✗ Builder retry limit exceeded"

	case "critic.plan_review":
		return "🔍 Critic reviewing plan before build"
	case "critic.pre_flight", "critic.preflight":
		return "🔍 Critic preflight"
	case "critic.rebuild_skip_green":
		return "✓ Critic · rebuild skipped (already green)"
	case "critic.project_health_verified":
		return "✓ Critic · project health green"
	case "workflow.scaffold_progress_marked":
		if n := payloadInt(p, "count"); n > 0 {
			return fmt.Sprintf("☑ scaffold progress · %d item(s)", n)
		}
		return "☑ scaffold progress marked"
	case "critic.preflight_health_err":
		if errText := payloadString(p, "error"); errText != "" {
			return "✗ Critic preflight · " + truncateRunes(errText, 100)
		}
		return "✗ Critic preflight failed"
	case "critic.phase_advance_builder":
		phase := payloadInt(p, "phase")
		if phase > 0 {
			return fmt.Sprintf("→ Critic hands Builder Phase %d", phase)
		}
		return "→ Critic dispatching Builder after advance"
	case "critic.dispatch_stalled":
		return "⚠ Critic↔Builder dispatch stalled"

	case "workflow.file_completed":
		return narrateFileCompleted(p)

	case "run.change_summary":
		if summary := payloadString(p, "files_changed_summary"); summary != "" {
			return "📎 " + summary
		}
	case "run.completed":
		status := payloadString(p, "status")
		if status == "" {
			return ""
		}
		syn := payloadString(p, "synthesis_status")
		files := payloadString(p, "files_changed_summary")
		line := "Run status: " + status
		if syn != "" {
			line += " | Synthesis: " + syn
		}
		if files != "" {
			line += " | " + files
		}
		return line
	}
	return ""
}

func narrateToolRequested(p map[string]any) string {
	tool := strings.ToLower(payloadString(p, "tool"))
	// F105: always narrate the rewrite-after path, never remapped_from.
	path := firstNonEmpty(payloadString(p, "path"), payloadString(p, "file"), payloadString(p, "target"))
	cmd := firstNonEmpty(payloadString(p, "command"), payloadString(p, "cmd"))
	op := payloadString(p, "operation")
	switch tool {
	case "read":
		if path != "" {
			return "Reading: " + path
		}
		return "Reading…"
	case "write", "write_file", "file_write":
		if path != "" {
			return "✍ writing · " + path
		}
		return "✍ writing…"
	case "edit_file", "precise_edit", "patch", "apply_patch":
		if path != "" {
			return "✎ editing · " + path
		}
		return "✎ editing…"
	case "shell", "bash", "exec":
		if cmd != "" {
			return "$ " + truncateRunes(cmd, 90)
		}
		return "$ shell…"
	default:
		if tool == "" {
			return ""
		}
		if path != "" {
			return fmt.Sprintf("⚙ %s · %s", tool, path)
		}
		if cmd != "" {
			return fmt.Sprintf("⚙ %s · %s", tool, truncateRunes(cmd, 70))
		}
		if op != "" {
			return fmt.Sprintf("⚙ %s (%s)", tool, op)
		}
		return "⚙ " + tool
	}
}

func narrateToolCompleted(p map[string]any) string {
	tool := strings.ToLower(payloadString(p, "tool"))
	path := firstNonEmpty(payloadString(p, "path"), payloadString(p, "file"))
	summary := payloadString(p, "summary")
	switch tool {
	case "read":
		if summary != "" {
			return summary
		}
		if path != "" {
			return "✓ read " + filepath.Base(path)
		}
	case "write", "write_file", "file_write":
		if path != "" {
			return "✓ wrote " + path
		}
		if summary != "" {
			return "✓ " + truncateRunes(summary, 90)
		}
		return "✓ write done"
	case "edit_file", "precise_edit", "patch", "apply_patch":
		if path != "" {
			return "✓ edited " + path
		}
		return "✓ edit done"
	case "shell", "bash", "exec":
		if summary != "" {
			return "✓ shell · " + truncateRunes(summary, 80)
		}
		return "✓ shell done"
	}
	if summary != "" {
		return "✓ " + truncateRunes(summary, 100)
	}
	if tool != "" {
		return "✓ " + tool
	}
	return ""
}

func narrateFileCompleted(p map[string]any) string {
	file := payloadString(p, "file")
	if file == "" {
		return ""
	}
	afterLines := payloadInt(p, "after_lines")
	lineDelta := payloadInt(p, "line_delta")
	done := payloadInt(p, "todo_done")
	total := payloadInt(p, "todo_total")
	var deltaStr string
	switch {
	case payloadInt(p, "before_lines") == 0 && afterLines > 0:
		deltaStr = fmt.Sprintf("new %dL", afterLines)
	case lineDelta > 0:
		deltaStr = fmt.Sprintf("+%dL", lineDelta)
	case lineDelta < 0:
		deltaStr = fmt.Sprintf("%dL", lineDelta)
	default:
		deltaStr = fmt.Sprintf("%dL", afterLines)
	}
	base := fmt.Sprintf("%s  [%s]", filepath.Base(file), deltaStr)
	if total > 0 {
		return fmt.Sprintf("%s  todo:%d/%d", base, done, total)
	}
	return base
}

// LivePhasePulseLine returns a one-line phase/todo snapshot for stderr.
func LivePhasePulseLine(projectRoot string) string {
	if projectRoot == "" {
		projectRoot = "."
	}
	planContent, err := workflow.ReadWorkflowDoc(projectRoot, "plan")
	if err != nil {
		return ""
	}
	meta := workflow.ParsePlanMeta(planContent)
	if meta.ActivePhase < 1 {
		return ""
	}
	phase := meta.ActivePhase
	if todoContent, todoErr := workflow.ReadWorkflowDoc(projectRoot, "todo"); todoErr == nil {
		if n := workflow.ParseTodoActivePhase(todoContent); n > 0 {
			phase = n
		}
	}
	done, total := workflow.CountTodoProgress(projectRoot)
	if total <= 0 {
		return fmt.Sprintf("◎ Phase %d/%d", phase, meta.PhaseCount)
	}
	return fmt.Sprintf("◎ Phase %d/%d · todo %d/%d", phase, meta.PhaseCount, done, total)
}

// ShouldPulsePhase reports whether this event warrants a live phase pulse line.
func ShouldPulsePhase(eventType string) bool {
	switch eventType {
	case "workflow.node_activated", "workflow.phase_advanced", "workflow.tasks_marked",
		"workflow.criteria_marked", "workflow.phase_advance_blocked", "workflow.file_completed",
		"builder.project_health_verified", "critic.project_health_verified":
		return true
	default:
		return false
	}
}

func truncateRunes(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// thinkingPreviewTail is the current CoT direction for progress lines:
// collapsed whitespace, last `max` runes (not the opening). Empty if none.
func thinkingPreviewTail(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if s == "" || max <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return "…" + string(r[len(r)-max:])
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func inferRoleFromAvatar(avatarID string) string {
	lower := strings.ToLower(avatarID)
	switch {
	case strings.Contains(lower, "builder"):
		return "Builder"
	case strings.Contains(lower, "critic"):
		return "Critic"
	case strings.Contains(lower, "research"):
		return "Researcher"
	case strings.Contains(lower, "plan"):
		return "Planner"
	case strings.Contains(lower, "synth"):
		return "Synthesizer"
	default:
		return ""
	}
}
