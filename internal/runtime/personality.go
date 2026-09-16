package runtime

import "strings"

// personalityPromptFragment builds the shared PERSONALITY block injected into
// role system prompts (Synthesizer, Builder, Direct, Critic fixes, …).
// Empty config yields an empty string (no injection).
func personalityPromptFragment(cfg PersonalityConfig) string {
	var parts []string
	if ctx := strings.TrimSpace(cfg.OperatorContext); ctx != "" {
		parts = append(parts, "Operator context: "+ctx)
	}
	if lang := strings.TrimSpace(cfg.Language); lang != "" {
		parts = append(parts,
			"Respond to the operator in "+lang+".",
			"User-facing prose (summaries, explanations, docs the task asks for) must use "+lang+".",
			"Keep code identifiers, APIs, paths, and protocol tokens in English unless the task specifies otherwise.",
		)
	}
	switch strings.ToLower(strings.TrimSpace(cfg.Style)) {
	case "concise":
		parts = append(parts, "Be concise. Prefer short paragraphs and bullets for operator-facing text.")
	case "detailed":
		parts = append(parts, "Be thorough when explaining to the operator; use clear markdown sections.")
	}
	if len(parts) == 0 {
		return ""
	}
	return "=== PERSONALITY ===\n" + strings.Join(parts, "\n")
}

// appendPersonality appends the personality fragment to a system prompt.
func appendPersonality(systemPrompt string, cfg PersonalityConfig) string {
	frag := personalityPromptFragment(cfg)
	if frag == "" {
		return systemPrompt
	}
	if strings.TrimSpace(systemPrompt) == "" {
		return frag
	}
	return systemPrompt + "\n\n" + frag
}

// synthesizerAnalysisLanguageLine chooses the operator-facing language instruction.
// Configured language wins; otherwise match the operator request (not hard-coded English).
func synthesizerAnalysisLanguageLine(cfg PersonalityConfig) string {
	if lang := strings.TrimSpace(cfg.Language); lang != "" {
		return "Write the operator-facing delivery summary in " + lang + "."
	}
	return "Write the operator-facing delivery summary in the operator's language (match the user request). Use English only when the request language is unclear."
}
