package identity

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"hash/maphash"
	"net/http"
	"time"

	"github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
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

const loginBucketsPerClass = 65536
const loginWindowSeconds int64 = 15 * 60

type loginReservation struct {
	bucket     uint32
	key        []byte
	until      int64
	generation []byte
}

// Existing and invented accounts occupy separate bounded spaces. A stored
// random key prevents clients from selecting another account's bucket; hash
// collisions conservatively share the window and cannot evict its owner.
func sharedLoginBucket(secret []byte, key string, known bool) (uint32, []byte) {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(key))
	hash := mac.Sum(nil)
	bucket := binary.BigEndian.Uint32(hash[:4]) % loginBucketsPerClass
	if !known {
		bucket += loginBucketsPerClass
	}
	return bucket, hash
}

func (s *Store) loginBudget(ctx context.Context, r *http.Request, input contracts.LoginRequest) (loginReservation, error) {
	var reservation loginReservation
	unavailable := func() (loginReservation, error) {
		return reservation, problem(503, "login_protection_unavailable", "暂时无法确认登录保护状态，请稍后重试")
	}
	if s.DB == nil {
		return unavailable()
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, nameKey, _ := normalizedName(input.Username)
	var known bool
	if err := s.DB.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM af_identity_users WHERE tenant_id=? AND username_key=?)", input.Tenant, nameKey).Scan(&known); err != nil {
		return unavailable()
	}
	var secret []byte
	if err := s.DB.QueryRowContext(ctx, "SELECT secret FROM af_identity_login_budget_key WHERE id=1").Scan(&secret); err != nil || len(secret) != 32 {
		return unavailable()
	}
	reservation.bucket, reservation.key = sharedLoginBucket(secret, loginKey(r, input), known)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return unavailable()
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "INSERT INTO af_identity_login_budgets (bucket_id,key_hash,attempt_count,expires_at) VALUES (?,?,0,0) ON DUPLICATE KEY UPDATE bucket_id=bucket_id", reservation.bucket, reservation.key); err != nil {
		return unavailable()
	}
	var count int
	var now int64
	var owner []byte
	if err := tx.QueryRowContext(ctx, "SELECT attempt_count,expires_at,key_hash,generation,UNIX_TIMESTAMP() FROM af_identity_login_budgets WHERE bucket_id=? FOR UPDATE", reservation.bucket).Scan(&count, &reservation.until, &owner, &reservation.generation, &now); err != nil {
		return unavailable()
	}
	if reservation.until <= now {
		count, reservation.until, owner = 0, now+loginWindowSeconds, reservation.key
		reservation.generation = make([]byte, 16)
		if _, err := rand.Read(reservation.generation); err != nil {
			return unavailable()
		}
	}
	if count >= 5 {
		return reservation, problem(429, "login_rate_limited", "登录尝试过于频繁，请15分钟后重试")
	}
	if _, err := tx.ExecContext(ctx, "UPDATE af_identity_login_budgets SET attempt_count=?,expires_at=?,key_hash=?,generation=? WHERE bucket_id=?", count+1, reservation.until, owner, reservation.generation, reservation.bucket); err != nil {
		return unavailable()
	}
	if err := tx.Commit(); err != nil {
		return unavailable()
	}
	return reservation, nil
}

// Successful or unprocessed requests release only their own reservation. They
// never clear preceding failures, a newer window, or a colliding bucket owner.
func (s *Store) releaseLoginBudget(ctx context.Context, reservation loginReservation) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if s.DB == nil {
		return problem(503, "login_protection_unavailable", "暂时无法确认登录保护状态，请稍后重试")
	}
	_, err := s.DB.ExecContext(ctx, "UPDATE af_identity_login_budgets SET attempt_count=GREATEST(attempt_count,1)-1 WHERE bucket_id=? AND key_hash=? AND expires_at=? AND generation=?", reservation.bucket, reservation.key, reservation.until, reservation.generation)
	if err != nil {
		return problem(503, "login_protection_unavailable", "暂时无法确认登录保护状态，请稍后重试")
	}
	return nil
}

func loginFailure(err error) bool {
	var access *platform.AccessError
	return errors.As(err, &access) && access.Status == http.StatusUnauthorized
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
