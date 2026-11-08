package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/wuhy80/AgentForge-Ontology/contracts"
)

type RemoteSessionChecker struct {
	URL          string
	ServiceToken string
	Client       *http.Client
}

func (c *RemoteSessionChecker) CheckSession(ctx context.Context, check contracts.SessionCheck) (contracts.Session, error) {
	var session contracts.Session
	if (check.AccessToken == "" && !opaqueCredential.MatchString(check.Token)) || (check.AccessToken != "" && (!ValidAccessToken(check.AccessToken) || check.Token != "")) {
		return session, &AccessError{Status: 401, Code: "unauthenticated", Message: "登录已失效，请重新登录"}
	}
	err := authorityCall(ctx, c.URL, c.ServiceToken, c.Client, "/internal/identity/session", check, &session)
	if err == nil {
		err = validateSession(session, check)
	}
	return session, err
}

// RemoteAuthorizer always consults Metadata's current user/role/policy state.
// It never uses the old example grants or a previous allow during an outage.
type RemoteAuthorizer struct {
	URL          string
	ServiceToken string
	Client       *http.Client
}

func (a *RemoteAuthorizer) Authorize(ctx context.Context, p contracts.Principal, req contracts.AuthorizationRequest) (contracts.Decision, error) {
	var decision contracts.Decision
	if p.Subject == "" || p.TenantID == "" || req.Operation == "" {
		return decision, errors.New("missing authorization context")
	}
	err := authorityCall(ctx, a.URL, a.ServiceToken, a.Client, "/internal/authorization/decision", contracts.AuthorizationCheck{Principal: p, Request: req}, &decision)
	if err != nil {
		return contracts.Decision{}, err
	}
	if !decision.Allow {
		return contracts.Decision{}, nil
	}
	if err := validateDecision(decision); err != nil {
		return contracts.Decision{}, err
	}
	return decision, nil
}

func authorityCall(ctx context.Context, base, serviceToken string, client *http.Client, path string, input, output any) error {
	return authorityTenantCall(ctx, base, serviceToken, client, path, "", input, output)
}

func authorityTenantCall(ctx context.Context, base, serviceToken string, client *http.Client, path, tenant string, input, output any) error {
	endpoint, err := url.Parse(base)
	if err != nil || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || len(serviceToken) < 32 {
		return &AccessError{Status: 503, Code: "identity_unavailable", Message: "身份与权限服务未正确配置"}
	}
	value, err := json.Marshal(input)
	if err != nil {
		return errors.New("invalid authority request")
	}
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(callCtx, http.MethodPost, strings.TrimRight(base, "/")+path, bytes.NewReader(value))
	if err != nil {
		return errors.New("invalid authority endpoint")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+serviceToken)
	if tenant != "" {
		request.Header.Set("X-Tenant-ID", tenant)
	}
	transport := http.Client{Timeout: 5 * time.Second}
	if client != nil {
		transport = *client
	}
	transport.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := transport.Do(request)
	if err != nil {
		return &AccessError{Status: 503, Code: "identity_unavailable", Message: "无法确认当前身份与权限，请稍后重试"}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		switch response.StatusCode {
		case 401:
			return &AccessError{Status: 401, Code: "unauthenticated", Message: "登录已失效，请重新登录"}
		case 403:
			return &AccessError{Status: 403, Code: "forbidden", Message: "当前身份不允许此请求"}
		case 422:
			return &AccessError{Status: 422, Code: "unsupported_policy_combination", Message: "当前权限组合无法安全执行，请联系管理员调整"}
		default:
			return &AccessError{Status: 503, Code: "identity_unavailable", Message: "身份与权限服务暂不可用"}
		}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(body) > 1<<20 {
		return errors.New("invalid authority response size")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return errors.New("invalid authority response")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("invalid authority response framing")
	}
	return nil
}

// RemoteObjectContext reads only explicitly configured authorization attributes
// from Data. It cannot be called with a browser token or user-supplied URL.
type RemoteObjectContext struct {
	URL          string
	ServiceToken string
	Client       *http.Client
}

func (c *RemoteObjectContext) Read(ctx context.Context, tenant string, input contracts.ObjectContextRequest) (contracts.ObjectContextResult, error) {
	var result contracts.ObjectContextResult
	if tenant == "" || len(tenant) > 128 {
		return result, errors.New("missing context tenant")
	}
	err := authorityTenantCall(ctx, c.URL, c.ServiceToken, c.Client, "/internal/authorization/object-value", tenant, input, &result)
	return result, err
}
