package oidctoken

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// defaultExpirationSkew is how long before expiry a cached token is renewed,
// so a token handed out is still valid for the call it is used in.
const defaultExpirationSkew = 60 * time.Second

type cachedToken struct {
	token     Token
	refreshAt time.Time
}

type mintCall struct {
	done  chan struct{}
	token Token
	err   error
}

// Manager caches minted tokens per source and renews them shortly before
// they expire. Concurrent requests for the same source share one mint. It is
// safe for concurrent use and sized for a process: sources that are never
// requested again stay cached until the process exits.
type Manager struct {
	mu       sync.Mutex
	cache    map[[32]byte]cachedToken
	inflight map[[32]byte]*mintCall
	now      func() time.Time
	mint     func(context.Context, Source) (Token, error)
}

// NewManager returns an empty Manager.
func NewManager() *Manager {
	return &Manager{
		cache:    make(map[[32]byte]cachedToken),
		inflight: make(map[[32]byte]*mintCall),
		now:      time.Now,
		mint:     Mint,
	}
}

// Token returns a valid token for the source, minting one when the cache has
// none or the cached one is about to expire.
func (m *Manager) Token(ctx context.Context, source Source) (Token, error) {
	key, err := sourceKey(source)
	if err != nil {
		return Token{}, err
	}
	now := m.now()
	m.mu.Lock()
	if cached, ok := m.cache[key]; ok && now.Before(cached.refreshAt) {
		m.mu.Unlock()
		return cached.token, nil
	}
	if call, ok := m.inflight[key]; ok {
		m.mu.Unlock()
		select {
		case <-ctx.Done():
			return Token{}, ctx.Err()
		case <-call.done:
			return call.token, call.err
		}
	}
	call := &mintCall{done: make(chan struct{})}
	m.inflight[key] = call
	m.mu.Unlock()

	call.token, call.err = m.mint(ctx, source)
	m.mu.Lock()
	if call.err == nil && !call.token.ExpiresAt.IsZero() {
		cached := cachedToken{token: call.token, refreshAt: refreshAt(m.now(), call.token.ExpiresAt)}
		m.cache[key] = cached
		// A provider may rotate the refresh token on every use. The caller
		// persists that replacement, so alias the cache entry under the
		// rotated source as well: the next request then finds the token it
		// just minted instead of minting again.
		if call.token.RefreshToken != "" && call.token.RefreshToken != source.RefreshToken {
			rotated := source
			rotated.RefreshToken = call.token.RefreshToken
			if rotatedKey, keyErr := sourceKey(rotated); keyErr == nil {
				m.cache[rotatedKey] = cached
			}
		}
	}
	delete(m.inflight, key)
	close(call.done)
	m.mu.Unlock()
	return call.token, call.err
}

// Forget drops any cached token for the source, forcing the next Token call
// to mint. Use it after the resource server rejects a token before expiry.
func (m *Manager) Forget(source Source) {
	key, err := sourceKey(source)
	if err != nil {
		return
	}
	m.mu.Lock()
	delete(m.cache, key)
	m.mu.Unlock()
}

// sourceKey hashes the whole source, secrets included, so two credentials
// with identical inputs share a token and any difference gets its own.
func sourceKey(source Source) ([32]byte, error) {
	encoded, err := json.Marshal(source)
	if err != nil {
		return [32]byte{}, fmt.Errorf("encode OIDC token source: %w", err)
	}
	return sha256.Sum256(encoded), nil
}

func refreshAt(now, expiresAt time.Time) time.Time {
	lifetime := expiresAt.Sub(now)
	if lifetime <= 0 {
		return now
	}
	skew := defaultExpirationSkew
	if lifetime <= 2*skew {
		skew = lifetime / 5
	}
	return expiresAt.Add(-skew)
}
