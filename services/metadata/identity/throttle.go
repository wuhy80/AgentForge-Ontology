package identity

import (
	"github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
	"hash/maphash"
	"net/http"
	"time"
)

type loginAttempt struct {
	count int
	until time.Time
	key   uint64
}

func loginKey(r *http.Request, input contracts.LoginRequest) string {
	_, key, _ := normalizedName(input.Username)
	return tokenHash(input.Tenant + "\x00" + string(key) + "\x00" + platform.ClientAddress(r))
}
func (s *Store) budgetHash(key string) uint64 {
	var hash maphash.Hash
	hash.SetSeed(s.abuseSeed)
	_, _ = hash.WriteString(key)
	return hash.Sum64()
}
func takeBudget(attempt *loginAttempt, key uint64, limit int, window time.Duration, now time.Time) bool {
	if !attempt.until.After(now) {
		*attempt = loginAttempt{until: now.Add(window), key: key}
	}
	if attempt.count >= limit {
		return false
	}
	attempt.count++
	return true
}

// Fixed randomly keyed buckets cannot be filled with invented account names.
// Collisions conservatively share a budget; they never allocate or evict state.
func (s *Store) loginBudget(key string) error {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	hash := s.budgetHash(key)
	if !takeBudget(&s.loginAttempts[hash%uint64(len(s.loginAttempts))], hash, 5, 15*time.Minute, time.Now()) {
		return problem(429, "login_rate_limited", "登录尝试过于频繁，请15分钟后重试")
	}
	return nil
}
func (s *Store) resetLoginBudget(key string) {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	hash := s.budgetHash(key)
	attempt := &s.loginAttempts[hash%uint64(len(s.loginAttempts))]
	if attempt.key == hash {
		*attempt = loginAttempt{}
	}
}

// Admission covers malformed requests before JSON/form decoding or bcrypt.
// Shared storage separately enforces registration capacity across replicas.
func (s *Store) publicRequest(w http.ResponseWriter, r *http.Request, route, perIP, total int) bool {
	s.loginMu.Lock()
	hash := s.budgetHash(platform.ClientAddress(r) + "\x00" + string(rune(route)))
	now := time.Now()
	allowed := takeBudget(&s.publicAttempts[hash%uint64(len(s.publicAttempts))], hash, perIP, time.Minute, now)
	if allowed {
		allowed = takeBudget(&s.publicTotals[route], 0, total, time.Minute, now)
	}
	s.loginMu.Unlock()
	if !allowed {
		w.Header().Set("Retry-After", "60")
		if route == 0 {
			platform.WriteError(w, 429, "login_rate_limited", "登录请求过于频繁，请稍后重试")
		} else {
			oauthError(w, 429, "slow_down")
		}
	}
	return allowed
}
