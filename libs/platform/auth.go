// Package platform provides transport and identity infrastructure.
package platform

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/wuhy80/AgentForge-Ontology/contracts"
)

type principalKey struct{}

func WithPrincipal(ctx context.Context, p contracts.Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

func Principal(r *http.Request) (contracts.Principal, bool) {
	p, ok := r.Context().Value(principalKey{}).(contracts.Principal)
	return p, ok && p.Subject != "" && p.TenantID != ""
}

// SignToken signs identity only. Roles and data grants come from OPA's current
// server-side policy data, never from a caller-supplied tenant or role header.
// This bootstrap format is payload.signature, not a JWT/OIDC implementation.
func SignToken(secret string, p contracts.Principal) (string, error) {
	if len(secret) < 32 {
		return "", errors.New("authentication secret must contain at least 32 bytes")
	}
	if err := validatePrincipal(p, time.Now()); err != nil {
		return "", err
	}
	b, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(b)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func VerifyToken(secret, token string) (contracts.Principal, error) {
	var p contracts.Principal
	if len(secret) < 32 || len(token) > 4096 {
		return p, errors.New("invalid identity token")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return p, errors.New("invalid identity token")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return p, errors.New("invalid identity token")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(parts[0]))
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return p, errors.New("invalid identity token")
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return p, errors.New("invalid identity token")
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return p, errors.New("invalid identity token")
	}
	if err := validatePrincipal(p, time.Now()); err != nil {
		return contracts.Principal{}, err
	}
	return p, nil
}

func validatePrincipal(p contracts.Principal, now time.Time) error {
	if strings.TrimSpace(p.Subject) == "" || strings.TrimSpace(p.TenantID) == "" ||
		len(p.Subject) > 200 || len(p.TenantID) > 100 || p.ExpiresAt <= now.Unix() ||
		p.ExpiresAt > now.Add(24*time.Hour).Unix() {
		return errors.New("identity requires subject, tenant and an expiry within 24 hours")
	}
	return nil
}

func Authenticate(secret string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, err := VerifyToken(secret, Bearer(r))
		if err != nil {
			WriteError(w, http.StatusUnauthorized, "unauthenticated", "有效的身份凭证是必需的")
			return
		}
		next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
	})
}

func Bearer(r *http.Request) string {
	value := r.Header.Get("Authorization")
	if !strings.HasPrefix(value, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(value, "Bearer ")
}

func EqualSecret(got, want string) bool {
	return len(want) >= 32 && hmac.Equal([]byte(got), []byte(want))
}

func WriteJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func WriteError(w http.ResponseWriter, status int, code, message string) {
	WriteJSON(w, status, contracts.Error{Code: code, Message: message})
}
