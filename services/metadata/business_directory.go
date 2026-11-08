package metadata

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
)

const businessDirectoryBytes = 2 << 20

func businessDirectorySource(check c.SessionCheck) bool {
	if (check.Token == "") == (check.AccessToken == "") {
		return false
	}
	if check.AccessToken != "" && !strings.HasPrefix(check.AccessToken, "mcp_") {
		return false
	}
	if check.Method == http.MethodGet {
		return check.Path == "/data/query/context" || strings.HasPrefix(check.Path, "/data/schemas/") && strings.HasSuffix(check.Path, "/form")
	}
	if check.Method != http.MethodPost {
		return false
	}
	switch check.Path {
	case "/data/query/ontology-sql", "/data/query/ontology-sql/analyze", "/data/query/ontology-sql/complete", "/data/query/ontology-sql/paths", "/data/query/ontology-sql/insert-field":
		return true
	}
	return false
}

// This bridge returns private definitions exclusively to authenticated Data.
// Discovery is based on instance.query, independently of definition.read.
func (h *handler) businessDirectoryHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !platform.EqualSecret(platform.Bearer(r), h.cfg.ServiceToken) {
		platform.WriteError(w, 401, "service_unauthenticated", "需要有效服务身份")
		return
	}
	var input c.BusinessDirectoryRequest
	if err := decode(r, &input); err != nil || input.Limit < 1 || input.Limit > 20 || len(input.CategoryID) > 128 || len(input.SchemaID) > 36 || len(input.After) > 36 || !businessDirectorySource(input.Check) {
		platform.WriteError(w, 400, "invalid_directory_request", "业务目录请求无效")
		return
	}
	if h.cfg.SessionChecker == nil || h.cfg.Authorizer == nil {
		platform.WriteError(w, 503, "identity_unavailable", "身份与权限服务未配置")
		return
	}
	session, err := h.cfg.SessionChecker.CheckSession(r.Context(), input.Check)
	if err != nil {
		writeDirectoryAccessError(w, err)
		return
	}
	if !session.User.Enabled || session.User.ID == "" || session.User.TenantID == "" || session.ExpiresAt <= time.Now().Unix() {
		platform.WriteError(w, 401, "unauthenticated", "会话已失效")
		return
	}
	p := c.Principal{Subject: session.User.ID, TenantID: session.User.TenantID, ExpiresAt: session.ExpiresAt}
	out, err := h.businessDirectoryPage(r, p, input)
	if err != nil {
		writeProblem(w, err)
		return
	}
	latest, err := h.cfg.SessionChecker.CheckSession(r.Context(), input.Check)
	if err != nil {
		writeDirectoryAccessError(w, err)
		return
	}
	if !latest.User.Enabled || latest.User.ID != p.Subject || latest.User.TenantID != p.TenantID || latest.User.ETag != session.User.ETag || latest.ExpiresAt <= time.Now().Unix() || latest.AccessTokenID != session.AccessTokenID {
		platform.WriteError(w, 401, "unauthenticated", "会话已失效")
		return
	}
	writeJSON(w, 200, out)
}

// Read a small bounded batch and close its SQL cursor before consulting the
// authorizer: identity authorization may need a connection from the same pool.
func (h *handler) businessDirectoryBatch(r *http.Request, p c.Principal, input c.BusinessDirectoryRequest, after string) ([]c.BusinessDirectoryEntry, error) {
	query := `SELECT d.current_json,d.current_digest,d.status,c.name FROM af_meta_definitions d JOIN af_meta_organizations c ON c.tenant_id=d.tenant_id AND c.id=d.category_id AND c.kind='category' WHERE d.tenant_id=? AND d.kind='schema' AND d.status='active' AND d.current_json IS NOT NULL AND d.id>?`
	args := []any{p.TenantID, after}
	if input.CategoryID != "" {
		query += " AND d.category_id=?"
		args = append(args, input.CategoryID)
	}
	if input.SchemaID != "" {
		query += " AND d.id=?"
		args = append(args, input.SchemaID)
	}
	query += " ORDER BY d.id LIMIT 5"
	rows, err := h.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []c.BusinessDirectoryEntry{}
	used := 0
	for rows.Next() {
		var raw sql.NullString
		entry := c.BusinessDirectoryEntry{}
		if err = rows.Scan(&raw, &entry.Current.ETag, &entry.Current.Status, &entry.Category); err != nil {
			return nil, err
		}
		if len(raw.String) > businessDirectoryBytes {
			return nil, problem(422, "directory_budget", "单个模型超过业务目录读取预算")
		}
		if used+len(raw.String) > businessDirectoryBytes && len(out) > 0 {
			break
		}
		used += len(raw.String)
		entry.Current.Definition, err = parseDefinition(raw)
		if err != nil || entry.Current.Definition.TenantID != p.TenantID || entry.Current.Definition.Kind != c.KindSchema {
			return nil, problem(503, "metadata_unavailable", "业务目录响应无效")
		}
		out = append(out, entry)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	return out, nil
}

func (h *handler) businessDirectoryPage(r *http.Request, p c.Principal, input c.BusinessDirectoryRequest) (c.BusinessDirectoryPage, error) {
	out := c.BusinessDirectoryPage{Entries: []c.BusinessDirectoryEntry{}}
	after, state, used := input.After, "", 1024
	complete := false
	for !complete {
		entries, err := h.businessDirectoryBatch(r, p, input, after)
		if err != nil {
			return out, err
		}
		if len(entries) == 0 {
			break
		}
		for _, entry := range entries {
			out.Scanned++
			if out.Scanned > 1000 {
				return out, problem(422, "directory_budget", "业务目录扫描超过预算，请指定分类")
			}
			after = entry.Current.Definition.ID
			entry.Decision, err = h.cfg.Authorizer.Authorize(r.Context(), p, c.AuthorizationRequest{Operation: "instance.query", ResourceID: after, Kind: c.KindSchema})
			if err != nil {
				return out, problem(503, "authorization_unavailable", "无法确认当前业务目录授权")
			}
			if !entry.Decision.Allow {
				continue
			}
			if entry.Decision.State == "" {
				return out, problem(503, "authorization_unavailable", "缺少权威授权状态标记")
			}
			if state != "" && state != entry.Decision.State {
				return out, problem(409, "authorization_changed", "授权已改变，请重新读取目录")
			}
			state = entry.Decision.State
			encoded, e := json.Marshal(entry)
			if e != nil {
				return out, problem(503, "metadata_unavailable", "业务目录响应无效")
			}
			if len(out.Entries) == input.Limit || used+len(encoded)+1 > businessDirectoryBytes {
				if len(out.Entries) == 0 {
					return out, problem(422, "directory_budget", "单个模型超过业务目录读取预算")
				}
				out.HasMore = true
				out.NextCursor = out.Entries[len(out.Entries)-1].Current.Definition.ID
				complete = true
				break
			}
			used += len(encoded) + 1
			out.Entries = append(out.Entries, entry)
		}
	}
	if len(out.Entries) > 0 {
		last := out.Entries[len(out.Entries)-1]
		latest, err := h.cfg.Authorizer.Authorize(r.Context(), p, c.AuthorizationRequest{Operation: "instance.query", ResourceID: last.Current.Definition.ID, Kind: c.KindSchema})
		before, _ := json.Marshal(last.Decision)
		after, _ := json.Marshal(latest)
		if err != nil {
			return out, problem(503, "authorization_unavailable", "无法确认当前业务目录授权")
		}
		if string(before) != string(after) {
			return out, problem(409, "authorization_changed", "授权已改变，请重新读取目录")
		}
	}
	return out, nil
}
