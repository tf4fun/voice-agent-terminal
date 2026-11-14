package gateway

import "context"

// A lifecycle entry exists only while a holder or waiter references its session.
type sessionLifecycleLock struct {
	held       chan struct{}
	references int
}

func (s *Service) lockSession(ctx context.Context, id string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.lifecycleMu.Lock()
	entry := s.lifecycles[id]
	if entry == nil {
		entry = &sessionLifecycleLock{held: make(chan struct{}, 1)}
		if s.lifecycles == nil {
			s.lifecycles = make(map[string]*sessionLifecycleLock)
		}
		s.lifecycles[id] = entry
	}
	entry.references++
	s.lifecycleMu.Unlock()
	select {
	case entry.held <- struct{}{}:
		unlock := func() {
			<-entry.held
			s.releaseSessionReference(id, entry)
		}
		if err := ctx.Err(); err != nil {
			unlock()
			return nil, err
		}
		return unlock, nil
	case <-ctx.Done():
		s.releaseSessionReference(id, entry)
		return nil, ctx.Err()
	}
}

func (s *Service) releaseSessionReference(id string, entry *sessionLifecycleLock) {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	entry.references--
	if entry.references == 0 {
		delete(s.lifecycles, id)
	}
}
