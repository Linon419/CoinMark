package hub

import (
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type Connection struct {
	ID         string
	Conn       *websocket.Conn
	CreatedMs  int64
	LastSeenMs int64
	Markets    map[string]struct{}
	Symbols    map[string]struct{}
	Types      map[string]struct{}
}

type Manager struct {
	mu             sync.RWMutex
	conns          map[string]*Connection
	maxConns       int
	heartbeatSec   int
	timeoutSec     int
}

func NewManager(maxConns, heartbeatSec, timeoutSec int) *Manager {
	if maxConns < 1 {
		maxConns = 1000
	}
	if heartbeatSec < 1 {
		heartbeatSec = 15
	}
	if timeoutSec < heartbeatSec {
		timeoutSec = heartbeatSec * 3
	}
	return &Manager{
		conns:        make(map[string]*Connection),
		maxConns:     maxConns,
		heartbeatSec: heartbeatSec,
		timeoutSec:   timeoutSec,
	}
}

func (m *Manager) Connect(id string, ws *websocket.Conn) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.conns) >= m.maxConns {
		return false
	}
	now := time.Now().UnixMilli()
	m.conns[id] = &Connection{
		ID: id, Conn: ws, CreatedMs: now, LastSeenMs: now,
		Markets: make(map[string]struct{}),
		Symbols: make(map[string]struct{}),
		Types:   make(map[string]struct{}),
	}
	return true
}

func (m *Manager) Disconnect(id string) {
	m.mu.Lock()
	c, ok := m.conns[id]
	if ok {
		delete(m.conns, id)
	}
	m.mu.Unlock()
	if ok && c.Conn != nil {
		c.Conn.Close()
	}
}

func (m *Manager) Touch(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.conns[id]; ok {
		c.LastSeenMs = time.Now().UnixMilli()
	}
}

func (m *Manager) UpdateSubscription(id string, markets, symbols, types []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.conns[id]
	if !ok {
		return
	}
	if markets != nil {
		c.Markets = toSet(markets)
	}
	if symbols != nil {
		c.Symbols = toSet(symbols)
	}
	if types != nil {
		c.Types = toSet(types)
	}
}

func (m *Manager) GetSubscription(id string) (markets, symbols, types []string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, ok := m.conns[id]
	if !ok {
		return
	}
	return setToSlice(c.Markets), setToSlice(c.Symbols), setToSlice(c.Types)
}

func (m *Manager) BroadcastEvent(evt HubEvent) int {
	data, err := json.Marshal(EventMsg{Kind: "event", Data: evt})
	if err != nil {
		return 0
	}
	m.mu.RLock()
	targets := make([]*Connection, 0, len(m.conns))
	for _, c := range m.conns {
		if matchesSubscription(c, evt) {
			targets = append(targets, c)
		}
	}
	m.mu.RUnlock()

	var stale []string
	sent := 0
	for _, c := range targets {
		c.Conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if err := c.Conn.WriteMessage(websocket.TextMessage, data); err != nil {
			stale = append(stale, c.ID)
		} else {
			sent++
		}
	}
	for _, id := range stale {
		m.Disconnect(id)
	}
	return sent
}

func (m *Manager) RunHeartbeat(stopCh <-chan struct{}) {
	ticker := time.NewTicker(time.Duration(m.heartbeatSec) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-stopCh:
			return
		case <-ticker.C:
			m.heartbeat()
		}
	}
}

func (m *Manager) heartbeat() {
	now := time.Now().UnixMilli()
	cutoff := now - int64(m.timeoutSec)*1000
	ping, _ := json.Marshal(PingMsg{Kind: "ping", Ts: now})

	m.mu.RLock()
	var active []*Connection
	var stale []string
	for _, c := range m.conns {
		if c.LastSeenMs < cutoff {
			stale = append(stale, c.ID)
		} else {
			active = append(active, c)
		}
	}
	m.mu.RUnlock()

	for _, id := range stale {
		m.Disconnect(id)
	}
	if len(stale) > 0 {
		log.Printf("hub: evicted %d stale connections", len(stale))
	}

	for _, c := range active {
		c.Conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		c.Conn.WriteMessage(websocket.TextMessage, ping)
	}
}

func (m *Manager) ConnectionCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.conns)
}

func matchesSubscription(c *Connection, evt HubEvent) bool {
	if len(c.Markets) > 0 && evt.Market != "" {
		if _, ok := c.Markets[evt.Market]; !ok {
			return false
		}
	}
	if len(c.Symbols) > 0 && evt.Symbol != "" {
		if _, ok := c.Symbols[evt.Symbol]; !ok {
			return false
		}
	}
	if len(c.Types) > 0 && evt.Type != "" {
		if _, ok := c.Types[evt.Type]; !ok {
			return false
		}
	}
	return true
}

func toSet(items []string) map[string]struct{} {
	s := make(map[string]struct{}, len(items))
	for _, v := range items {
		if v != "" {
			s[v] = struct{}{}
		}
	}
	return s
}

func setToSlice(s map[string]struct{}) []string {
	if len(s) == 0 {
		return nil
	}
	out := make([]string, 0, len(s))
	for k := range s {
		out = append(out, k)
	}
	return out
}
