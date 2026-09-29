package application

import "context"

func (s *Service) Cleanup(ctx context.Context) error { return s.maintenance.Cleanup(ctx) }
