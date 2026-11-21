package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
)

type actionRecordClient interface {
	ReadActionRecord(context.Context, c.ActionRecordReadRequest) (c.ActionRecordSnapshot, error)
}

func (d *RemoteDataTransactions) ReadActionRecord(ctx context.Context, input c.ActionRecordReadRequest) (c.ActionRecordSnapshot, error) {
	var out c.ActionRecordSnapshot
	if !d.configured() {
		return out, problem(503, "record_read_unavailable", "数据服务尚未配置")
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return out, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(d.URL, "/")+"/internal/execution/current-record", bytes.NewReader(raw))
	if err != nil {
		return out, problem(503, "record_read_unavailable", "数据服务地址无效")
	}
	req.Header.Set("Authorization", "Bearer "+d.ServiceToken)
	req.Header.Set("Content-Type", "application/json")
	client := http.Client{Timeout: 5 * time.Second}
	if d.Client != nil {
		client = *d.Client
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return out, problem(503, "record_read_unavailable", "当前记录暂不可读取")
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case 200:
	case 401, 403, 404, 409, 422:
		return out, problem(resp.StatusCode, "record_read_rejected", "当前记录不存在、不可读取或读取条件已改变")
	default:
		return out, problem(503, "record_read_unavailable", "当前记录暂不可读取")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, c.MaxActionRecordBytes+1))
	if err != nil || len(body) > c.MaxActionRecordBytes || json.Unmarshal(body, &out) != nil || out.ETag == "" {
		return c.ActionRecordSnapshot{}, problem(503, "record_read_unavailable", "当前记录响应无效")
	}
	return out, nil
}

func (h *handler) actionRecordSnapshot(ctx context.Context, r *http.Request, record Record) (c.ActionRecordSnapshot, error) {
	var out c.ActionRecordSnapshot
	read := record.Root.Action.RecordRead
	client, ok := h.cfg.DataTransactions.(actionRecordClient)
	if !ok || !dataTransactionsConfigured(h.cfg.DataTransactions) {
		return out, problem(503, "record_read_unavailable", "当前记录读取服务尚未配置")
	}
	check, err := platform.SessionRequest(r)
	if err != nil {
		return out, err
	}
	if check.AccessToken != "" || check.Token == "" {
		return out, problem(403, "connection_scope_denied", "当前记录读取仅开放浏览器会话")
	}
	instanceID := actionRecordInstanceID(record)
	if instanceID == "" {
		return out, problem(422, "invalid_record_read", "缺少当前记录实例参数")
	}
	out, err = client.ReadActionRecord(ctx, c.ActionRecordReadRequest{
		Session: check, ActionID: record.Root.ID, DefinitionDigest: digest(record.Root), InstanceID: instanceID,
		TenantID: record.TenantID, SubjectID: record.SubjectID,
	})
	if err != nil {
		return out, err
	}
	if out.ETag == "" || len(out.Values) != len(read.FieldIDs) {
		return out, problem(503, "record_read_unavailable", "当前记录字段不符合声明")
	}
	for _, field := range read.FieldIDs {
		if !json.Valid(out.Values[field]) {
			return out, problem(503, "record_read_unavailable", "当前记录字段不符合声明")
		}
	}
	return out, nil
}

func actionRecordInstanceID(record Record) string {
	read := record.Root.Action.RecordRead
	for _, p := range record.Root.Action.Input {
		if p.ID == read.InstanceInputID {
			var id string
			_ = json.Unmarshal(record.Input[p.Name], &id)
			return id
		}
	}
	return ""
}

func actionRecordDigest(snapshot c.ActionRecordSnapshot) string {
	// Authorization is checked afresh by Data. Keep existing read-only
	// invocation digests compatible with snapshots from before write guards.
	snapshot.Authorization = ""
	return digest(snapshot)
}
