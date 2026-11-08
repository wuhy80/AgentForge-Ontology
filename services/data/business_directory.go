package data

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

const queryDirectoryMaxBytes = 1 << 20
const queryDirectoryCursorReserve = 4096

func (h *handler) businessDirectory(r *http.Request, category, schemaID, after string) (c.BusinessDirectoryPage, error) {
	var out c.BusinessDirectoryPage
	check, err := platform.SessionRequest(r)
	if err != nil {
		return out, problem(401, "unauthenticated", "无法确认当前目录身份")
	}
	check.Path = r.URL.Path
	encoded, err := json.Marshal(c.BusinessDirectoryRequest{Check: check, CategoryID: category, SchemaID: schemaID, After: after, Limit: 20})
	if err != nil {
		return out, err
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(h.cfg.MetadataURL, "/")+"/internal/metadata/business-directory", bytes.NewReader(encoded))
	if err != nil {
		return out, err
	}
	req.Header.Set("Authorization", "Bearer "+h.cfg.ServiceToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.client.Do(req)
	if err != nil {
		return out, problem(503, "metadata_unavailable", "无法读取授权业务目录")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		var failure c.Error
		_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&failure)
		if resp.StatusCode == 401 || resp.StatusCode == 403 || resp.StatusCode == 409 || resp.StatusCode == 422 {
			return out, problem(resp.StatusCode, failure.Code, failure.Message)
		}
		return out, problem(503, "metadata_unavailable", "授权业务目录不可读取")
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	if readErr != nil || len(body) > 2<<20 {
		return out, problem(503, "metadata_unavailable", "授权业务目录响应超过预算")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	if decoder.Decode(&out) != nil || decoder.Decode(new(any)) != io.EOF || len(out.Entries) > 20 || out.Scanned < len(out.Entries) || out.Scanned > sqlDirectoryBudget {
		return out, problem(503, "metadata_unavailable", "授权业务目录响应不合法")
	}
	if out.HasMore && (out.NextCursor == "" || out.NextCursor == after || len(out.Entries) == 0) {
		return out, problem(503, "metadata_unavailable", "授权业务目录分页不合法")
	}
	return out, nil
}

func directoryFingerprint(categoryID, schemaID, search string, limit int) string {
	return digest([]any{"query-context-v1", categoryID, schemaID, nameKey(search), limit})
}

func (h *handler) directoryCursor(p c.Principal, current c.CurrentDefinition, decision c.Decision, fingerprint string) (string, error) {
	return signCursor(h.cfg.CursorSecret, pageCursor{Fingerprint: fingerprint, TenantID: p.TenantID, Subject: p.Subject, SchemaID: "query-context", Digest: current.ETag, Authorization: decision.State, LastID: current.Definition.ID, Expires: time.Now().Add(15 * time.Minute).Unix()})
}

func (h *handler) directoryAfter(r *http.Request, p c.Principal, cursor, fingerprint string) (string, error) {
	if cursor == "" {
		return "", nil
	}
	bad := func() (string, error) {
		return "", problem(422, "invalid_cursor", "目录游标已失效或不属于本次查询")
	}
	if len(cursor) > queryDirectoryCursorReserve {
		return bad()
	}
	parsed, err := parseCursor(h.cfg.CursorSecret, cursor)
	if err != nil || parsed.Fingerprint != fingerprint || parsed.TenantID != p.TenantID || parsed.Subject != p.Subject || parsed.SchemaID != "query-context" {
		return bad()
	}
	current, err := h.current(r.Context(), p.TenantID, parsed.LastID)
	if err != nil {
		if hiddenSQLSource(err) {
			return bad()
		}
		return "", err
	}
	decision, err := h.authorize(r.Context(), p, "instance.query", parsed.LastID)
	if err != nil {
		if hiddenSQLSource(err) {
			return bad()
		}
		return "", err
	}
	if decision.State != parsed.Authorization || current.ETag != parsed.Digest {
		return bad()
	}
	return parsed.LastID, nil
}

func (h *handler) formDefinitionHTTP(w http.ResponseWriter, r *http.Request) {
	p, err := h.principal(r)
	if err != nil {
		writeError(w, err)
		return
	}
	operation := r.URL.Query().Get("operation")
	if operation == "" {
		operation = "create"
	}
	if operation != "create" && operation != "patch" || len(r.URL.Query()["operation"]) > 1 {
		writeError(w, problem(400, "invalid_form_operation", "表单 operation 仅支持 create 或 patch"))
		return
	}
	id := r.PathValue("schemaId")
	if !uuidPattern.MatchString(id) {
		writeError(w, problem(404, "not_found", "模型不存在或不可访问"))
		return
	}
	current, err := h.current(r.Context(), p.TenantID, id)
	if err != nil {
		writeError(w, err)
		return
	}
	d := current.Definition
	if err = validateSchema(d); err != nil {
		writeError(w, err)
		return
	}
	query, err := h.authorize(r.Context(), p, "instance.query", id)
	if err != nil {
		writeError(w, err)
		return
	}
	if err = validateOntologySQLDecision(d, query); err != nil {
		writeError(w, err)
		return
	}
	decisions := map[string]c.Decision{"instance.query": query}
	for _, name := range []string{"instance.create", "instance.patch"} {
		decision, e := h.cfg.Authorizer.Authorize(r.Context(), p, c.AuthorizationRequest{Operation: name, ResourceID: id, Kind: c.KindSchema})
		if e != nil {
			writeError(w, problem(503, "authorization_unavailable", "无法确认当前表单权限"))
			return
		}
		if decision.Allow && decision.State == "" {
			writeError(w, problem(503, "authorization_unavailable", "缺少权威授权状态标记"))
			return
		}
		if decision.Allow && decision.State != query.State {
			writeError(w, problem(409, "authorization_changed", "授权已改变，请重新读取表单"))
			return
		}
		decisions[name] = decision
	}
	mapped := d.Schema.StorageBinding != nil
	storageReady := true
	protectedFields := map[string]bool{}
	if mapped {
		storageReady, _, err = h.mappedMutationReadiness(r.Context(), current)
		if err != nil {
			writeError(w, err)
			return
		}
		if storageReady {
			protectedFields, err = h.mappedProtectedWriteFields(r.Context(), current)
			if err == nil && operation == "create" {
				protectedFields, err = h.mappedCreateProtectedWriteFields(r.Context(), p, current)
			}
			if err != nil {
				writeError(w, err)
				return
			}
		}
	}
	canWrite := func(permission string) bool {
		decision := decisions[permission]
		return decision.Allow && storageReady && validateWriteContext(current, []c.Decision{decision}, map[string]bool{id: true}) == nil
	}
	out := c.FormDefinition{Definition: c.FormSchemaDefinition{ID: id, Schema: c.FormSchema{Properties: []c.FormField{}, StorageBinding: mapped}}, Operation: operation,
		CreateAllowed: canWrite("instance.create"), PatchAllowed: canWrite("instance.patch")}
	write := decisions["instance."+operation]
	operationAllowed := canWrite("instance." + operation)
	for _, property := range d.Schema.Properties {
		if !fieldAllowed(query.ReadFields, property.ID) {
			continue
		}
		out.Definition.Schema.Properties = append(out.Definition.Schema.Properties, c.FormField{ID: property.ID, Name: property.Name, DataType: property.DataType,
			Required: property.Required, Nullable: property.Nullable, ReadOnly: property.ReadOnly || protectedFields[property.ID] || !operationAllowed || !fieldAllowed(write.WriteFields, property.ID), Precision: property.Precision, Scale: property.Scale, EnumID: property.EnumID})
	}
	for name, previous := range decisions {
		now, e := h.cfg.Authorizer.Authorize(r.Context(), p, c.AuthorizationRequest{Operation: name, ResourceID: id, Kind: c.KindSchema})
		if e != nil {
			writeError(w, problem(503, "authorization_unavailable", "无法确认当前表单权限"))
			return
		}
		if digest(now) != digest(previous) {
			writeError(w, problem(409, "authorization_changed", "授权已改变，请重新读取表单"))
			return
		}
	}
	latest, err := h.current(r.Context(), p.TenantID, id)
	if err != nil {
		writeError(w, err)
		return
	}
	if latest.ETag != current.ETag {
		writeError(w, problem(409, "definition_changed", "模型已改变，请重新读取表单"))
		return
	}
	writeJSON(w, 200, out)
}
