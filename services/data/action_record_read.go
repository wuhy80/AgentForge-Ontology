package data

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
)

func (h *handler) actionRecordReadHTTP(w http.ResponseWriter, r *http.Request) {
	if len(h.cfg.ServiceToken) < 32 || !platform.EqualSecret(platform.Bearer(r), h.cfg.ServiceToken) {
		writeError(w, problem(401, "unauthorized", "内部认证失败"))
		return
	}
	var request c.ActionRecordReadRequest
	if err := decode(w, r, &request); err != nil {
		writeError(w, err)
		return
	}
	check := request.Session
	if check.Token == "" || check.AccessToken != "" || check.Path != "" || check.Resource != "" ||
		(check.Method != http.MethodPost && check.Method != http.MethodGet) || !uuidPattern.MatchString(request.InstanceID) {
		writeError(w, problem(403, "invalid_execution_context", "读取当前记录需要原调用者会话与实例 ID"))
		return
	}
	p, err := h.executionTransactionPrincipal(r.Context(), check)
	if err == nil && (p.TenantID != request.TenantID || p.Subject != request.SubjectID) {
		err = problem(403, "invalid_execution_context", "原调用者会话已改变")
	}
	if err != nil {
		writeError(w, err)
		return
	}
	snapshot, err := h.readActionRecord(r.Context(), p, request)
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, snapshot)
}

func (h *handler) readActionRecord(ctx context.Context, p c.Principal, request c.ActionRecordReadRequest) (c.ActionRecordSnapshot, error) {
	var snapshot c.ActionRecordSnapshot
	checkAction := func() (*c.ActionRecordRead, error) {
		current, err := h.current(ctx, p.TenantID, request.ActionID)
		if err != nil {
			return nil, err
		}
		d := current.Definition
		if d.Kind != c.KindAction || current.Status != "active" || d.ID != request.ActionID || digest(d) != request.DefinitionDigest || d.Action == nil || d.Action.RecordRead == nil || c.ValidateActionRecordRead(d) != nil {
			return nil, problem(409, "definition_changed", "当前动作或读取声明已改变")
		}
		invoke, err := h.authorizeKind(ctx, p, "action.invoke", d.ID, c.KindAction)
		if err != nil {
			return nil, err
		}
		if invoke.RowFilter != nil || invoke.NewRowFilter != nil {
			return nil, problem(422, "unsupported_authorization", "调用授权不支持行条件")
		}
		return d.Action.RecordRead, nil
	}
	read, err := checkAction()
	if err != nil {
		return snapshot, err
	}
	schema, err := h.current(ctx, p.TenantID, read.SchemaID)
	if err != nil {
		return snapshot, err
	}
	if err := validateSchema(schema.Definition); err != nil {
		return snapshot, err
	}
	if schema.Status != "active" || schema.Definition.ID != read.SchemaID || schema.Definition.Schema.StorageBinding != nil {
		return snapshot, problem(422, "invalid_record_read", "当前记录只支持平台托管 Schema")
	}
	decision, err := h.authorize(ctx, p, "instance.read", read.SchemaID)
	if err != nil {
		return snapshot, err
	}
	fields := make([]c.Property, 0, len(read.FieldIDs))
	for _, id := range read.FieldIDs {
		p, ok := findProperty(schema.Definition.Schema, id)
		if !ok || p.ID != id || p.DataType == "longtext" || !fieldAllowed(decision.ReadFields, id) {
			return snapshot, problem(403, "record_fields_unavailable", "声明字段不可读取")
		}
		fields = append(fields, p)
	}
	err = h.cfg.Engine.WithTx(ctx, func(tx *sql.Tx) error {
		if err := h.gate(ctx, tx, p, schema); err != nil {
			return err
		}
		instance, err := selectInstanceFields(ctx, tx, schema.Definition, fields, p.TenantID, request.InstanceID, decision.RowFilter, false)
		if err != nil {
			return err
		}
		snapshot = c.ActionRecordSnapshot{ETag: instance.ETag, Values: instance.Values, Authorization: digest(decision)}
		if encoded, err := json.Marshal(snapshot); err != nil || len(encoded) > c.MaxActionRecordBytes {
			return problem(422, "record_read_budget", "当前记录超过读取预算")
		}
		if err := h.recheck(ctx, p, "instance.read", read.SchemaID, decision); err != nil {
			return err
		}
		if err := h.recheckEnums(ctx, schema); err != nil {
			return err
		}
		fresh, err := h.executionTransactionPrincipal(ctx, request.Session)
		if err != nil {
			return err
		}
		if fresh.TenantID != p.TenantID || fresh.Subject != p.Subject {
			return problem(403, "invalid_execution_context", "原调用者会话已改变")
		}
		_, err = checkAction()
		return err
	})
	return snapshot, err
}
