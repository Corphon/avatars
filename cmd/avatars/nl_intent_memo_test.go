package main

import "testing"

func TestIntentLLMMemo_SameInputHitsWithoutReprint(t *testing.T) {
	storeIntentLLMMemo("hello cache", llmRouterDecision{Action: "safe_run", Reason: "first"}, true)
	t.Cleanup(resetIntentLLMMemo)

	d, ok, hit := recallIntentLLMMemo("hello cache")
	if !hit || !ok || d.Reason != "first" {
		t.Fatalf("F115: expected memo hit, got hit=%v ok=%v %+v", hit, ok, d)
	}
	d2, ok2, hit2 := recallIntentLLMMemo("different")
	if hit2 {
		t.Fatalf("different input must miss, got %+v ok=%v", d2, ok2)
	}
}
