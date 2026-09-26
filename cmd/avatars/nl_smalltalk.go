package main

import "strings"

// offTopicChatDecision answers chatter that is not project work, before the
// intent LLM. The router prefix stays untouched, and these turns do not spend it.
// Replies vary with the utterance so a fixed script is not the only answer.
func offTopicChatDecision(input string) (naturalLanguageDecision, bool) {
	lowered := strings.ToLower(strings.TrimSpace(input))
	if !looksLikeOffTopicChat(lowered) {
		return naturalLanguageDecision{}, false
	}
	return naturalLanguageDecision{
		Kind:       naturalLanguageDecisionAnswer,
		Reason:     "off-topic chat answered locally",
		Answer:     offTopicChatAnswer(input, lowered),
		Confidence: 93,
	}, true
}

func looksLikeOffTopicChat(lowered string) bool {
	if lowered == "" {
		return false
	}
	if looksLikeNewSoftwareCreate(lowered) || looksLikeSelfPlannedCreation(lowered) ||
		looksLikeResumeContinuationIntent(lowered) || looksLikeBootstrapProjectRequest(lowered) ||
		looksLikeInProcessLibraryCreate(lowered) || looksLikeForbiddenMutationAsk(lowered) {
		return false
	}
	if looksLikeDesignChoiceAsk(lowered) && !containsAnyIntentToken(lowered, "下雨", "天气", "带伞", "weather", "umbrella", "raining") {
		return false
	}
	if mechanicalLocalAnswersYieldToWork(lowered) {
		return false
	}
	return containsAnyIntentToken(lowered,
		"午饭", "晚饭", "早饭", "吃饭", "盒饭", "拉面", "咖啡", "夜宵",
		"天气", "下雨", "带伞", "降温", "出太阳",
		"打油诗", "写首诗", "写两句", "顺口溜", "讲个笑话", "说个笑话", "唱首歌",
		"猫踩", "猫刚踩", "狗把", "宠物",
		"无聊", "闲聊", "瞎聊",
		"lunch", "dinner", "breakfast", "coffee", "weather", "raining", "umbrella",
		"doggerel", "write a poem", "write me a poem", "tell me a joke", "tell a joke",
		"sing a song", "small talk", "i'm bored", "im bored",
		"cat stepped", "cat on the keyboard", "stepped on the keyboard",
		"my dog", "my cat",
	)
}

func offTopicChatAnswer(input, lowered string) string {
	switch {
	case containsAnyIntentToken(lowered, "天气", "下雨", "带伞", "伞", "降温", "出太阳", "weather", "raining", "umbrella"):
		return pickChatVariant(input, []string{
			"I don't have live weather, so I can't say whether to take an umbrella. That does not change the project.",
			"No forecast from here. The workspace is unchanged — say \"continue\" to get back to it.",
			"Weather stays outside this repo. I did not touch the project.",
		})
	case containsAnyIntentToken(lowered, "打油诗", "写首诗", "写两句", "顺口溜", "doggerel", "write a poem", "write me a poem"):
		return pickChatVariant(input, verseReplies(lowered))
	case containsAnyIntentToken(lowered, "讲个笑话", "说个笑话", "tell me a joke", "tell a joke"):
		return pickChatVariant(input, []string{
			"A joke, then: the bug was in the comment, and the comment was the bug. I did not touch the project.",
			"Two processes walk into a lock. Neither leaves. The repo is the same.",
			"I can spare a joke. It does not change the project — say \"continue\" when you want the work.",
		})
	case containsAnyIntentToken(lowered, "唱首歌", "sing a song"):
		return pickChatVariant(input, []string{
			"I won't sing, and I didn't change any files.",
			"No song, no edits. Say \"continue\" to get back to the project.",
		})
	case containsAnyIntentToken(lowered, "猫踩", "猫刚踩", "狗把", "宠物", "cat stepped", "cat on the keyboard", "stepped on the keyboard", "my dog", "my cat"):
		return pickChatVariant(input, []string{
			"Accidental keystrokes do not cancel the last real request. Say \"continue\" to resume it, or repeat what you want.",
			"Pets don't edit the plan. The last real request still stands — say \"continue\" or repeat it.",
			"That aside doesn't change the project. Say \"continue\" when you want the work.",
		})
	case containsAnyIntentToken(lowered, "无聊", "闲聊", "瞎聊", "small talk", "i'm bored", "im bored"):
		return pickChatVariant(input, []string{
			"We can pause. Nothing on disk changed. Say \"continue\" to pick the project back up.",
			"Noted, and the workspace is the same. Say \"continue\" when you want to work.",
		})
	default:
		return pickChatVariant(input, []string{
			"I don't eat, so I can't judge the food. That doesn't change the project — say \"continue\" when you want to get back to it.",
			"Meal talk stays off the repo. I did not touch the project.",
			"I can't taste that. The project is unchanged — say \"continue\" to resume.",
		})
	}
}

func verseReplies(lowered string) []string {
	generic := []string{
		"Lines for the moment, then back to the tree.\nI did not touch the project.",
		"A couplet, and the files stay put.\nNothing on disk changed.",
		"Rhyme is cheap; the workspace is the same.\nSay \"continue\" when you want the work.",
	}
	if containsAnyIntentToken(lowered, "哈希", "hash", "摘要", "digest") {
		return append([]string{
			"Hash in, hash out, the bits stay true;\nSame bytes in, same digest too.\nI did not touch the project.",
		}, generic...)
	}
	return generic
}

func pickChatVariant(seed string, options []string) string {
	if len(options) == 0 {
		return ""
	}
	var n uint32
	for i := 0; i < len(seed); i++ {
		n = n*33 + uint32(seed[i])
	}
	return options[int(n)%len(options)]
}
