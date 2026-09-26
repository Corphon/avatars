package main

import (
	"strings"
	"unicode"

	"avatars/internal/llm"
)

const talkMaxTokens = 80

const talkSystemPrompt = "Answer in one or two short sentences. No project advice, file names, or commands. If the user wants software created, changed, run, tested, or resumed, reply with the single word TASK."

const talkFallbackAnswer = "Noted. Nothing in the project changed."

// generateTalkAnswer is replaced by tests. The default calls the model
// with a frozen prompt and no project context.
var generateTalkAnswer = defaultGenerateTalkAnswer

func talkAsideDecision(input string, fetch bool) (naturalLanguageDecision, bool) {
	lowered := strings.ToLower(strings.TrimSpace(input))
	if !looksLikeTalkAside(lowered) {
		return naturalLanguageDecision{}, false
	}
	if !fetch {
		return naturalLanguageDecision{
			Kind:       naturalLanguageDecisionAnswer,
			Reason:     "talk aside",
			Confidence: 93,
		}, true
	}
	text, err := generateTalkAnswer(strings.TrimSpace(input))
	if err != nil || strings.TrimSpace(text) == "" {
		text = talkFallbackAnswer
	}
	if talkSentinel(text) {
		return naturalLanguageDecision{}, false
	}
	return naturalLanguageDecision{
		Kind:       naturalLanguageDecisionAnswer,
		Reason:     "talk aside",
		Answer:     truncateTalkReply(text),
		Confidence: 93,
	}, true
}

func looksLikeTalkAside(lowered string) bool {
	if strings.TrimSpace(lowered) == "" {
		return false
	}
	if looksLikeContinuationWorkRequest(lowered) || looksLikeResumeContinuationIntent(lowered) ||
		looksLikeChecklistProgressReconcile(lowered) || looksLikeBootstrapProjectRequest(lowered) ||
		looksLikeInProcessLibraryCreate(lowered) || looksLikeProjectFollowUpQuestion(lowered) ||
		looksLikeVisualSketchSpec(lowered) ||
		looksLikeUnconfirmedStackSwitch(lowered) || looksLikeStorageEngineSwitch(lowered) ||
		looksLikeStageGalleryQuestion(lowered) ||
		looksLikeSelfPlannedCreation(lowered) || mechanicalLocalAnswersYieldToWork(lowered) ||
		looksLikeOfflineWorkRequest(lowered) || isAnalysisRequest(lowered) || isEditIntent(lowered) ||
		looksLikeRepoMutationCue(lowered) {
		return false
	}
	if looksLikeNewSoftwareCreate(lowered) && !weakWriteWithoutSoftwareArtifact(lowered) {
		return false
	}
	if hasProgrammingLanguageSignal(lowered) && (hasCLISignal(lowered) || looksLikeSoftwareArtifact(lowered)) {
		return false
	}
	return true
}

func looksLikeRepoMutationCue(lowered string) bool {
	return containsAnyIntentToken(lowered,
		"实现", "修改", "改成", "加上", "修一下", "跑测试", "重构", "修复",
		"implement", "refactor", "change the", "add a", "add an",
		"run the tests", "fix the", "fix a", "fix this",
	)
}

func looksLikeSoftwareArtifact(lowered string) bool {
	if hasProgrammingLanguageSignal(lowered) || hasCLISignal(lowered) {
		return true
	}
	return containsAnyIntentToken(lowered,
		"project", "library", "package", "module", "crate", "function",
		"server", "service", "单元测试", "unit test",
		"项目", "命令行", "函数", "接口", "服务", "模块",
	)
}

// weakWriteWithoutSoftwareArtifact is chatter that only borrows a create verb
// ("write a poem", "make it into a magnet"). A language, CLI, script, or
// product noun stays on the work path.
func weakWriteWithoutSoftwareArtifact(lowered string) bool {
	if looksLikeSoftwareArtifact(lowered) || hasCLISignal(lowered) {
		return false
	}
	if looksLikeOneShotFileCreate(lowered) || looksLikeSelfPlannedCreation(lowered) || looksLikeProductBehavior(lowered) {
		return false
	}
	return looksLikeNewSoftwareCreate(lowered)
}

func talkLLMRequest(input string) llm.Request {
	return llm.Request{
		SystemPrompt:     talkSystemPrompt,
		UserPrompt:       strings.TrimSpace(input),
		ToolChoice:       "none",
		MaxTokens:        talkMaxTokens,
		ThinkMode:        llm.ThinkModeOff,
		StructuredOutput: false,
	}
}

func defaultGenerateTalkAnswer(input string) (string, error) {
	client, cleanup, err := bootstrapIntentLLMClient()
	if err != nil || client == nil {
		if cleanup != nil {
			cleanup()
		}
		return "", err
	}
	if cleanup != nil {
		defer cleanup()
	}
	llmCtx, cancel := llmCallContext()
	defer cancel()
	response, err := client.Generate(llmCtx, talkLLMRequest(input))
	if err != nil || response.Fallback {
		if err == nil {
			err = errTalkFallback
		}
		return "", err
	}
	return strings.TrimSpace(response.Text), nil
}

type talkFallbackError struct{}

func (talkFallbackError) Error() string { return "talk model unavailable" }

var errTalkFallback = talkFallbackError{}

func talkSentinel(text string) bool {
	t := strings.TrimSpace(text)
	t = strings.Trim(t, ".!。")
	t = strings.TrimSpace(t)
	return strings.EqualFold(t, "TASK")
}

func truncateTalkReply(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return ""
	}
	var sentences []string
	var b strings.Builder
	runes := []rune(text)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		b.WriteRune(r)
		if !isTalkSentenceEnd(r) {
			continue
		}
		if r == '.' && i+1 < len(runes) && !unicode.IsSpace(runes[i+1]) {
			continue
		}
		s := strings.TrimSpace(b.String())
		if s != "" {
			sentences = append(sentences, s)
		}
		b.Reset()
		if len(sentences) == 2 {
			break
		}
	}
	if len(sentences) < 2 {
		if rest := strings.TrimSpace(b.String()); rest != "" {
			sentences = append(sentences, rest)
		}
	}
	if len(sentences) > 2 {
		sentences = sentences[:2]
	}
	out := strings.TrimSpace(strings.Join(sentences, " "))
	r := []rune(out)
	if len(r) > 240 {
		out = strings.TrimSpace(string(r[:240]))
	}
	return out
}

func isTalkSentenceEnd(r rune) bool {
	switch r {
	case '.', '!', '?', '。', '！', '？':
		return true
	default:
		return false
	}
}
