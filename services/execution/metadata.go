package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

type MetadataReader interface {
	Current(context.Context, string, string) (c.CurrentDefinition, error)
}

type RemoteMetadata struct {
	URL, ServiceToken string
	Client            *http.Client
}

func (m *RemoteMetadata) Current(ctx context.Context, tenant, id string) (c.CurrentDefinition, error) {
	var out c.CurrentDefinition
	base, err := url.Parse(m.URL)
	if err != nil || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || (base.Scheme != "http" && base.Scheme != "https") || len(m.ServiceToken) < 32 {
		return out, problem(503, "metadata_unavailable", "定义服务未正确配置")
	}
	if !validID(tenant) || !validID(id) {
		return out, problem(404, "not_found", "未找到可调用的当前定义")
	}
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(callCtx, http.MethodGet, strings.TrimRight(m.URL, "/")+"/metadata/definitions/"+url.PathEscape(id), nil)
	if err != nil {
		return out, err
	}
	req.Header.Set("Authorization", "Bearer "+m.ServiceToken)
	req.Header.Set("X-Tenant-ID", tenant)
	client := http.Client{Timeout: 5 * time.Second}
	if m.Client != nil {
		client = *m.Client
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := client.Do(req)
	if err != nil {
		return out, problem(503, "metadata_unavailable", "暂时无法读取当前定义")
	}
	defer res.Body.Close()
	if res.StatusCode == 404 {
		return out, problem(404, "not_found", "未找到可调用的当前定义")
	}
	if res.StatusCode != 200 {
		return out, problem(503, "metadata_unavailable", "无法确认当前定义")
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	if err != nil || len(body) > 1<<20 {
		return out, problem(503, "invalid_metadata", "定义响应超出预算")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&out) != nil || decoder.Decode(new(any)) != io.EOF || out.Definition.TenantID != tenant || out.Definition.ID != id || out.Status != "active" {
		return c.CurrentDefinition{}, problem(503, "invalid_metadata", "定义服务返回了无效当前定义")
	}
	return out, nil
}
