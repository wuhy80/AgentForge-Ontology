package data

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"unicode"

	"github.com/go-sql-driver/mysql"
	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

type mutationSavedItem struct {
	ClientKey string     `json:"clientKey,omitempty"`
	Instance  c.Instance `json:"instance"`
	Outcome   string     `json:"outcome"`
	Operation string     `json:"operation"`
}

type mutationSaved struct {
	Items         []mutationSavedItem `json:"items"`
	Authorization map[string]string   `json:"authorization,omitempty"`
}

type mutationCommand struct {
	ClientKey string
	Match     mutationMatch
	IfMatch   string
	Values    map[string]json.RawMessage
}

type mutationRequest struct {
	Operation      string
	ReceiptOp      string
	IdempotencyKey string
	Hash           string
	ConflictPolicy string
	Commands       []mutationCommand
	Bulk           bool
}

type mutationDecisions map[string]c.Decision

func mutationPermission(operation string) string {
	if operation == "update" {
		return "instance.patch"
	}
	return "instance." + operation
}

func (h *handler) mutationDecisions(ctx context.Context, p c.Principal, schemaID, operation, policy string) (mutationDecisions, error) {
	names := []string{mutationPermission(operation)}
	if operation == "upsert" {
		names = append(names, "instance.create", "instance.patch")
		if policy == "overwrite" {
			names = append(names, "instance.upsert.overwrite")
		}
	}
	out := mutationDecisions{}
	state := ""
	for _, name := range names {
		decision, err := h.authorize(ctx, p, name, schemaID)
		if err != nil {
			return nil, err
		}
		if state != "" && decision.State != state {
			return nil, problem(409, "authorization_changed", "授权已改变，请重新提交")
		}
		state = decision.State
		out[name] = decision
	}
	return out, nil
}

func (d mutationDecisions) relevant(operation string) []c.Decision {
	out := []c.Decision{d[operation]}
	for _, name := range []string{"instance.upsert", "instance.upsert.overwrite"} {
		if decision, ok := d[name]; ok {
			out = append(out, decision)
		}
	}
	return out
}

func mutationReadFields(schema *c.Schema, decisions []c.Decision) []string {
	fields := []string{}
	for _, property := range schema.Properties {
		allowed := true
		for _, decision := range decisions {
			allowed = allowed && fieldAllowed(decision.ReadFields, property.ID)
		}
		if allowed {
			fields = append(fields, property.ID)
		}
	}
	return fields
}

func mutationETag(header string) (string, error) {
	if len(header) < 3 || len(header) > 202 || header[0] != '"' || header[len(header)-1] != '"' {
		return "", problem(428, "precondition_required", "更新或删除需要读取时返回的强 If-Match")
	}
	value := header[1 : len(header)-1]
	for _, char := range value {
		if char < 0x21 || char > 0x7e || char == '"' || char == '\\' {
			return "", problem(428, "precondition_required", "If-Match 须为单个强 ETag")
		}
	}
	return value, nil
}

func mutationID(current c.CurrentDefinition, id string) (string, error) {
	if current.Definition.Schema.StorageBinding == nil {
		if !uuidPattern.MatchString(id) {
			return "", problem(422, "invalid_instance_id", "实例 ID 须为有效 UUID")
		}
		return strings.ToLower(id), nil
	}
	if id == "" || len(id) > 1024 || strings.IndexFunc(id, unicode.IsControl) >= 0 {
		return "", problem(422, "invalid_instance_id", "实例 ID 不合法")
	}
	return id, nil
}

func normalizeMutationValues(schema *c.Schema, values map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	out := map[string]json.RawMessage{}
	for key, value := range values {
		property, ok := findProperty(schema, key)
		if !ok {
			return nil, problem(422, "unknown_field", "写入包含未知属性")
		}
		if _, exists := out[property.ID]; exists {
			return nil, problem(422, "duplicate_field", "同一属性不能同时按名称和 ID 写入")
		}
		out[property.ID] = value
	}
	return out, nil
}

func normalizeMutationMatch(current c.CurrentDefinition, match c.WriteMatch, decisions mutationDecisions) (mutationMatch, error) {
	if match.ID != "" {
		if match.IndexID != "" || len(match.Values) != 0 {
			return mutationMatch{}, problem(422, "invalid_match", "匹配须选择实例 ID 或完整唯一键")
		}
		id, err := mutationID(current, match.ID)
		return mutationMatch{ID: id}, err
	}
	if match.IndexID == "" {
		return mutationMatch{}, problem(422, "invalid_match", "upsert 需要明确实例 ID 或完整唯一键")
	}
	var index *c.Index
	for _, candidate := range current.Definition.Schema.Indexes {
		if candidate.ID == match.IndexID && candidate.Unique {
			copy := candidate
			index = &copy
			break
		}
	}
	if index == nil || len(match.Values) != len(index.FieldIDs) {
		return mutationMatch{}, problem(422, "invalid_match", "匹配须使用已生效的完整唯一键")
	}
	values := map[string]json.RawMessage{}
	for _, fieldID := range index.FieldIDs {
		property, _ := findProperty(current.Definition.Schema, fieldID)
		value, ok := match.Values[fieldID]
		if !ok || string(value) == "null" {
			return mutationMatch{}, problem(422, "invalid_match", "唯一键须使用稳定属性 ID、完整值且不能为 null")
		}
		for _, decision := range decisions.relevant("instance.create") {
			if property.ReadOnly || !fieldAllowed(decision.ReadFields, fieldID) || !fieldAllowed(decision.WriteFields, fieldID) {
				return mutationMatch{}, problem(403, "forbidden_match", "无权使用该唯一键匹配")
			}
		}
		for _, decision := range decisions.relevant("instance.patch") {
			if !fieldAllowed(decision.ReadFields, fieldID) || !fieldAllowed(decision.WriteFields, fieldID) {
				return mutationMatch{}, problem(403, "forbidden_match", "无权使用该唯一键匹配")
			}
		}
		if _, err := typedValue(property, value); err != nil {
			return mutationMatch{}, err
		}
		values[fieldID] = value
	}
	return mutationMatch{Index: index, Values: values}, nil
}

func mutationKey(match mutationMatch) string {
	if match.Index == nil {
		return "id:" + match.ID
	}
	return "key:" + match.Index.ID + ":" + digest(match.Values)
}

func (h *handler) recheckMutations(ctx context.Context, p c.Principal, current c.CurrentDefinition, decisions mutationDecisions) error {
	names := make([]string, 0, len(decisions))
	for name := range decisions {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := h.recheck(ctx, p, name, current.Definition.ID, decisions[name]); err != nil {
			return err
		}
	}
	return nil
}

func mutationCheckOld(ctx context.Context, store mutationStore, schema *c.Schema, old c.Instance, relevant []c.Decision, upsert bool) error {
	for _, decision := range relevant {
		if err := checkRowFilter(ctx, store.Tx(), schema, old.Values, decision.RowFilter); err != nil {
			var api *apiError
			if errors.As(err, &api) && api.status == 403 {
				if upsert {
					return problem(409, "write_conflict", "目标或约束不允许本次写入")
				}
				return problem(404, "not_found", "实例不存在或不可访问")
			}
			return err
		}
	}
	return nil
}

func mutationBuild(ctx context.Context, store mutationStore, schema *c.Schema, input, base map[string]json.RawMessage, relevant []c.Decision, creating bool) (map[string]json.RawMessage, error) {
	var out map[string]json.RawMessage
	for _, decision := range relevant {
		values, err := buildValues(schema, input, base, decision.WriteFields, creating)
		if err != nil {
			return nil, err
		}
		if err = checkNewRow(ctx, store.Tx(), schema, values, decision); err != nil {
			return nil, err
		}
		out = values
	}
	return out, nil
}

func mutationResult(schema *c.Schema, item mutationSavedItem, decisions mutationDecisions) c.WriteResult {
	values := projectInstance(item.Instance, mutationReadFields(schema, decisions.relevant(item.Operation))).Values
	result := c.WriteResult{SchemaID: item.Instance.SchemaID, ClientKey: item.ClientKey, ID: item.Instance.ID, ETag: item.Instance.ETag, Outcome: item.Outcome, Values: values}
	if item.Outcome == "deleted" {
		result.Values, result.ETag = nil, ""
	}
	return result
}

func (h *handler) executeMutations(ctx context.Context, p c.Principal, current c.CurrentDefinition, req mutationRequest, decisions mutationDecisions) (c.BulkWriteResult, error) {
	result := c.BulkWriteResult{SchemaID: current.Definition.ID, Atomic: true, Committed: true, Results: []c.WriteResult{}}
	if err := validateWriteContext(current, mutationContextDecisions(decisions), map[string]bool{current.Definition.ID: true}); err != nil {
		return c.BulkWriteResult{}, err
	}
	// A gate serializes platform requests; retries are still bounded for source
	// unique-key races, deadlocks and first-use idempotency insert contention.
	for attempt := 0; attempt < 3; attempt++ {
		result.Results, result.Replayed = []c.WriteResult{}, false
		err := h.withMutationStore(ctx, p, current, func(store mutationStore) error {
			if req.IdempotencyKey != "" {
				hash, raw, found, err := store.Receipt(ctx, req.ReceiptOp, req.IdempotencyKey)
				if err != nil {
					return err
				}
				if found {
					if hash != req.Hash {
						return problem(409, "idempotency_conflict", "同一幂等键不能代表不同意图")
					}
					var saved mutationSaved
					if json.Unmarshal([]byte(raw), &saved) != nil || len(saved.Items) != len(req.Commands) {
						return problem(500, "idempotency_corrupt", "无法核对原写入结果")
					}
					for _, item := range saved.Items {
						projected, err := replayMutation(ctx, store, current, req, decisions, item, saved.Authorization)
						if err != nil {
							return err
						}
						result.Results = append(result.Results, projected)
					}
					result.Replayed = true
					if _, err := encodeBudgetedJSON(result); err != nil {
						return err
					}
					return h.recheckMutations(ctx, p, current, decisions)
				}
			}
			ordered := make([]int, len(req.Commands))
			for i := range ordered {
				ordered[i] = i
			}
			sort.Slice(ordered, func(i, j int) bool {
				return mutationKey(req.Commands[ordered[i]].Match) < mutationKey(req.Commands[ordered[j]].Match)
			})
			saved := mutationSaved{Items: make([]mutationSavedItem, len(req.Commands)), Authorization: map[string]string{}}
			for name, decision := range decisions {
				saved.Authorization[name] = digest(decision)
			}
			seen := map[string]bool{}
			for _, i := range ordered {
				item, err := applyMutation(ctx, store, current, req, decisions, req.Commands[i], seen)
				if err != nil {
					return err
				}
				saved.Items[i] = item
				// Stop accumulating retained historical values before reaching an
				// unbounded batch even when the public projection is very small.
				budget := jsonBudget{remaining: c.MaxDataResponseBytes - 1}
				if err = budget.add(saved); err != nil {
					return err
				}
			}
			for _, item := range saved.Items {
				result.Results = append(result.Results, mutationResult(current.Definition.Schema, item, decisions))
			}
			if _, err := encodeBudgetedJSON(result); err != nil {
				return err
			}
			if req.IdempotencyKey != "" {
				raw, err := json.Marshal(saved)
				if err != nil {
					return err
				}
				if err = store.SaveReceipt(ctx, req.ReceiptOp, req.IdempotencyKey, req.Hash, string(raw)); err != nil {
					return err
				}
			}
			return h.recheckMutations(ctx, p, current, decisions)
		})
		if err == nil {
			return result, nil
		}
		var db *mysql.MySQLError
		if attempt < 2 && ctx.Err() == nil && errors.As(err, &db) && (db.Number == 1213 || db.Number == 1205 || db.Number == 1062 && req.Operation == "upsert") {
			continue
		}
		return c.BulkWriteResult{}, err
	}
	return c.BulkWriteResult{}, problem(409, "transaction_conflict", "并发操作冲突，请核对后重试")
}

func mutationValuesEqual(ctx context.Context, store mutationStore, schema *c.Schema, fieldID string, a, b json.RawMessage) (bool, error) {
	property, _ := findProperty(schema, fieldID)
	left, le := typedValue(property, a)
	right, re := typedValue(property, b)
	if le != nil {
		return false, le
	}
	if re != nil {
		return false, re
	}
	cast := "CAST(? AS CHAR CHARACTER SET utf8mb4) COLLATE utf8mb4_0900_bin"
	switch property.DataType {
	case "int64", "boolean":
		cast = "CAST(? AS SIGNED)"
	case "decimal":
		typeName, _ := sqlType(property)
		cast = "CAST(? AS " + typeName + ")"
	case "timestamp":
		cast = "CAST(? AS DATETIME(6))"
	}
	var equal bool
	err := store.Tx().QueryRowContext(ctx, "SELECT COALESCE(("+cast+" = "+cast+"),FALSE)", left, right).Scan(&equal)
	return equal, err
}

func writePolicy(policy string) (string, error) {
	if policy == "" {
		return "check", nil
	}
	if policy != "check" && policy != "overwrite" {
		return "", problem(422, "invalid_conflict_policy", "upsert 并发策略仅支持 check 或 overwrite")
	}
	return policy, nil
}

func validWriteKey(key string) bool {
	return key != "" && len(key) <= 128 && strings.IndexFunc(key, unicode.IsControl) < 0
}

func (h *handler) upsertHTTP(w http.ResponseWriter, r *http.Request) {
	p, err := h.principal(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var body c.UpsertRequest
	if err = decode(w, r, &body); err != nil {
		writeError(w, err)
		return
	}
	policy, err := writePolicy(body.ConflictPolicy)
	if err != nil {
		writeError(w, err)
		return
	}
	current, decisions, err := h.prepareMutation(r.Context(), p, r.PathValue("schemaId"), "upsert", policy)
	if err != nil {
		writeError(w, err)
		return
	}
	if !validWriteKey(body.IdempotencyKey) || len(body.Values) == 0 {
		writeError(w, problem(422, "invalid_upsert", "upsert 需要非空 values 和 1..128 字节幂等键"))
		return
	}
	match, err := normalizeMutationMatch(current, body.Match, decisions)
	if err != nil {
		writeError(w, err)
		return
	}
	values, err := normalizeMutationValues(current.Definition.Schema, body.Values)
	if err != nil {
		writeError(w, err)
		return
	}
	ifMatch := r.Header.Get("If-Match")
	if len(r.Header.Values("If-Match")) > 1 || policy == "overwrite" && ifMatch != "" {
		writeError(w, problem(422, "invalid_precondition", "If-Match 须为单项条件且不能与 overwrite 同用"))
		return
	}
	if ifMatch != "" {
		if _, err = mutationETag(ifMatch); err != nil {
			writeError(w, err)
			return
		}
	}
	req := mutationRequest{Operation: "upsert", ReceiptOp: "upsert", IdempotencyKey: body.IdempotencyKey, Hash: digest([]any{body, ifMatch}), ConflictPolicy: policy, Commands: []mutationCommand{{Match: match, Values: values, IfMatch: ifMatch}}}
	result, err := h.executeMutations(r.Context(), p, current, req, decisions)
	if err != nil {
		writeError(w, err)
		return
	}
	status := 200
	if result.Results[0].Outcome == "created" && !result.Replayed {
		status = 201
	}
	w.Header().Set("ETag", `"`+result.Results[0].ETag+`"`)
	writeJSON(w, status, result.Results[0])
}

func (h *handler) prepareMutation(ctx context.Context, p c.Principal, schemaID, operation, policy string) (c.CurrentDefinition, mutationDecisions, error) {
	current, err := h.current(ctx, p.TenantID, schemaID)
	if err != nil {
		return current, nil, err
	}
	if err = validateSchema(current.Definition); err != nil {
		return current, nil, err
	}
	decisions, err := h.mutationDecisions(ctx, p, current.Definition.ID, operation, policy)
	if err == nil {
		err = validateWriteContext(current, mutationContextDecisions(decisions), map[string]bool{current.Definition.ID: true})
	}
	if err == nil && current.Definition.Schema.StorageBinding != nil {
		ready, _, readinessErr := h.mappedMutationReadiness(ctx, current)
		if readinessErr != nil {
			return current, nil, readinessErr
		}
		if !ready {
			return current, nil, problem(422, "mapped_writes_unsupported", "来源尚未通过单表写入认证或显式初始化")
		}
		if operation == "delete" {
			deleteReady, e := h.mappedDeleteReadiness(ctx, current)
			if e != nil {
				return current, nil, e
			}
			if !deleteReady {
				return current, nil, problem(422, "mapped_relation_mutation_unsupported", "当前来源关系尚未通过原子删除认证")
			}
		}
	}
	return current, decisions, err
}

// singleMutationHTTP preserves the existing wire shapes and create receipt
// namespace while sharing every field/row/storage rule with bulk and upsert.
func (h *handler) singleMutationHTTP(w http.ResponseWriter, r *http.Request, operation string) {
	p, err := h.principal(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var body c.WriteRequest
	if operation != "delete" {
		if err = decode(w, r, &body); err != nil {
			writeError(w, err)
			return
		}
	}
	if operation == "create" && !validWriteKey(body.IdempotencyKey) {
		writeError(w, problem(422, "invalid_idempotency_key", "创建需要 1..128 字节幂等键"))
		return
	}
	current, decisions, err := h.prepareMutation(r.Context(), p, r.PathValue("schemaId"), operation, "check")
	if err != nil {
		writeError(w, err)
		return
	}
	if operation == "update" && (len(body.Values) == 0 || body.IdempotencyKey != "") {
		writeError(w, problem(422, "invalid_patch", "PATCH 需要非空 values，不接受创建幂等键"))
		return
	}
	command := mutationCommand{}
	if operation == "create" {
		command.Match.ID = c.NewID()
	} else {
		command.Match.ID, err = mutationID(current, r.PathValue("entityId"))
		if err != nil {
			writeError(w, problem(404, "not_found", "实例不存在或不可访问"))
			return
		}
		command.IfMatch = r.Header.Get("If-Match")
		if len(r.Header.Values("If-Match")) != 1 {
			writeError(w, problem(428, "precondition_required", "更新或删除需要单个强 If-Match"))
			return
		}
		if _, err = mutationETag(command.IfMatch); err != nil {
			writeError(w, err)
			return
		}
	}
	command.Values, err = normalizeMutationValues(current.Definition.Schema, body.Values)
	if err != nil {
		writeError(w, err)
		return
	}
	req := mutationRequest{Operation: operation, ReceiptOp: operation, IdempotencyKey: body.IdempotencyKey, Hash: digest(body.Values), Commands: []mutationCommand{command}}
	result, err := h.executeMutations(r.Context(), p, current, req, decisions)
	if err != nil {
		writeError(w, err)
		return
	}
	if operation == "delete" {
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(204)
		return
	}
	item := result.Results[0]
	values := item.Values
	if values == nil {
		values = map[string]json.RawMessage{}
	}
	w.Header().Set("ETag", `"`+item.ETag+`"`)
	status := 200
	if operation == "create" && !result.Replayed {
		status = 201
	}
	writeJSON(w, status, c.Instance{SchemaID: item.SchemaID, ID: item.ID, ETag: item.ETag, Values: values})
}

func (h *handler) bulkWriteHTTP(operation string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, err := h.principal(r)
		if err != nil {
			writeError(w, err)
			return
		}
		var body c.BulkWriteRequest
		if err = decode(w, r, &body); err != nil {
			writeError(w, err)
			return
		}
		if body.Atomic != nil && !*body.Atomic || len(body.Items) < 1 || len(body.Items) > c.MaxBulkWriteItems || !validWriteKey(body.IdempotencyKey) || r.Header.Get("If-Match") != "" {
			writeError(w, problem(422, "invalid_bulk", "批次仅支持原子提交、1..100项、整批幂等键及逐项 If-Match"))
			return
		}
		policy := "check"
		if operation == "upsert" {
			policy, err = writePolicy(body.ConflictPolicy)
		} else if body.ConflictPolicy != "" {
			err = problem(422, "invalid_conflict_policy", "只有 upsert 可声明并发策略")
		}
		if err != nil {
			writeError(w, err)
			return
		}
		current, decisions, err := h.prepareMutation(r.Context(), p, r.PathValue("schemaId"), operation, policy)
		if err != nil {
			writeError(w, err)
			return
		}
		req := mutationRequest{Operation: operation, ReceiptOp: "bulk_" + operation, Bulk: true, IdempotencyKey: body.IdempotencyKey, Hash: digest(body), ConflictPolicy: policy}
		clients, targets := map[string]bool{}, map[string]bool{}
		for _, item := range body.Items {
			if !validWriteKey(item.ClientKey) || clients[item.ClientKey] {
				writeError(w, problem(422, "invalid_client_key", "clientKey 须为批内唯一的 1..128 字节标识"))
				return
			}
			clients[item.ClientKey] = true
			command, err := normalizeMutationCommand(current, decisions, operation, policy, item)
			if err != nil {
				writeError(w, err)
				return
			}
			key := mutationKey(command.Match)
			if targets[key] {
				writeError(w, problem(422, "duplicate_target", "同一批次不能重复匹配同一目标"))
				return
			}
			targets[key] = true
			req.Commands = append(req.Commands, command)
		}
		result, err := h.executeMutations(r.Context(), p, current, req, decisions)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, 200, result)
	}
}

// replayMutation reauthorizes historical and current rows before projecting a receipt.
func replayMutation(ctx context.Context, store mutationStore, current c.CurrentDefinition, req mutationRequest, decisions mutationDecisions, item mutationSavedItem, authorization map[string]string) (c.WriteResult, error) {
	if item.Operation != "instance.create" && item.Operation != "instance.patch" && item.Operation != "instance.delete" {
		return c.WriteResult{}, problem(500, "idempotency_corrupt", "无法核对原写入结果")
	}
	for _, decision := range decisions.relevant(item.Operation) {
		if err := checkRowFilter(ctx, store.Tx(), current.Definition.Schema, item.Instance.Values, decision.RowFilter); err != nil {
			return c.WriteResult{}, err
		}
	}
	if item.Outcome != "deleted" {
		for _, decision := range decisions.relevant(item.Operation) {
			if err := checkNewRow(ctx, store.Tx(), current.Definition.Schema, item.Instance.Values, decision); err != nil {
				return c.WriteResult{}, err
			}
		}
	}
	actual, exists, err := store.Lookup(ctx, mutationMatch{ID: item.Instance.ID})
	if err != nil {
		return c.WriteResult{}, err
	}
	if item.Outcome == "deleted" {
		for name, decision := range decisions {
			if authorization[name] != digest(decision) {
				return c.WriteResult{}, problem(409, "authorization_changed", "授权已改变，不能重放原删除回执")
			}
		}
	} else if !exists {
		return c.WriteResult{}, problem(404, "not_found", "实例不存在或不可访问")
	}
	if exists {
		if err = mutationCheckOld(ctx, store, current.Definition.Schema, actual, decisions.relevant(item.Operation), req.Operation == "upsert"); err != nil {
			return c.WriteResult{}, err
		}
		if item.Outcome != "deleted" {
			for _, decision := range decisions.relevant(item.Operation) {
				if err = checkNewRow(ctx, store.Tx(), current.Definition.Schema, actual.Values, decision); err != nil {
					return c.WriteResult{}, err
				}
			}
		}
	}
	return mutationResult(current.Definition.Schema, item, decisions), nil
}

// applyMutation runs one logical write in its caller-owned transaction.
func applyMutation(ctx context.Context, store mutationStore, current c.CurrentDefinition, req mutationRequest, decisions mutationDecisions, command mutationCommand, seen map[string]bool) (mutationSavedItem, error) {
	old, exists, err := store.Lookup(ctx, command.Match)
	if err != nil {
		return mutationSavedItem{}, err
	}
	operation := mutationPermission(req.Operation)
	creating := req.Operation == "create" || req.Operation == "upsert" && !exists
	if req.Operation == "upsert" {
		operation = "instance.patch"
		if creating {
			operation = "instance.create"
		}
	}
	relevant := decisions.relevant(operation)
	if exists {
		if err = mutationCheckOld(ctx, store, current.Definition.Schema, old, relevant, req.Operation == "upsert"); err != nil {
			return mutationSavedItem{}, err
		}
		if seen[old.ID] {
			return mutationSavedItem{}, problem(422, "duplicate_target", "同一批次不能重复修改同一实例")
		}
		seen[old.ID] = true
	}
	if req.Operation == "create" && exists {
		return mutationSavedItem{}, problem(409, "write_conflict", "目标或约束不允许本次写入")
	}
	if !creating && !exists {
		if req.Operation == "upsert" && command.IfMatch != "" {
			return mutationSavedItem{}, problem(412, "precondition_failed", "实例已改变或不存在，请重新读取")
		}
		return mutationSavedItem{}, problem(404, "not_found", "实例不存在或不可访问")
	}
	if req.Operation == "upsert" && creating && command.IfMatch != "" {
		return mutationSavedItem{}, problem(412, "precondition_failed", "实例已改变或不存在，请重新读取")
	}
	if !creating && !(req.Operation == "upsert" && req.ConflictPolicy == "overwrite") {
		etag, err := mutationETag(command.IfMatch)
		if err != nil {
			return mutationSavedItem{}, err
		}
		if old.ETag != etag {
			return mutationSavedItem{}, problem(412, "precondition_failed", "实例已被修改，请重新读取")
		}
	}
	item := mutationSavedItem{ClientKey: command.ClientKey, Operation: operation}
	if req.Operation == "delete" {
		item.Instance, item.Outcome = old, "deleted"
		if err = store.Delete(ctx, old); err != nil {
			return mutationSavedItem{}, err
		}
	} else {
		input := command.Values
		if req.Operation == "upsert" && command.Match.Index != nil {
			input = map[string]json.RawMessage{}
			for key, value := range command.Values {
				input[key] = value
			}
			for key, value := range command.Match.Values {
				if other, ok := input[key]; ok {
					equal, err := mutationValuesEqual(ctx, store, current.Definition.Schema, key, other, value)
					if err != nil {
						return mutationSavedItem{}, err
					}
					if !equal {
						return mutationSavedItem{}, problem(422, "match_value_conflict", "写入值不能改变匹配键")
					}
				}
				if creating {
					input[key] = value
				} else {
					// Matching identity remains immutable even when an
					// equivalent decimal/UUID encoding was repeated.
					delete(input, key)
				}
			}
		}
		values, err := mutationBuild(ctx, store, current.Definition.Schema, input, old.Values, relevant, creating)
		if err != nil {
			return mutationSavedItem{}, err
		}
		item.Instance = c.Instance{SchemaID: current.Definition.ID, ID: old.ID, Values: values, ETag: c.NewID()}
		item.Outcome = "updated"
		if creating {
			item.Outcome = "created"
			item.Instance.ID = command.Match.ID
			if item.Instance.ID == "" {
				item.Instance.ID = c.NewID()
			}
		}
		if seen[item.Instance.ID] && !exists {
			return mutationSavedItem{}, problem(422, "duplicate_target", "同一批次不能重复修改同一实例")
		}
		seen[item.Instance.ID] = true
		if stamper, ok := store.(interface {
			Stamp(c.Instance) (c.Instance, error)
		}); ok {
			item.Instance, err = stamper.Stamp(item.Instance)
			if err != nil {
				return mutationSavedItem{}, err
			}
		}
		budget := jsonBudget{remaining: c.MaxDataResponseBytes - 1}
		if err = budget.add(item.Instance); err != nil {
			return mutationSavedItem{}, err
		}
		if creating {
			err = store.Insert(ctx, item.Instance)
		} else {
			changed := []string{}
			for field := range input {
				changed = append(changed, field)
			}
			sort.Strings(changed)
			err = store.Update(ctx, old, item.Instance, changed)
		}
		if err != nil {
			return mutationSavedItem{}, err
		}
	}
	if err = store.Emit(ctx, "instance."+item.Outcome, item.Instance); err != nil {
		return mutationSavedItem{}, err
	}
	return item, nil
}

func normalizeMutationCommand(current c.CurrentDefinition, decisions mutationDecisions, operation, policy string, item c.BulkWriteItem) (mutationCommand, error) {
	var err error
	command := mutationCommand{ClientKey: item.ClientKey, IfMatch: item.IfMatch}
	if operation == "upsert" {
		if item.Match == nil || item.ID != "" || len(item.Values) == 0 || policy == "overwrite" && item.IfMatch != "" {
			err = problem(422, "invalid_bulk_item", "upsert 项需要 match 和 values，并发条件须与策略一致")
		} else {
			command.Match, err = normalizeMutationMatch(current, *item.Match, decisions)
		}
	} else if operation == "create" {
		if item.ID != "" || item.IfMatch != "" || item.Match != nil || len(item.Values) == 0 {
			err = problem(422, "invalid_bulk_item", "create 项只接受 clientKey 和非空 values")
		}
		command.Match.ID = c.NewID()
	} else {
		if item.Match != nil || operation == "delete" && len(item.Values) != 0 || operation == "update" && len(item.Values) == 0 {
			err = problem(422, "invalid_bulk_item", "更新或删除项目格式无效")
		} else {
			command.Match.ID, err = mutationID(current, item.ID)
		}
	}
	if err == nil && command.IfMatch != "" {
		_, err = mutationETag(command.IfMatch)
	}
	if err == nil && (operation == "update" || operation == "delete") && command.IfMatch == "" {
		err = problem(428, "precondition_required", "每个更新或删除项目均需要读取时返回的 If-Match")
	}
	if err == nil {
		command.Values, err = normalizeMutationValues(current.Definition.Schema, item.Values)
	}
	return command, err
}
