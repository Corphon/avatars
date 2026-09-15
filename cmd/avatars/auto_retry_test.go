package main

import (
	"testing"

	"avatars/internal/runtime"
)

func TestShouldAutoRetryAfterRun_F7(t *testing.T) {
	cases := []struct {
		name         string
		status       string
		summary      string
		buildOK      bool
		buildOKKnown bool
		want         bool
	}{
		{name: "construct_plan_fail", status: "failed: construct_plan", want: false},
		{name: "confirm_plan_fail", status: "failed: confirm_plan", want: false},
		{name: "confirm_plan_prefix", status: "failed: confirm_plan: thin tasks", want: false},
		{name: "other_failed_status", status: "failed: builder", want: true},
		{name: "quota_402", status: "failed: openai-compatible provider \"deepseek\" returned 402: Insufficient Balance", want: false},
		{name: "completed_clean", status: "completed", want: false},
		{name: "completed_partial", status: "completed", summary: "PARTIAL success", want: true},
		{name: "completed_failed_word", status: "completed", summary: "build FAILED", want: true},
		{name: "empty_status", status: "", want: true},
		{name: "max_tool_turns_green", status: "completed", summary: "PARTIAL: exceeded max tool turns",
			buildOK: true, buildOKKnown: true, want: false},
		{name: "max_tool_turns_red", status: "completed", summary: "PARTIAL: exceeded max tool turns",
			buildOK: false, buildOKKnown: true, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := shouldAutoRetryAfterRun(runtime.RunResult{
				SynthesisStatus: tc.status,
				Summary:         tc.summary,
				BuildOK:         tc.buildOK,
				BuildOKKnown:    tc.buildOKKnown,
			})
			if got != tc.want {
				t.Fatalf("got %v want %v (status=%q summary=%q)", got, tc.want, tc.status, tc.summary)
			}
		})
	}
}
