package job

import (
	"sync"
	"time"
)

// Scheduler is a tiny delayed-task runner (media group flush / delayed delete).
type Scheduler struct {
	mu   sync.Mutex
	jobs map[string]*time.Timer
}

func New() *Scheduler {
	return &Scheduler{jobs: make(map[string]*time.Timer)}
}

// Once schedules fn after delay. Replacing the same name cancels the previous timer.
func (s *Scheduler) Once(name string, delay time.Duration, fn func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.jobs[name]; ok {
		old.Stop()
		delete(s.jobs, name)
	}
	s.jobs[name] = time.AfterFunc(delay, func() {
		fn()
		s.mu.Lock()
		delete(s.jobs, name)
		s.mu.Unlock()
	})
}

func (s *Scheduler) Cancel(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.jobs[name]; ok {
		t.Stop()
		delete(s.jobs, name)
	}
}

func (s *Scheduler) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, t := range s.jobs {
		t.Stop()
		delete(s.jobs, k)
	}
}
