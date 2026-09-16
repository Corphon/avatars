// Package health is the Engine-facing wrapper around verification.HealthService
// (B5 / F48). Compile/test checkers stay in runtime to avoid import cycles.
package health

import "avatars/internal/verification"

// Service memoizes health execs for one Engine run.
type Service struct {
	*verification.HealthService
}

func New() *Service {
	return &Service{HealthService: verification.NewHealthService()}
}

// Cache returns the underlying memo table for verification.Runner.WithCache
// and verification.SetCurrentHealth. Nil-safe.
func (s *Service) Cache() *verification.HealthService {
	if s == nil {
		return nil
	}
	return s.HealthService
}
