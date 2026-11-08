package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

type toolProblem struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"status,omitempty"`
}

func (e *toolProblem) Error() string { return e.Message }
func invalid(message string) error   { return &toolProblem{Code: "invalid_arguments", Message: message} }

func (g *gateway) call(ctx context.Context, base, method, path string, input, output any, key string) error {
	identity, ok := ctx.Value(identityKey{}).(requestIdentity)
	if !ok {
		return &toolProblem{Code: "unauthenticated", Message: "未确认 MCP 调用身份", Status: 401}
	}
	var body []byte
	var err error
	if input != nil {
		body, err = json.Marshal(input)
		if err != nil || len(body) > MaxRequestBytes {
			return invalid("请求内容超过预算或格式无效")
		}
	}
	r, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(base, "/")+path, bytes.NewReader(body))
	if err != nil {
		return &toolProblem{Code: "upstream_unavailable", Message: "服务请求暂不可用", Status: 503}
	}
	r.Header.Set("Authorization", "Bearer "+identity.token)
	r.Header.Set("Accept", "application/json")
	if input != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	response, err := g.client.Do(r)
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return &toolProblem{Code: "canceled", Message: "调用已取消"}
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return &toolProblem{Code: "timeout", Message: "调用超过时限"}
		}
		return &toolProblem{Code: "upstream_unavailable", Message: "本体服务暂不可用", Status: 503}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return upstreamProblem(response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, MaxResultBytes+1))
	if err != nil {
		return &toolProblem{Code: "upstream_unavailable", Message: "未能完整读取服务结果", Status: 503}
	}
	if len(raw) > MaxResultBytes {
		return &toolProblem{Code: "result_budget", Message: "结果超过1MiB，请缩小页数或所选字段", Status: 413}
	}
	latest, err := g.authenticate(ctx, identity.token)
	if err != nil {
		return upstreamProblem(http.StatusUnauthorized)
	}
	if latest.User.ID != identity.session.User.ID || latest.User.TenantID != identity.session.User.TenantID || latest.AccessTokenID != identity.session.AccessTokenID || scopeMask(latest.AccessScopes) != scopeMask(identity.session.AccessScopes) {
		return upstreamProblem(http.StatusUnauthorized)
	}
	if output == nil {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(output) != nil || decoder.Decode(new(any)) != io.EOF {
		return &toolProblem{Code: "invalid_upstream", Message: "本体服务返回了无效结果", Status: 503}
	}
	return nil
}

func upstreamProblem(status int) error {
	code, message := "upstream_unavailable", "本体服务暂不可用"
	switch status {
	case 400:
		code, message = "invalid_request", "参数或查询语法无效，请检查查询分析结果"
	case 401:
		code, message = "invalid_token", "连接凭据已失效，请重新连接"
	case 403:
		code, message = "forbidden", "当前账号或连接范围无权执行此操作"
	case 404:
		code, message = "not_found", "资源不存在或当前账号不可见"
	case 409:
		code, message = "conflict", "请求与当前定义或原幂等请求冲突，请核对状态"
	case 412:
		code, message = "precondition_failed", "资源上下文已改变，请重新读取"
	case 413:
		code, message = "request_budget", "请求超过后端预算"
	case 422:
		code, message = "unsupported_request", "查询、参数或权限组合尚不支持，请检查当前能力与定义"
	case 429:
		code, message = "busy", "服务正忙，请保留原幂等键稍后重试"
	}
	return &toolProblem{Code: code, Message: message, Status: status}
}

func toolResult(value any, err error) (*mcp.CallToolResult, error) {
	isError := err != nil
	if isError {
		var problem *toolProblem
		if !errors.As(err, &problem) {
			problem = &toolProblem{Code: "gateway_error", Message: "调用未完成，请核对连接及请求"}
		}
		value = map[string]any{"error": problem}
	}
	raw, marshalErr := json.Marshal(value)
	if marshalErr != nil || len(raw) > MaxResultBytes {
		return toolResult(nil, &toolProblem{Code: "result_budget", Message: "结果超过预算或无法序列化，请缩小查询范围"})
	}
	return &mcp.CallToolResult{IsError: isError, StructuredContent: json.RawMessage(raw), Content: []mcp.Content{&mcp.TextContent{Text: string(raw)}}}, nil
}

// OAuth endpoints are delegated to their owning identity service. Redirects
// are returned to the browser, never followed with credentials by the gateway.
func (g *gateway) proxyOAuth(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if err != nil {
		writeJSON(w, 413, c.Error{Code: "request_budget", Message: "认证请求超过预算"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, r.Method, strings.TrimRight(g.cfg.MetadataURL, "/")+r.URL.RequestURI(), bytes.NewReader(body))
	if err != nil {
		writeJSON(w, 503, c.Error{Code: "identity_unavailable", Message: "认证服务暂不可用"})
		return
	}
	for _, header := range []string{"Content-Type", "Accept", "Origin"} {
		if v := r.Header.Get(header); v != "" {
			req.Header.Set(header, v)
		}
	}
	res, err := g.client.Do(req)
	if err != nil {
		writeJSON(w, 503, c.Error{Code: "identity_unavailable", Message: "认证服务暂不可用"})
		return
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, MaxResultBytes+1))
	if err != nil || len(raw) > MaxResultBytes {
		writeJSON(w, 503, c.Error{Code: "identity_unavailable", Message: "认证服务响应无效"})
		return
	}
	for _, header := range []string{"Content-Type", "Location", "WWW-Authenticate", "Pragma"} {
		if v := res.Header.Get(header); v != "" {
			w.Header().Set(header, v)
		}
	}
	w.WriteHeader(res.StatusCode)
	_, _ = w.Write(raw)
}

func (g *gateway) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	for _, base := range []string{g.cfg.MetadataURL, g.cfg.DataURL, g.cfg.ExecutionURL} {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/readyz", nil)
		res, err := g.client.Do(req)
		if err != nil {
			writeJSON(w, 503, c.Error{Code: "not_ready", Message: "依赖服务尚未就绪"})
			return
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
		res.Body.Close()
		if res.StatusCode != 200 {
			writeJSON(w, 503, c.Error{Code: "not_ready", Message: "依赖服务尚未就绪"})
			return
		}
	}
	writeJSON(w, 200, map[string]string{"status": "ready"})
}
