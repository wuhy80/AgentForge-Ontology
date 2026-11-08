package platform

import (
	"net/http"
	"strings"
)

// SessionCookie validates the raw headers before Go's cookie parser can discard
// malformed duplicates. The session protocol uses an unquoted opaque token.
// Missing cookies are permitted here so token-only protocols can reject mixed
// credentials without applying browser-session authentication.
func SessionCookie(r *http.Request) (token string, present bool, err error) {
	invalid := func() (string, bool, error) {
		return "", true, &AccessError{Status: 401, Code: "unauthenticated", Message: "会话凭据格式无效，请重新登录"}
	}
	for _, header := range r.Header.Values("Cookie") {
		for _, part := range strings.Split(header, ";") {
			part = strings.TrimSpace(part)
			name, value, hasValue := strings.Cut(part, "=")
			if strings.TrimSpace(name) != SessionCookieName {
				continue
			}
			if present || !hasValue || name != SessionCookieName || !opaqueCredential.MatchString(value) {
				return invalid()
			}
			token, present = value, true
		}
	}
	return token, present, nil
}
