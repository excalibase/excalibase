package sites

import "time"

// SetNow fixes the service's clock for a test.
func (s *Service) SetNow(now func() time.Time) { s.now = now }
