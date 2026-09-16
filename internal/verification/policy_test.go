// Verified: complies with `skills.md` test_file_requirements — no network listeners or external process startup; uses mocks/locals only.
package verification

import "testing"

func TestRequireIndependentVerification_DefaultRules(t *testing.T) {
	policy := DefaultPolicy()

	tests := []struct {
		name         string
		changedFiles []string
		touchesAPI   bool
		touchesInfra bool
		want         bool
	}{
		{name: "below threshold without api or infra", changedFiles: []string{"a.go", "b.go"}, want: false},
		{name: "reaches threshold", changedFiles: []string{"a.go", "b.go", "c.go"}, want: true},
		{name: "api change", changedFiles: []string{"a.go"}, touchesAPI: true, want: true},
		{name: "infra change", changedFiles: []string{"a.go"}, touchesInfra: true, want: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := RequireIndependentVerification(policy, test.changedFiles, test.touchesAPI, test.touchesInfra)
			if got != test.want {
				t.Fatalf("expected %t, got %t", test.want, got)
			}
		})
	}
}
