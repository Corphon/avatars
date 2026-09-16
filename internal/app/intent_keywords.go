package app

import (
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// IntentKeywords is the runtime-loaded form of configs/intent_keywords.yaml.
// L1: Loaded once at startup and used by the NL classifier instead of
// hardcoded lists scattered across natural_language.go and complexity.go.
type IntentKeywords struct {
	MultiStepVerbs  KeywordLists        `yaml:"multi_step_verbs"`
	CodeImplVerbs   KeywordLists        `yaml:"code_impl_verbs"`
	LargeVerbs      KeywordLists        `yaml:"large_verbs"`
	CodeIndicators  []string            `yaml:"code_indicators"`
	DeepSignals     KeywordLists        `yaml:"deep_signals"`
	Components      [][]string          `yaml:"components"`
	DocExts         []string            `yaml:"doc_exts"`
	CodeExts        []string            `yaml:"code_exts"`
	CodeLangs       []string            `yaml:"code_langs"`
	CodeTech        []string            `yaml:"code_tech"`
	SingleAction    KeywordLists        `yaml:"single_action"`
	Documentation   KeywordLists        `yaml:"documentation"`
	Exts            []string            `yaml:"exts"`
}

// KeywordLists holds English and Chinese keyword variants for a category.
type KeywordLists struct {
	EN       []string `yaml:"en"`
	ZH       []string `yaml:"zh"`
	Analysis []string `yaml:"analysis,omitempty"`
}

// GlobalIntentKeywords is populated at startup from intent_keywords.yaml.
// L1: Single source of truth for keyword-based intent classification.
var GlobalIntentKeywords *IntentKeywords

// LoadIntentKeywords reads configs/intent_keywords.yaml into the global singleton.
// Call once during startup. Falls back to empty (no keywords) if file is absent.
func LoadIntentKeywords() {
	data, err := os.ReadFile("configs/intent_keywords.yaml")
	if err != nil {
		GlobalIntentKeywords = &IntentKeywords{}
		return
	}
	var kw IntentKeywords
	if err := yaml.Unmarshal(data, &kw); err != nil {
		GlobalIntentKeywords = &IntentKeywords{}
		return
	}
	GlobalIntentKeywords = &kw
}

// Keywords returns the global keyword set, or an empty fallback if not loaded.
func Keywords() *IntentKeywords {
	if GlobalIntentKeywords != nil {
		return GlobalIntentKeywords
	}
	return &IntentKeywords{}
}

// HasKeyword checks whether the given text contains any keyword from the
// specified category and language. Categories: "code_impl", "multi_step",
// "large", "single_action", "documentation", "deep".
func HasKeyword(text string, category string, lang string) bool {
	kw := Keywords()
	lower := strings.ToLower(text)

	matchList := func(list []string) bool {
		for _, k := range list {
			if strings.Contains(lower, strings.ToLower(k)) {
				return true
			}
		}
		return false
	}

	switch category {
	case "code_impl":
		return matchList(kw.CodeImplVerbs.EN) || matchList(kw.CodeImplVerbs.ZH)
	case "multi_step":
		return matchList(kw.MultiStepVerbs.EN) || matchList(kw.MultiStepVerbs.ZH)
	case "large":
		return matchList(kw.LargeVerbs.EN) || matchList(kw.LargeVerbs.ZH)
	case "single_action":
		return matchList(kw.SingleAction.EN) || matchList(kw.SingleAction.ZH)
	case "documentation":
		return matchList(kw.Documentation.EN) || matchList(kw.Documentation.ZH)
	case "deep":
		return matchList(kw.DeepSignals.EN) || matchList(kw.DeepSignals.ZH) || matchList(kw.CodeTech) || matchList(kw.CodeLangs)
	case "code_indicator":
		if lang == "" || lang == "en" {
			return matchList(kw.CodeIndicators)
		}
	case "analysis":
		return matchList(kw.MultiStepVerbs.Analysis)
	}
	return false
}
