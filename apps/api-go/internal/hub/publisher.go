package hub

import (
	"sync"
	"time"
)

type Publisher struct {
	mgr            *Manager
	dedupeWindowMs int64
	maxPerSec      int

	mu      sync.Mutex
	seen    map[string]int64 // dedupeKey → expiryMs
	buckets map[int64]int    // secondTs → count
}

func NewPublisher(mgr *Manager, dedupeWindowSec, maxPerSec int) *Publisher {
	if dedupeWindowSec < 1 {
		dedupeWindowSec = 60
	}
	if maxPerSec < 1 {
		maxPerSec = 200
	}
	return &Publisher{
		mgr:            mgr,
		dedupeWindowMs: int64(dedupeWindowSec) * 1000,
		maxPerSec:      maxPerSec,
		seen:           make(map[string]int64),
		buckets:        make(map[int64]int),
	}
}

func (p *Publisher) Publish(evt HubEvent) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	nowMs := time.Now().UnixMilli()

	// clean expired dedupe entries
	for k, expiry := range p.seen {
		if expiry < nowMs {
			delete(p.seen, k)
		}
	}
	// clean old rate buckets (keep last 3 seconds)
	secNow := nowMs / 1000
	for s := range p.buckets {
		if s < secNow-2 {
			delete(p.buckets, s)
		}
	}

	// dedupe check
	key := evt.DedupeKey
	if key == "" {
		key = evt.Type + ":" + evt.Market + ":" + evt.Symbol + ":" + evt.Title
	}
	if _, dup := p.seen[key]; dup {
		return false
	}

	// rate limit check
	if p.buckets[secNow] >= p.maxPerSec {
		return false
	}

	p.seen[key] = nowMs + p.dedupeWindowMs
	p.buckets[secNow]++

	go p.mgr.BroadcastEvent(evt)
	return true
}
