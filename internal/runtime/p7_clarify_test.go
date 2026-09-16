
package runtime

import "testing"

func TestP7_DetectAmbiguity_AddFeatureWithoutSurface(t *testing.T) {
    qs := detectAmbiguity("add a search command")
    if len(qs) == 0 {
        t.Fatal("P7 FAIL: 'add a search command' should trigger clarification")
    }
    if qs[0].ID != "clarify-surface" {
        t.Fatalf("P7 FAIL: expected clarify-surface, got %s", qs[0].ID)
    }
}

func TestP7_DetectAmbiguity_AddFeatureWithSurface(t *testing.T) {
    qs := detectAmbiguity("add a search subcommand in main.go")
    if len(qs) != 0 {
        t.Fatalf("P7 FAIL: specific task should NOT trigger clarification, got %d questions", len(qs))
    }
}

func TestP7_DetectAmbiguity_MultipleItems(t *testing.T) {
    qs := detectAmbiguity("add search, fix the UI, and update the README")
    if len(qs) == 0 {
        t.Fatal("P7 FAIL: multi-item task should trigger clarification")
    }
}

func TestP7_DetectAmbiguity_SpecificTaskPasses(t *testing.T) {
    qs := detectAmbiguity("rename variable x to y in main.go line 42")
    if len(qs) != 0 {
        t.Fatalf("P7 FAIL: specific rename should NOT trigger clarification, got %d questions", len(qs))
    }
}

func TestP7_DetectAmbiguity_VagueVerb(t *testing.T) {
    qs := detectAmbiguity("improve the code")
    if len(qs) == 0 {
        t.Fatal("P7 FAIL: vague 'improve' should trigger clarification")
    }
}

func TestP7_DetectAmbiguity_VagueVerbWithScope(t *testing.T) {
    qs := detectAmbiguity("improve error handling in main.go")
    if len(qs) != 0 {
        t.Fatalf("P7 FAIL: scoped improvement should NOT trigger clarification, got %d questions", len(qs))
    }
}
