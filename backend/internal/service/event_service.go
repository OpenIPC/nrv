package service

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/nvr/backend/internal/domain"
	"github.com/nvr/backend/internal/repository/postgres"
)

type EventService struct {
	repo *postgres.EventRepo
}

func NewEventService(repo *postgres.EventRepo) *EventService {
	return &EventService{repo: repo}
}

func (s *EventService) List(ctx context.Context, cameraID *uuid.UUID, objectClass string,
	from, to *time.Time, search string, page, pageSize int) ([]domain.DetectionEvent, int64, error) {
	return s.repo.List(ctx, cameraID, objectClass, from, to, search, page, pageSize)
}

func (s *EventService) Get(ctx context.Context, id uuid.UUID) (*domain.DetectionEvent, error) {
	return s.repo.GetByID(ctx, id)
}

// CrossingStats — счётчик пересечений линии за период, отдельно по направлениям.
func (s *EventService) CrossingStats(ctx context.Context, cameraID uuid.UUID,
	since time.Time) (forward, backward int, err error) {
	return s.repo.CrossingStats(ctx, cameraID, since)
}
