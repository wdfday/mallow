package repo

import (
	"fmt"
	"sync"

	"orchestrator/internal/module/worker/domain"
)

// MemoryStore is a thread-safe in-memory implementation of BotRepo.
type MemoryStore struct {
	mu     sync.RWMutex
	bots   map[string]*domain.BotInstance
	nextID int
}

// NewMemoryStore creates an empty in-memory store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{bots: make(map[string]*domain.BotInstance)}
}

func (s *MemoryStore) GenerateID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	return fmt.Sprintf("bot_%d", s.nextID)
}

func (s *MemoryStore) Save(instance *domain.BotInstance) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bots[instance.ID] = instance
	return nil
}

func (s *MemoryStore) Get(id string) (*domain.BotInstance, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	bi, ok := s.bots[id]
	if !ok {
		return nil, fmt.Errorf("bot %q not found", id)
	}
	return bi, nil
}

func (s *MemoryStore) All() []*domain.BotInstance {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*domain.BotInstance, 0, len(s.bots))
	for _, bi := range s.bots {
		out = append(out, bi)
	}
	return out
}

func (s *MemoryStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.bots[id]; !ok {
		return fmt.Errorf("bot %q not found", id)
	}
	delete(s.bots, id)
	return nil
}

func (s *MemoryStore) Update(id string, fn func(*domain.BotInstance) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	bi, ok := s.bots[id]
	if !ok {
		return fmt.Errorf("bot %q not found", id)
	}
	return fn(bi)
}
