package verification

type Policy struct {
	MinEditedFiles  int
	RequireForAPI   bool
	RequireForInfra bool
}

func DefaultPolicy() Policy {
	return Policy{
		MinEditedFiles:  3,
		RequireForAPI:   true,
		RequireForInfra: true,
	}
}

func RequireIndependentVerification(policy Policy, changedFiles []string, touchesAPI bool, touchesInfra bool) bool {
	if len(changedFiles) >= policy.MinEditedFiles {
		return true
	}
	if policy.RequireForAPI && touchesAPI {
		return true
	}
	if policy.RequireForInfra && touchesInfra {
		return true
	}
	return false
}
