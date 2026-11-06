package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/wuhy80/AgentForge-Ontology/contracts"
)

type Authorizer interface {
	Authorize(context.Context, contracts.Principal, contracts.AuthorizationRequest) (contracts.Decision, error)
}

// OPAAuthorizer does not cache allow decisions. Its result is a deliberately
// bounded initial protocol; arbitrary Rego/UCAST nodes are not accepted as SQL.
type OPAAuthorizer struct {
	URL         string
	Client      *http.Client
	BearerToken string
}

func (a *OPAAuthorizer) Authorize(ctx context.Context, p contracts.Principal, req contracts.AuthorizationRequest) (contracts.Decision, error) {
	var denied contracts.Decision
	if p.Subject == "" || p.TenantID == "" || req.Operation == "" {
		return denied, errors.New("missing authorization context")
	}
	u, err := url.Parse(a.URL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return denied, errors.New("OPA endpoint is not configured")
	}
	input := struct {
		Input struct {
			Principal contracts.Principal            `json:"principal"`
			Request   contracts.AuthorizationRequest `json:"request"`
		} `json:"input"`
	}{}
	input.Input.Principal = p
	input.Input.Request = req
	body, err := json.Marshal(input)
	if err != nil {
		return denied, err
	}
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	r, err := http.NewRequestWithContext(callCtx, http.MethodPost, strings.TrimRight(a.URL, "/")+"/v1/data/ontology/decision", bytes.NewReader(body))
	if err != nil {
		return denied, err
	}
	r.Header.Set("Content-Type", "application/json")
	if a.BearerToken != "" {
		r.Header.Set("Authorization", "Bearer "+a.BearerToken)
	}
	client := a.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	res, err := client.Do(r)
	if err != nil {
		return denied, fmt.Errorf("authorization unavailable: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return denied, errors.New("authorization engine rejected request")
	}
	var wire struct {
		Result *contracts.Decision `json:"result"`
	}
	decoder := json.NewDecoder(io.LimitReader(res.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return denied, errors.New("invalid authorization decision")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return denied, errors.New("invalid authorization response framing")
	}
	if wire.Result == nil {
		return denied, errors.New("undefined authorization decision")
	}
	d := *wire.Result
	if !d.Allow {
		return denied, nil
	}
	if strings.TrimSpace(d.State) == "" {
		return denied, errors.New("authorization state is required")
	}
	if len(d.ReadFields) > 256 || len(d.WriteFields) > 256 {
		return denied, errors.New("authorization field budget exceeded")
	}
	// Consumers bind every filter field to the actual current definition and
	// reject unsupported operators. A grant never bypasses their type checking.
	return d, nil
}
