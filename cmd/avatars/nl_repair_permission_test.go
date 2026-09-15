package main

import (
	"strings"
	"testing"
)

func TestNaturalLanguagePermissionMode_TestRepairIsMutating(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"go test 还是红的，TestStressSameKey 卡住。请把库修到测试绿，清单勾成和盘上真实进度一致。", "acceptEdits"},
		{"别只分析了。go test 还是红的，请改代码把测试修绿。", "acceptEdits"},
		{"Stop just analyzing. Tests still fail — make the tests pass.", "acceptEdits"},
		{"Don't just look. Fix the failing tests until pytest is green.", "acceptEdits"},
		{"清单看起来还全是空的。请把清单和真实进度对齐。用人话讲现在第几阶段。", "plan"},
		{"analyze this repository and do not modify files", "plan"},
		{"继续", "acceptEdits"},
		{"继续推进", "acceptEdits"},
	}
	for _, tc := range cases {
		got := naturalLanguagePermissionMode(strings.ToLower(tc.input))
		if got != tc.want {
			t.Fatalf("naturalLanguagePermissionMode(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestContinuationWorkSafeRun_RepairKeepsAcceptEdits(t *testing.T) {
	in := "别只分析了。go test 还是红的，请改代码把测试修绿。"
	nd, ok := continuationWorkSafeRunDecision(in, strings.ToLower(in))
	if !ok {
		t.Fatal("expected a continuation run decision")
	}
	joined := strings.Join(nd.Command, " ")
	if !strings.Contains(joined, "--permission-mode acceptEdits") {
		t.Fatalf("repair continue must not stay plan, got %q", joined)
	}
}

func TestLooksLikeNegatedReadOnlyOnly(t *testing.T) {
	if !looksLikeNegatedReadOnlyOnly(strings.ToLower("别只分析了。请改代码")) {
		t.Fatal("negated only-analyze must not count as read-only")
	}
	if looksLikeReadOnlySurveyIntent(strings.ToLower("别只分析了。请改代码")) {
		t.Fatal("read-only survey must ignore negated only-analyze")
	}
	if !looksLikeReadOnlySurveyIntent(strings.ToLower("只分析这个仓库，不要改代码")) {
		t.Fatal("positive only-analyze should stay read-only")
	}
}
