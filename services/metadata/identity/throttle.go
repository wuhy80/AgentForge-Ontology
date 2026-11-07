package identity

import (
	"net"
	"net/http"
	"time"

	"github.com/wuhy80/AgentForge-Ontology/contracts"
)

type loginAttempt struct {
	count int
	until time.Time
}

func loginKey(r *http.Request, input contracts.LoginRequest) string {
	address := r.RemoteAddr
	if host, _, err := net.SplitHostPort(address); err == nil {
		address = host
	}
	_, key, _ := normalizedName(input.Username)
	return tokenHash(input.Tenant + "\x00" + string(key) + "\x00" + address)
}

// This local abuse guard has a strict memory budget. A full live table refuses
// new login attempts instead of silently evicting active failure counters.
func (s *Store) loginBudget(key string) error {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	now := time.Now()
	if s.loginAttempts == nil {
		s.loginAttempts = map[string]loginAttempt{}
	}
	for id, attempt := range s.loginAttempts {
		if !attempt.until.After(now) {
			delete(s.loginAttempts, id)
		}
	}
	attempt, exists := s.loginAttempts[key]
	if !exists && len(s.loginAttempts) >= 1024 || attempt.count >= 5 {
		return problem(429, "login_rate_limited", "登录尝试过于频繁，请15分钟后重试")
	}
	if !exists {
		attempt.until = now.Add(15 * time.Minute)
	}
	attempt.count++
	s.loginAttempts[key] = attempt
	return nil
}

func (s *Store) resetLoginBudget(key string) {
	s.loginMu.Lock()
	delete(s.loginAttempts, key)
	s.loginMu.Unlock()
}
