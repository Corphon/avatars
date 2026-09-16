package runtime

// maxCriticBuilderCycles caps Critic→Builder dispatches per run.
// NL smoke #8/#17: without this, Builder↔Critic loops for 5+ minutes.
const maxCriticBuilderCycles = 3

// beginCriticBuilderCycle increments the run-level Critic→Builder counter.
// Returns false when the hard cap is reached.
func (e *Engine) beginCriticBuilderCycle(reason string) bool {
	if e == nil {
		return false
	}
	if e.criticBuilderCycles >= maxCriticBuilderCycles {
		return false
	}
	e.criticBuilderCycles++
	return true
}

func (e *Engine) criticBuilderCyclesExhausted() bool {
	return e != nil && e.criticBuilderCycles >= maxCriticBuilderCycles
}

// noteQualityFailure records a staged-gate / health failure fingerprint and
// returns how many consecutive times this same cause has been seen (R11-3).
func (e *Engine) noteQualityFailure(errMsg string) int {
	if e == nil {
		return 0
	}
	fp := healthFailureFingerprint(errMsg)
	if fp == "" {
		return 0
	}
	if e.lastQualityFailFingerprint == fp {
		e.qualityFailRepeatCount++
	} else {
		e.lastQualityFailFingerprint = fp
		e.qualityFailRepeatCount = 1
	}
	return e.qualityFailRepeatCount
}

// sameQualityFailureRepeated is true when the current error matches the last
// recorded fingerprint at least twice (R11-3).
func (e *Engine) sameQualityFailureRepeated(errMsg string) bool {
	if e == nil {
		return false
	}
	fp := healthFailureFingerprint(errMsg)
	return fp != "" && e.lastQualityFailFingerprint == fp && e.qualityFailRepeatCount >= 2
}
