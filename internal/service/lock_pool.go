package service

import "sync"

type keyedLockPool struct {
	mu    sync.Mutex
	locks map[int64]*keyedLock
}

type keyedLock struct {
	mu   sync.Mutex
	refs int
}

// Lock serializes work for key and returns an unlock function.
// Entries are removed once the final holder or waiter releases them.
func (p *keyedLockPool) Lock(key int64) func() {
	p.mu.Lock()
	if p.locks == nil {
		p.locks = make(map[int64]*keyedLock)
	}
	entry := p.locks[key]
	if entry == nil {
		entry = &keyedLock{}
		p.locks[key] = entry
	}
	entry.refs++
	p.mu.Unlock()

	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		p.mu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(p.locks, key)
		}
		p.mu.Unlock()
	}
}

func (p *keyedLockPool) Len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.locks)
}
