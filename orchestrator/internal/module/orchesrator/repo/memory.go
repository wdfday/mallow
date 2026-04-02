package repo

import (
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"orchestrator/internal/module/orchesrator/domain"
)

// MemoryOrchestratorStore is a thread-safe in-memory implementation of OrchestratorRepo.
// Used when POSTGRES_DSN is not set (dev / test).
type MemoryOrchestratorStore struct {
	mu   sync.RWMutex
	data map[uuid.UUID]*domain.OrchestratorConfig
}

// NewMemoryOrchestratorStore creates an empty in-memory store.
func NewMemoryOrchestratorStore() *MemoryOrchestratorStore {
	return &MemoryOrchestratorStore{data: make(map[uuid.UUID]*domain.OrchestratorConfig)}
}

func (s *MemoryOrchestratorStore) Save(o *domain.OrchestratorConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *o
	s.data[o.ID] = &cp
	return nil
}

func (s *MemoryOrchestratorStore) Get(id uuid.UUID) (*domain.OrchestratorConfig, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	o, ok := s.data[id]
	if !ok {
		return nil, fmt.Errorf("orchestrator %q not found", id)
	}
	cp := *o
	return &cp, nil
}

func (s *MemoryOrchestratorStore) GetByAccountID(accountID uuid.UUID) (*domain.OrchestratorConfig, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, o := range s.data {
		if o.AccountID == accountID {
			cp := *o
			return &cp, nil
		}
	}
	return nil, fmt.Errorf("orchestrator for account %q not found", accountID)
}

func (s *MemoryOrchestratorStore) AllByUser(userID uuid.UUID) ([]*domain.OrchestratorConfig, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*domain.OrchestratorConfig
	for _, o := range s.data {
		if o.UserID == userID {
			cp := *o
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (s *MemoryOrchestratorStore) All() ([]*domain.OrchestratorConfig, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*domain.OrchestratorConfig, 0, len(s.data))
	for _, o := range s.data {
		cp := *o
		out = append(out, &cp)
	}
	return out, nil
}

func (s *MemoryOrchestratorStore) Update(id uuid.UUID, fn func(*domain.OrchestratorConfig) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.data[id]
	if !ok {
		return fmt.Errorf("orchestrator %q not found", id)
	}
	cp := *o
	if err := fn(&cp); err != nil {
		return err
	}
	cp.UpdatedAt = time.Now().UTC()
	s.data[id] = &cp
	return nil
}

func (s *MemoryOrchestratorStore) UpdateLastSyncedAt(id uuid.UUID, t time.Time) error {
	return s.Update(id, func(o *domain.OrchestratorConfig) error {
		if o.LastSyncedAt == nil || t.After(*o.LastSyncedAt) {
			o.LastSyncedAt = &t
		}
		return nil
	})
}

func (s *MemoryOrchestratorStore) Delete(id uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data[id]; !ok {
		return fmt.Errorf("orchestrator %q not found", id)
	}
	delete(s.data, id)
	return nil
}
