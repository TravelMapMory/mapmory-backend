package main

import "sync"

type memoryTripStore struct {
	mu    sync.RWMutex
	trips []trip
}

func newMemoryTripStore() *memoryTripStore {
	return &memoryTripStore{
		trips: []trip{},
	}
}

func (s *memoryTripStore) add(t trip) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.trips = append(s.trips, t)
}

func (s *memoryTripStore) list() []trip {
	s.mu.RLock()
	defer s.mu.RUnlock()

	trips := make([]trip, len(s.trips))
	copy(trips, s.trips)

	return trips
}
