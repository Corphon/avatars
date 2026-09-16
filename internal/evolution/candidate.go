package evolution

import (
	"fmt"
	"sort"
	"strings"

	memstore "avatars/internal/memory"
)

type Candidate struct {
	Kind      string
	Summary   string
	Source    string
	Priority  string
	Status    string
	SkillName string
}

type ProjectLesson struct {
	SourceTaskID string
	Kind         string
	Summary      string
	Source       string
	Confidence   string
}

func BuildCandidates(taskSummary string, finalSummary string, snapshot memstore.Snapshot, skillSuccessRates map[string]float64) []Candidate {
	candidates := make([]Candidate, 0, 4)
	seen := make(map[string]struct{}, 4)
	appendCandidate := func(candidate Candidate) {
		candidate.Kind = strings.TrimSpace(candidate.Kind)
		candidate.Summary = strings.TrimSpace(candidate.Summary)
		if candidate.Kind == "" || candidate.Summary == "" {
			return
		}
		if candidate.Source == "" {
			candidate.Source = "runtime"
		}
		if candidate.Priority == "" {
			candidate.Priority = "medium"
		}
		if candidate.Status == "" {
			candidate.Status = "open"
		}
		key := candidate.Kind + "\n" + candidate.Summary
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		candidates = append(candidates, candidate)
	}
	for _, candidate := range buildCandidatesFromEvaluation(snapshot.EvaluationRecords) {
		appendCandidate(candidate)
	}
	if snapshot.Verification != nil {
		tool := strings.TrimSpace(snapshot.Verification.Tool)
		verdict := strings.ToUpper(strings.TrimSpace(snapshot.Verification.Verdict))
		if tool != "" && verdict != "" && verdict != "PASS" {
			appendCandidate(Candidate{
				Kind:     "runtime_followup",
				Summary:  fmt.Sprintf("Add a deterministic follow-up path for %s when verifier verdict is %s before marking the task complete.", tool, verdict),
				Source:   "verification",
				Priority: "high",
				Status:   "open",
			})
		}
	}
	if len(snapshot.WarmLessons) > 0 {
		lesson := snapshot.WarmLessons[0]
		if trimmedLesson := strings.TrimSpace(lesson.Summary); trimmedLesson != "" {
			appendCandidate(Candidate{
				Kind:     "workflow_pattern",
				Summary:  fmt.Sprintf("Promote this lesson into a reusable workflow pattern: %s", trimmedLesson),
				Source:   "warm_lesson",
				Priority: priorityFromConfidence(lesson.Confidence),
				Status:   "open",
			})
		}
	}
	if trimmedTaskSummary := strings.TrimSpace(taskSummary); trimmedTaskSummary != "" {
		appendCandidate(Candidate{
			Kind:     "task_pattern",
			Summary:  fmt.Sprintf("Review whether this task decomposition should become a reusable planner pattern: %s", trimmedTaskSummary),
			Source:   "task_summary",
			Priority: "medium",
			Status:   "open",
		})
	} else if trimmedFinalSummary := strings.TrimSpace(finalSummary); trimmedFinalSummary != "" {
		appendCandidate(Candidate{
			Kind:     "outcome_signal",
			Summary:  fmt.Sprintf("Keep this run outcome available for later evolution review: %s", trimmedFinalSummary),
			Source:   "run_summary",
			Priority: "low",
			Status:   "open",
		})
	}
	// Sort by skill success rate (higher first). Candidates with a known skill
	// name get their success rate from the skillSuccessRates map; those with no
	// skill data or skill name not found in the map go last. The original
	// insertion order is preserved as a stable secondary sort.
	if len(skillSuccessRates) > 0 {
		sort.SliceStable(candidates, func(i, j int) bool {
			iRate, iHasRate := skillSuccessRates[candidates[i].SkillName]
			jRate, jHasRate := skillSuccessRates[candidates[j].SkillName]
			// Both have rates: higher first.
			if iHasRate && jHasRate {
				return iRate > jRate
			}
			// Only i has a rate: i comes first.
			if iHasRate {
				return true
			}
			// Only j has a rate or neither has a rate: keep insertion order.
			return false
		})
	}
	return candidates
}

func buildCandidatesFromEvaluation(records []memstore.EvaluationRecord) []Candidate {
	if len(records) == 0 {
		return nil
	}
	candidates := make([]Candidate, 0, len(records))
	for _, record := range records {
		switch strings.ToLower(strings.TrimSpace(record.Kind)) {
		case "verifier_verdict":
			if candidate, ok := candidateFromEvaluationVerdict(record); ok {
				candidates = append(candidates, candidate)
			}
		case "passive_feedback":
			if candidate, ok := candidateFromPassiveFeedback(record); ok {
				candidates = append(candidates, candidate)
			}
		case "skill_candidate":
			if candidate, ok := candidateFromSkillCandidate(record); ok {
				candidates = append(candidates, candidate)
			}
		}
	}
	return candidates
}

func candidateFromEvaluationVerdict(record memstore.EvaluationRecord) (Candidate, bool) {
	verdict := strings.ToUpper(strings.TrimSpace(record.Verdict))
	if verdict == "" || verdict == "PASS" {
		return Candidate{}, false
	}
	tool := strings.TrimSpace(record.Tool)
	summary := strings.TrimSpace(record.Summary)
	if tool != "" {
		summary = fmt.Sprintf("Add a deterministic follow-up path for %s when verifier verdict is %s before marking the task complete.", tool, verdict)
	} else if summary != "" {
		summary = fmt.Sprintf("Review the verifier outcome and keep a deterministic follow-up path before marking the task complete: %s", summary)
	}
	if summary == "" {
		return Candidate{}, false
	}
	return Candidate{
		Kind:     "runtime_followup",
		Summary:  summary,
		Source:   "evaluation_record",
		Priority: priorityFromVerdict(verdict),
		Status:   "open",
	}, true
}

func candidateFromPassiveFeedback(record memstore.EvaluationRecord) (Candidate, bool) {
	feedback := strings.TrimSpace(record.Summary)
	if feedback == "" {
		return Candidate{}, false
	}
	tool := strings.TrimSpace(record.Tool)
	summary := fmt.Sprintf("Capture and address passive feedback before repeating the verifier path: %s", feedback)
	if tool != "" {
		summary = fmt.Sprintf("Capture and address passive feedback for %s before repeating the verifier path: %s", tool, feedback)
	}
	return Candidate{
		Kind:     "passive_feedback_followup",
		Summary:  summary,
		Source:   "evaluation_record",
		Priority: priorityFromVerdict(record.Verdict),
		Status:   "open",
	}, true
}

func candidateFromSkillCandidate(record memstore.EvaluationRecord) (Candidate, bool) {
	name := strings.TrimSpace(detailValue(record.Details, "skill_name"))
	if name == "" {
		name = strings.TrimSpace(record.Summary)
	}
	if name == "" {
		return Candidate{}, false
	}
	state := strings.ToLower(strings.TrimSpace(detailValue(record.Details, "skill_state")))
	if state == "" {
		state = "candidate_prepared"
	}
	summary := fmt.Sprintf("Promote reusable skill candidate %s into the skill promotion queue.", name)
	if state == "generated" {
		summary = fmt.Sprintf("Promote generated skill %s into the skill promotion queue.", name)
	}
	return Candidate{
		Kind:      "skill_promotion",
		Summary:   summary,
		Source:    "skill_candidate",
		Priority:  priorityFromSkillState(state),
		Status:    "open",
		SkillName: name,
	}, true
}

func priorityFromConfidence(confidence string) string {
	switch strings.ToLower(strings.TrimSpace(confidence)) {
	case "high":
		return "high"
	case "low":
		return "low"
	default:
		return "medium"
	}
}

func priorityFromVerdict(verdict string) string {
	switch strings.ToUpper(strings.TrimSpace(verdict)) {
	case "FAIL":
		return "high"
	case "PARTIAL":
		return "medium"
	default:
		return "low"
	}
}

func priorityFromSkillState(state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "generated":
		return "high"
	case "candidate_prepared":
		return "medium"
	default:
		return "low"
	}
}

func detailValue(details []string, key string) string {
	prefix := strings.ToLower(strings.TrimSpace(key)) + ":"
	for _, detail := range details {
		trimmed := strings.TrimSpace(detail)
		if strings.HasPrefix(strings.ToLower(trimmed), prefix) {
			return strings.TrimSpace(trimmed[len(prefix):])
		}
	}
	return ""
}

func BuildProjectLessons(sourceTaskID string, snapshot memstore.Snapshot) []ProjectLesson {
	lessons := make([]ProjectLesson, 0, 2)
	for _, candidate := range snapshot.EvolutionCandidates {
		if strings.TrimSpace(candidate.Summary) == "" || strings.ToLower(strings.TrimSpace(candidate.Priority)) != "high" {
			continue
		}
		lessons = append(lessons, ProjectLesson{
			SourceTaskID: sourceTaskID,
			Kind:         candidate.Kind,
			Summary:      candidate.Summary,
			Source:       "evolution_candidate",
			Confidence:   "high",
		})
	}
	for _, lesson := range snapshot.WarmLessons {
		if strings.TrimSpace(lesson.Summary) == "" || strings.ToLower(strings.TrimSpace(lesson.Confidence)) != "high" {
			continue
		}
		lessons = append(lessons, ProjectLesson{
			SourceTaskID: sourceTaskID,
			Kind:         lesson.Kind,
			Summary:      lesson.Summary,
			Source:       "warm_lesson",
			Confidence:   lesson.Confidence,
		})
	}
	return lessons
}
