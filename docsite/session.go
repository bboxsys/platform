package docsite

import (
	"crypto/rand"
	"encoding/base64"
	"sort"
	"sync"
	"time"
)

// Identity is who a documentation session belongs to.
type Identity struct {
	AccountID string
	// UserID is the user's NATS public key (the token subject).
	UserID string
	Roles  []string
	// Expires is when the session ends; the token's expiry, bounded by
	// HandlerOptions.MaxAge.
	Expires time.Time
}

// sessions is the handler's in-memory session table: a random id in the
// cookie maps to the identity the bearer proved. Nothing secret is stored
// in the browser, a restart only sends readers back through the gate, and
// the table is bounded.
type sessions struct {
	mu   sync.Mutex
	byID map[string]*Identity
	max  int
	now  func() time.Time
}

func newSessions(max int, now func() time.Time) *sessions {
	if max <= 0 {
		max = DefaultMaxSessions
	}
	if now == nil {
		now = time.Now
	}
	return &sessions{byID: map[string]*Identity{}, max: max, now: now}
}

// add stores an identity and returns its id.
func (s *sessions) add(id *Identity) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	key := base64.RawURLEncoding.EncodeToString(raw)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeLocked()
	s.byID[key] = id
	return key, nil
}

// get returns a live identity, or nil.
func (s *sessions) get(key string) *Identity {
	if key == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.byID[key]
	if id == nil {
		return nil
	}
	if !s.now().Before(id.Expires) {
		delete(s.byID, key)
		return nil
	}
	return id
}

func (s *sessions) remove(key string) {
	s.mu.Lock()
	delete(s.byID, key)
	s.mu.Unlock()
}

// purgeLocked drops expired sessions and, past the bound, the ones that
// expire soonest.
func (s *sessions) purgeLocked() {
	now := s.now()
	for k, id := range s.byID {
		if !now.Before(id.Expires) {
			delete(s.byID, k)
		}
	}
	if len(s.byID) < s.max {
		return
	}
	type entry struct {
		key string
		exp time.Time
	}
	all := make([]entry, 0, len(s.byID))
	for k, id := range s.byID {
		all = append(all, entry{k, id.Expires})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].exp.Before(all[j].exp) })
	for _, e := range all[:len(all)-s.max+1] {
		delete(s.byID, e.key)
	}
}
