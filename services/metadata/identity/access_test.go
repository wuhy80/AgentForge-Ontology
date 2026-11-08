package identity

import (
	"crypto/sha256"
	"encoding/base64"
	"testing"
)

func TestExternalOAuthPKCEAndRedirectValidation(t *testing.T) {
	verifier := "a-strong-code-verifier-used-by-the-client-1234567890"
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	if len(challenge) != 43 || !pkce(challenge, verifier) || pkce(challenge, verifier+"x") {
		t.Fatal("S256 PKCE validation failed")
	}
	for _, valid := range []string{"https://client.example/callback", "http://127.0.0.1:49200/callback", "http://localhost/cb"} {
		if !validRedirect(valid) {
			t.Fatalf("valid redirect rejected: %s", valid)
		}
	}
	for _, invalid := range []string{"https://client.example/callback?next=x", "https://client.example/callback#x", "https://user:pass@client.example/callback", "http://client.example/callback", "javascript:alert(1)"} {
		if validRedirect(invalid) {
			t.Fatalf("invalid redirect accepted: %s", invalid)
		}
	}
}
