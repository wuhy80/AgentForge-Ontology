package data

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"sort"

	"github.com/go-sql-driver/mysql"
	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

type preparedTransactionOperation struct {
	current   c.CurrentDefinition
	request   mutationRequest
	decisions mutationDecisions
	command   mutationCommand
}

// Only complete terminal receipts are committed. A temporary claim stays in
// the same transaction and is never independently visible or recoverable.
type transactionSaved struct {
	Status string          `json:"status"`
	Items  []mutationSaved `json:"items,omitempty"`
	Error  *c.Error        `json:"error,omitempty"`
}

func (h *handler) writeTransactionHTTP(lookup bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, err := h.principal(r)
		if err != nil {
			writeError(w, err)
			return
		}
		var body c.WriteTransactionRequest
		if err = decode(w, r, &body); err != nil {
			writeError(w, err)
			return
		}
		if len(r.Header.Values("If-Match")) != 0 {
			writeError(w, problem(422, "invalid_transaction", "事务并发条件必须逐项提供"))
			return
		}
		result, err := h.executeWriteTransaction(r.Context(), p, body, lookup)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	}
}

func (h *handler) executeWriteTransaction(ctx context.Context, p c.Principal, body c.WriteTransactionRequest, lookup bool) (c.WriteTransactionResult, error) {
	return h.executeGuardedWriteTransaction(ctx, p, body, lookup, nil)
}

// guard revalidates the original session and callable identity immediately
// before business commit. It never supplies or expands the user's authority.
func (h *handler) executeGuardedWriteTransaction(ctx context.Context, p c.Principal, body c.WriteTransactionRequest, lookup bool, guard func(context.Context) error) (c.WriteTransactionResult, error) {
	ctx, cancel := context.WithTimeout(ctx, dataRequestTimeout)
	defer cancel()
	if p.TenantID == "" || len(p.TenantID) > 128 || p.Subject == "" || len(p.Subject) > 255 {
		return c.WriteTransactionResult{}, problem(401, "unauthorized", "需要可信身份和租户")
	}
	if !validWriteKey(body.IdempotencyKey) || len(body.Operations) < 1 || len(body.Operations) > c.MaxBulkWriteItems {
		return c.WriteTransactionResult{}, problem(422, "invalid_transaction", "事务需要整笔幂等键及 1..100 项操作")
	}
	budget := jsonBudget{remaining: c.MaxDataRequestBytes}
	if err := budget.add(body); err != nil {
		return c.WriteTransactionResult{}, problem(413, "request_too_large", "Data 请求体不能超过 8 MiB")
	}
	if guard != nil {
		if err := guard(ctx); err != nil {
			return c.WriteTransactionResult{}, err
		}
	}
	operations, schemas, err := h.prepareWriteTransaction(ctx, p, body)
	if err != nil {
		if failure, safe := transactionFailure(err); safe {
			return h.recoverRejectedWriteTransaction(ctx, p, body, failure, err, lookup)
		}
		return c.WriteTransactionResult{}, err
	}
	if h.cfg.Engine == nil {
		return c.WriteTransactionResult{}, problem(503, "storage_unavailable", "数据存储未配置")
	}
	hash := digest(body)
	for attempt := 0; attempt < 3; attempt++ {
		result := c.WriteTransactionResult{Atomic: true, Results: []c.WriteResult{}}
		var callbackErr error
		err = h.cfg.Engine.WithTx(ctx, func(tx *sql.Tx) (txErr error) {
			defer func() { callbackErr = txErr }()
			raw, found, receiptErr := lockTransactionReceipt(ctx, tx, p, body.IdempotencyKey, hash, !lookup)
			if receiptErr != nil {
				return receiptErr
			}
			if lookup && !found {
				result.Status = "not_found"
				return h.recheckWriteTransaction(ctx, p, operations, guard)
			}
			// Every managed write already participates in these gates. Sorting
			// across schemas gives mixed transactions the same admission order.
			for _, current := range schemas {
				if txErr = h.gate(ctx, tx, p, current); txErr != nil {
					return txErr
				}
			}
			if found {
				var saved transactionSaved
				if json.Unmarshal([]byte(raw), &saved) != nil {
					return problem(500, "idempotency_corrupt", "无法核对原事务结果")
				}
				result, txErr = replayWriteTransaction(ctx, tx, p, operations, saved)
				if txErr != nil {
					return txErr
				}
				return h.recheckWriteTransaction(ctx, p, operations, guard)
			}
			if _, txErr = tx.ExecContext(ctx, "SAVEPOINT af_transaction_writes"); txErr != nil {
				return txErr
			}
			saved := transactionSaved{Status: "committed", Items: make([]mutationSaved, len(operations))}
			result, txErr = applyWriteTransaction(ctx, tx, p, operations, &saved)
			if txErr == nil {
				txErr = h.recheckWriteTransaction(ctx, p, operations, guard)
			}
			if txErr != nil {
				failure, safe := transactionFailure(txErr)
				if !safe {
					return txErr
				}
				// Failed means the write savepoint was actually rolled back AND
				// this safe terminal receipt committed. Transport/commit errors
				// return an HTTP error and never claim a known rollback outcome.
				if _, rollbackErr := tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT af_transaction_writes"); rollbackErr != nil {
					return rollbackErr
				}
				saved = transactionSaved{Status: "failed", Error: failure}
				result = c.WriteTransactionResult{Atomic: true, Status: "failed", Results: []c.WriteResult{}, Error: failure}
			}
			if _, txErr = tx.ExecContext(ctx, "RELEASE SAVEPOINT af_transaction_writes"); txErr != nil {
				return txErr
			}
			return saveTransactionReceipt(ctx, tx, p, body.IdempotencyKey, hash, saved)
		})
		if err == nil {
			return result, nil
		}
		var db *mysql.MySQLError
		// A commit error has an uncertain outcome; only failures reported by
		// the transaction body are eligible for a bounded whole-tx retry.
		if attempt < 2 && callbackErr != nil && ctx.Err() == nil && errors.As(callbackErr, &db) && (db.Number == 1213 || db.Number == 1205) {
			continue
		}
		return c.WriteTransactionResult{}, err
	}
	return c.WriteTransactionResult{}, problem(409, "transaction_conflict", "并发操作冲突，请核对后重试")
}

// The same claim lock protects execution and deterministic preflight failures.
// A failed preflight can wait for an earlier execution, but cannot overwrite
// its committed result or interpret a competing transaction as rolled back.
func lockTransactionReceipt(ctx context.Context, tx *sql.Tx, p c.Principal, key, hash string, claim bool) (string, bool, error) {
	owner := "pending:" + c.NewID()
	if claim {
		if _, err := tx.ExecContext(ctx, "INSERT INTO af_data_transaction_receipts (tenant_id,subject_id,request_key,request_hash,response) VALUES (?,?,?,?,?) ON DUPLICATE KEY UPDATE request_key=request_key", p.TenantID, p.Subject, key, hash, owner); err != nil {
			return "", false, err
		}
	}
	var storedHash, raw string
	err := tx.QueryRowContext(ctx, "SELECT request_hash,response FROM af_data_transaction_receipts WHERE tenant_id=? AND subject_id=? AND request_key=? FOR UPDATE", p.TenantID, p.Subject, key).Scan(&storedHash, &raw)
	if errors.Is(err, sql.ErrNoRows) && !claim {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if storedHash != hash {
		return "", false, problem(409, "idempotency_conflict", "同一幂等键不能代表不同意图")
	}
	return raw, !claim || raw != owner, nil
}

func saveTransactionReceipt(ctx context.Context, tx *sql.Tx, p c.Principal, key, hash string, saved transactionSaved) error {
	encoded, err := encodeBudgetedJSON(saved)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE af_data_transaction_receipts SET response=? WHERE tenant_id=? AND subject_id=? AND request_key=? AND request_hash=?", string(encoded), p.TenantID, p.Subject, key, hash)
	return err
}

func (h *handler) recoverRejectedWriteTransaction(ctx context.Context, p c.Principal, body c.WriteTransactionRequest, failure *c.Error, cause error, lookup bool) (c.WriteTransactionResult, error) {
	if h.cfg.Engine == nil {
		return c.WriteTransactionResult{}, problem(503, "storage_unavailable", "数据存储未配置")
	}
	result := c.WriteTransactionResult{Atomic: true, Status: "failed", Results: []c.WriteResult{}, Error: failure}
	err := h.cfg.Engine.WithTx(ctx, func(tx *sql.Tx) error {
		hash := digest(body)
		raw, found, err := lockTransactionReceipt(ctx, tx, p, body.IdempotencyKey, hash, !lookup)
		if err != nil {
			return err
		}
		if lookup && !found {
			return cause
		}
		if found {
			var saved transactionSaved
			if json.Unmarshal([]byte(raw), &saved) != nil {
				return problem(500, "idempotency_corrupt", "无法核对原事务结果")
			}
			if saved.Status == "committed" {
				// The caller currently failed authorization/validation. Preserve
				// the success receipt and return that rejection, with no old data.
				return cause
			}
			if saved.Status != "failed" || saved.Error == nil || len(saved.Items) != 0 {
				return problem(500, "idempotency_corrupt", "无法核对原事务结果")
			}
			result.Error, result.Replayed = saved.Error, true
			return nil
		}
		return saveTransactionReceipt(ctx, tx, p, body.IdempotencyKey, hash, transactionSaved{Status: "failed", Error: failure})
	})
	if err != nil {
		return c.WriteTransactionResult{}, err
	}
	return result, nil
}

func (h *handler) prepareWriteTransaction(ctx context.Context, p c.Principal, body c.WriteTransactionRequest) ([]preparedTransactionOperation, []c.CurrentDefinition, error) {
	operations := make([]preparedTransactionOperation, 0, len(body.Operations))
	schemaMap := map[string]c.CurrentDefinition{}
	clients, targets := map[string]bool{}, map[string]bool{}
	state := ""
	for _, item := range body.Operations {
		if !validWriteKey(item.ClientKey) || clients[item.ClientKey] {
			return nil, nil, problem(422, "invalid_client_key", "clientKey 须为事务内唯一的 1..128 字节标识")
		}
		clients[item.ClientKey] = true
		if !uuidPattern.MatchString(item.SchemaID) || item.Operation != "create" && item.Operation != "update" && item.Operation != "delete" && item.Operation != "upsert" {
			return nil, nil, problem(422, "invalid_transaction_operation", "事务仅接受稳定 Schema ID 及 create/update/delete/upsert")
		}
		policy, err := writePolicy(item.ConflictPolicy)
		if err == nil && item.Operation != "upsert" && item.ConflictPolicy != "" {
			err = problem(422, "invalid_conflict_policy", "只有 upsert 可声明并发策略")
		}
		if err != nil {
			return nil, nil, err
		}
		current, found := schemaMap[item.SchemaID]
		if !found {
			current, err = h.current(ctx, p.TenantID, item.SchemaID)
			if err != nil {
				return nil, nil, err
			}
			if current.Definition.ID != item.SchemaID || current.Definition.TenantID != p.TenantID {
				return nil, nil, problem(404, "not_found", "Schema 不存在或不可访问")
			}
		}
		decisions, err := h.mutationDecisions(ctx, p, item.SchemaID, item.Operation, policy)
		if err != nil {
			return nil, nil, err
		}
		if !found {
			// Storage readiness must not reveal an inaccessible object's mode.
			if current.Definition.Schema != nil && current.Definition.Schema.StorageBinding != nil {
				return nil, nil, problem(422, "transaction_storage_unsupported", "事务首批仅支持默认平台存储，不支持映射或跨来源 Schema")
			}
			if err = validateSchema(current.Definition); err != nil {
				return nil, nil, err
			}
			schemaMap[item.SchemaID] = current
		}
		for _, decision := range decisions {
			if state != "" && state != decision.State {
				return nil, nil, problem(409, "authorization_changed", "授权已改变，请重新提交")
			}
			state = decision.State
		}
		command, err := normalizeMutationCommand(current, decisions, item.Operation, policy, c.BulkWriteItem{ClientKey: item.ClientKey, ID: item.ID, IfMatch: item.IfMatch, Match: item.Match, Values: item.Values})
		if err != nil {
			return nil, nil, err
		}
		key := item.SchemaID + ":" + mutationKey(command.Match)
		if targets[key] {
			return nil, nil, problem(422, "duplicate_target", "同一事务不能重复匹配同一目标")
		}
		targets[key] = true
		operations = append(operations, preparedTransactionOperation{current: current, request: mutationRequest{Operation: item.Operation, ConflictPolicy: policy}, decisions: decisions, command: command})
	}
	schemas := make([]c.CurrentDefinition, 0, len(schemaMap))
	contextTargets := map[string]bool{}
	for id := range schemaMap {
		contextTargets[id] = true
	}
	for _, item := range operations {
		if err := validateWriteContext(item.current, mutationContextDecisions(item.decisions), contextTargets); err != nil {
			return nil, nil, err
		}
	}
	for _, current := range schemaMap {
		schemas = append(schemas, current)
	}
	sort.Slice(schemas, func(i, j int) bool { return schemas[i].Definition.ID < schemas[j].Definition.ID })
	return operations, schemas, nil
}

func (h *handler) recheckWriteTransaction(ctx context.Context, p c.Principal, operations []preparedTransactionOperation, guard func(context.Context) error) error {
	checked := map[string]bool{}
	for _, item := range operations {
		if err := h.recheckMutations(ctx, p, item.current, item.decisions); err != nil {
			return err
		}
		id := item.current.Definition.ID
		if !checked[id] {
			current, err := h.current(ctx, p.TenantID, id)
			if err != nil {
				return err
			}
			if current.ETag != item.current.ETag {
				return problem(409, "definition_changed", "Schema 已改变，请重新提交")
			}
			checked[id] = true
		}
	}
	if guard != nil {
		return guard(ctx)
	}
	return nil
}

func transactionOperationOrder(operations []preparedTransactionOperation) []int {
	order := make([]int, len(operations))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(i, j int) bool {
		left, right := operations[order[i]], operations[order[j]]
		if left.current.Definition.ID != right.current.Definition.ID {
			return left.current.Definition.ID < right.current.Definition.ID
		}
		return mutationKey(left.command.Match) < mutationKey(right.command.Match)
	})
	return order
}

func applyWriteTransaction(ctx context.Context, tx *sql.Tx, p c.Principal, operations []preparedTransactionOperation, saved *transactionSaved) (c.WriteTransactionResult, error) {
	result := c.WriteTransactionResult{Atomic: true, Committed: true, Status: "committed", Results: make([]c.WriteResult, len(operations))}
	seen := map[string]map[string]bool{}
	for _, i := range transactionOperationOrder(operations) {
		item := operations[i]
		id := item.current.Definition.ID
		if seen[id] == nil {
			seen[id] = map[string]bool{}
		}
		store := &platformMutationStore{tx: tx, p: p, current: item.current}
		out, err := applyMutation(ctx, store, item.current, item.request, item.decisions, item.command, seen[id])
		if err != nil {
			return c.WriteTransactionResult{}, err
		}
		authorization := map[string]string{}
		authorization["enumContext"] = c.EnumContextDigest(item.current.Definition)
		for name, decision := range item.decisions {
			authorization[name] = digest(decision)
		}
		saved.Items[i] = mutationSaved{Items: []mutationSavedItem{out}, Authorization: authorization}
		budget := jsonBudget{remaining: c.MaxDataResponseBytes - 1}
		if err = budget.add(saved); err != nil {
			return c.WriteTransactionResult{}, err
		}
		result.Results[i] = mutationResult(item.current.Definition.Schema, out, item.decisions)
	}
	if _, err := encodeBudgetedJSON(result); err != nil {
		return c.WriteTransactionResult{}, err
	}
	return result, nil
}

func replayWriteTransaction(ctx context.Context, tx *sql.Tx, p c.Principal, operations []preparedTransactionOperation, saved transactionSaved) (c.WriteTransactionResult, error) {
	result := c.WriteTransactionResult{Atomic: true, Replayed: true, Status: saved.Status, Results: []c.WriteResult{}}
	if saved.Status == "failed" && saved.Error != nil && len(saved.Items) == 0 {
		result.Error = saved.Error
		return result, nil
	}
	if saved.Status != "committed" || saved.Error != nil || len(saved.Items) != len(operations) {
		return c.WriteTransactionResult{}, problem(500, "idempotency_corrupt", "无法核对原事务结果")
	}
	result.Committed, result.Results = true, make([]c.WriteResult, len(operations))
	for _, i := range transactionOperationOrder(operations) {
		operation, historical := operations[i], saved.Items[i]
		if len(historical.Items) != 1 || historical.Items[0].ClientKey != operation.command.ClientKey || historical.Items[0].Instance.SchemaID != operation.current.Definition.ID {
			return c.WriteTransactionResult{}, problem(500, "idempotency_corrupt", "无法核对原事务结果")
		}
		store := &platformMutationStore{tx: tx, p: p, current: operation.current}
		out, err := replayMutation(ctx, store, operation.current, operation.request, operation.decisions, historical.Items[0], historical.Authorization)
		if err != nil {
			return c.WriteTransactionResult{}, err
		}
		result.Results[i] = out
	}
	if _, err := encodeBudgetedJSON(result); err != nil {
		return c.WriteTransactionResult{}, err
	}
	return result, nil
}

func transactionFailure(err error) (*c.Error, bool) {
	var api *apiError
	if errors.As(err, &api) && api.status >= 400 && api.status < 500 {
		return &c.Error{Code: api.code, Message: api.message}, true
	}
	var db *mysql.MySQLError
	if errors.As(err, &db) {
		switch db.Number {
		case 1062:
			return &c.Error{Code: "constraint_conflict", Message: "写入与当前唯一约束冲突"}, true
		case 1451:
			return &c.Error{Code: "link_restrict", Message: "对象仍有关联关系，需先移除关系再删除"}, true
		case 1452:
			return &c.Error{Code: "link_endpoint_conflict", Message: "关系端点已改变或不存在，请重新读取"}, true
		}
	}
	return nil, false
}
