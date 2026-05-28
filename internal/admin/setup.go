package admin

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

// SetupManager issues and validates the one-time admin bootstrap token.
type SetupManager struct {
	mu        sync.Mutex
	token     string
	expiresAt time.Time
	consumed  bool
}

func NewSetupManager() *SetupManager { return &SetupManager{} }

// GenerateToken produces a cryptographically random 64-char hex token valid for 15 minutes.
func (s *SetupManager) GenerateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating setup token: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.token = hex.EncodeToString(b)
	s.expiresAt = time.Now().Add(15 * time.Minute)
	s.consumed = false
	return s.token, nil
}

// Consume validates token and marks it used. Returns false if invalid, expired, or already consumed.
func (s *SetupManager) Consume(token string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.consumed || s.token == "" || time.Now().After(s.expiresAt) || s.token != token {
		return false
	}
	s.consumed = true
	return true
}

// IsActive reports whether an unconsumed, unexpired token exists.
func (s *SetupManager) IsActive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.consumed && s.token != "" && time.Now().Before(s.expiresAt)
}
