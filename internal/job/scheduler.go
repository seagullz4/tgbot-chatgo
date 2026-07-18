package job

import (
	"sync"
	"time"
)

type scheduledJob struct {
	timer      *time.Timer
	generation uint64
}

// Scheduler is a tiny delayed-task runner (media group flush / delayed delete).
type Scheduler struct {
	mu         sync.Mutex
	cond       *sync.Cond
	jobs       map[string]scheduledJob
	generation uint64
	running    int
	stopping   bool
}

func New() *Scheduler {
	scheduler := &Scheduler{jobs: make(map[string]scheduledJob)}
	scheduler.cond = sync.NewCond(&scheduler.mu)
	return scheduler
}

// Once schedules fn after delay. Replacing the same name cancels the previous timer.
func (s *Scheduler) Once(name string, delay time.Duration, fn func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopping {
		return
	}
	if old, ok := s.jobs[name]; ok {
		old.timer.Stop()
	}
	s.generation++
	generation := s.generation
	timer := time.AfterFunc(delay, func() {
		if !s.begin(name, generation) {
			return
		}
		defer s.end()
		fn()
	})
	s.jobs[name] = scheduledJob{timer: timer, generation: generation}
}

func (s *Scheduler) begin(name string, generation uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.jobs[name]
	if !ok || current.generation != generation || s.stopping {
		return false
	}
	delete(s.jobs, name)
	s.running++
	return true
}

func (s *Scheduler) end() {
	s.mu.Lock()
	s.running--
	if s.running == 0 {
		s.cond.Broadcast()
	}
	s.mu.Unlock()
}

func (s *Scheduler) finish(name string, generation uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if current, ok := s.jobs[name]; ok && current.generation == generation {
		delete(s.jobs, name)
	}
}

func (s *Scheduler) Cancel(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if current, ok := s.jobs[name]; ok {
		current.timer.Stop()
		delete(s.jobs, name)
	}
}

// Stop cancels pending jobs and waits for callbacks that already started.
// The scheduler remains reusable after Stop returns.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	s.stopping = true
	for name, current := range s.jobs {
		current.timer.Stop()
		delete(s.jobs, name)
	}
	for s.running > 0 {
		s.cond.Wait()
	}
	s.stopping = false
	s.mu.Unlock()
}
