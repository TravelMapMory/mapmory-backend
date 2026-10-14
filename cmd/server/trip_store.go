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

// get retrieves a trip by its ID without exposing the store's backing slice.
func (s *memoryTripStore) get(id string) (trip, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, t := range s.trips {
		if t.ID == id {
			return t, true
		}
	}
	return trip{}, false
}
