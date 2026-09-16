package skills

import (
	"errors"
	"sort"
	"strings"
	"time"

	"avatars/internal/skillbuilder"
)

type CompositeStore struct {
	primary *Store
	overlay []Definition
}

func NewCompositeStore(primary *Store, overlay []Definition) *CompositeStore {
	return &CompositeStore{primary: primary, overlay: normalizeOverlayDefinitions(overlay)}
}

func (s *CompositeStore) Generate(runID string, taskID string, proposal skillbuilder.Proposal) (string, error) {
	if s == nil || s.primary == nil {
		return "", errors.New("primary skills store is nil")
	}
	return s.primary.Generate(runID, taskID, proposal)
}

func (s *CompositeStore) PrepareGenerated(runID string, taskID string, proposal skillbuilder.Proposal) (PreparedGeneratedSkill, error) {
	if s == nil || s.primary == nil {
		return PreparedGeneratedSkill{}, errors.New("primary skills store is nil")
	}
	return s.primary.PrepareGenerated(runID, taskID, proposal)
}

func (s *CompositeStore) FinalizeGenerated(path string, generatedAt time.Time) error {
	if s == nil || s.primary == nil {
		return errors.New("primary skills store is nil")
	}
	return s.primary.FinalizeGenerated(path, generatedAt)
}

func (s *CompositeStore) ReviewGenerated(generatedPath string) (CandidateReview, error) {
	if s == nil || s.primary == nil {
		return CandidateReview{}, errors.New("primary skills store is nil")
	}
	return s.primary.ReviewGenerated(generatedPath)
}

func (s *CompositeStore) Approve(candidatePath string) (string, error) {
	if s == nil || s.primary == nil {
		return "", errors.New("primary skills store is nil")
	}
	return s.primary.Approve(candidatePath)
}

func (s *CompositeStore) ListApproved() ([]Listing, error) {
	if s == nil || s.primary == nil {
		return nil, errors.New("primary skills store is nil")
	}
	listings, err := s.primary.ListApproved()
	if err != nil {
		return nil, err
	}
	return appendOverlayListings(listings, s.overlay), nil
}

func (s *CompositeStore) ListApprovedTolerant() (ListingScan, error) {
	if s == nil || s.primary == nil {
		return ListingScan{}, errors.New("primary skills store is nil")
	}
	scan, err := s.primary.ListApprovedTolerant()
	if err != nil {
		return ListingScan{}, err
	}
	scan.Listings = appendOverlayListings(scan.Listings, s.overlay)
	return scan, nil
}

func (s *CompositeStore) RegenerateNavigator() error {
	if s == nil || s.primary == nil {
		return errors.New("primary skills store is nil")
	}
	return s.primary.RegenerateNavigator()
}

func (s *CompositeStore) CleanupStaleGenerated(maxAgeDays int) (int, error) {
	if s == nil || s.primary == nil {
		return 0, errors.New("primary skills store is nil")
	}
	return s.primary.CleanupStaleGenerated(maxAgeDays)
}

// RecordSkillUse delegates skill performance tracking to the primary store.
func (s *CompositeStore) RecordSkillUse(skillName string, skillPath string, taskID string, success bool) error {
	if s == nil || s.primary == nil {
		return errors.New("primary skills store is nil")
	}
	return s.primary.RecordSkillUse(skillName, skillPath, taskID, success)
}

func (s *CompositeStore) ShouldAutoApprove(skillName string) bool {
	if s == nil || s.primary == nil {
		return false
	}
	return s.primary.ShouldAutoApprove(skillName)
}

func (s *CompositeStore) GovernanceLedger() (GovernanceLedger, error) {
	if s == nil || s.primary == nil {
		return GovernanceLedger{}, errors.New("primary skills store is nil")
	}
	return s.primary.GovernanceLedger()
}

func (s *CompositeStore) LoadApproved(path string) (Definition, error) {
	if s == nil || s.primary == nil {
		return Definition{}, errors.New("primary skills store is nil")
	}
	for _, definition := range s.overlay {
		if sameSkillPath(definition.Path, path) {
			return definition, nil
		}
	}
	return s.primary.LoadApproved(path)
}

func normalizeOverlayDefinitions(definitions []Definition) []Definition {
	normalized := make([]Definition, 0, len(definitions))
	for _, definition := range definitions {
		if strings.TrimSpace(definition.Name) == "" || strings.TrimSpace(definition.Path) == "" {
			continue
		}
		if definition.LifecycleState == "" {
			definition.LifecycleState = "plugin"
		}
		normalized = append(normalized, definition)
	}
	sort.Slice(normalized, func(i int, j int) bool {
		if normalized[i].Name != normalized[j].Name {
			return normalized[i].Name < normalized[j].Name
		}
		return normalized[i].Path < normalized[j].Path
	})
	return normalized
}

func appendOverlayListings(listings []Listing, overlay []Definition) []Listing {
	merged := append([]Listing{}, listings...)
	for _, definition := range overlay {
		merged = append(merged, definition.Listing)
	}
	sort.Slice(merged, func(i int, j int) bool {
		if merged[i].Name != merged[j].Name {
			return merged[i].Name < merged[j].Name
		}
		return merged[i].Path < merged[j].Path
	})
	return merged
}

func sameSkillPath(left string, right string) bool {
	return strings.EqualFold(strings.TrimSpace(left), strings.TrimSpace(right))
}
