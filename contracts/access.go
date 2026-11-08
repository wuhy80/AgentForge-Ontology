package contracts

// AccessConnection contains only revocable credential metadata. Secrets are
// returned exactly once at creation and are never included in listings.
type AccessConnection struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Scopes    []string `json:"scopes"`
	CreatedAt int64    `json:"createdAt"`
	ExpiresAt int64    `json:"expiresAt"`
	RevokedAt int64    `json:"revokedAt,omitempty"`
	Kind      string   `json:"kind"`
	ClientID  string   `json:"clientId,omitempty"`
}

type AccessConnectionInput struct {
	Name             string   `json:"name"`
	Scopes           []string `json:"scopes"`
	ExpiresInSeconds int64    `json:"expiresInSeconds"`
}

type AccessConnectionCreated struct {
	Connection  AccessConnection `json:"connection"`
	AccessToken string           `json:"accessToken"`
}

type OAuthAuthorizationInput struct {
	ClientID            string `json:"client_id"`
	RedirectURI         string `json:"redirect_uri"`
	ResponseType        string `json:"response_type"`
	CodeChallenge       string `json:"code_challenge"`
	CodeChallengeMethod string `json:"code_challenge_method"`
	Scope               string `json:"scope"`
	State               string `json:"state"`
	Resource            string `json:"resource"`
	Approve             bool   `json:"approve"`
}

type OAuthClientSummary struct {
	ClientID     string   `json:"clientId"`
	Name         string   `json:"name"`
	RedirectURIs []string `json:"redirectUris"`
}
