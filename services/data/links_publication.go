package data

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

// Endpoint gates always precede the Link gate. Managed instance writes hold an
// endpoint gate too, so relation changes and endpoint deletion serialize without
// relying on a check performed outside the transaction.
func (h *handler) linkGates(ctx context.Context, tx *sql.Tx, p c.Principal, plan *linkPlan, link *c.CurrentDefinition) error {
	endpoints := []c.CurrentDefinition{plan.source, plan.target}
	sort.Slice(endpoints, func(i, j int) bool { return endpoints[i].Definition.ID < endpoints[j].Definition.ID })
	previous := ""
	for _, current := range endpoints {
		if current.Definition.ID == previous {
			continue
		}
		previous = current.Definition.ID
		if err := h.gate(ctx, tx, p, current); err != nil {
			return err
		}
	}
	if link != nil {
		return h.gate(ctx, tx, p, *link)
	}
	return nil
}

func (h *handler) previewLink(ctx context.Context, req c.PrepareRequest) (c.PrepareResult, error) {
	result := c.PrepareResult{OperationID: req.OperationID, Status: "preview", Statements: []string{}}
	plan, err := h.planLink(ctx, req.Definition)
	if err != nil {
		return result, err
	}
	defer plan.close()
	var signature, status string
	err = h.cfg.Engine.DB().QueryRowContext(ctx, "SELECT storage_signature,status FROM af_data_gates WHERE tenant_id=? AND definition_id=?", req.Definition.TenantID, req.Definition.ID).Scan(&signature, &status)
	if err == nil {
		if status != "ready" {
			return result, problem(409, "publication_busy", "关系映射正在生效，请先恢复发布")
		}
		if signature != linkStorageSignature(req.Definition) {
			return result, problem(422, "unsupported_migration", "已生效的关系端点、存储映射和基数不能直接修改")
		}
		if req.Definition.Link.StorageBinding.Mode == "managed" {
			err = verifyLinkTable(ctx, h.cfg.Engine.DB(), req.Definition)
		}
		return result, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	if req.Definition.Link.StorageBinding.Mode == "managed" {
		var exists int
		if err = h.cfg.Engine.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name=?", linkTableName(req.Definition.ID)).Scan(&exists); err != nil {
			return result, err
		}
		if exists != 0 {
			return result, problem(409, "unmanaged_table", "目标存在未纳管关系表，不能直接接管")
		}
		statement, e := linkDDL(req.Definition)
		if e != nil {
			return result, e
		}
		result.Statements = []string{statement}
	}
	return result, nil
}

func (h *handler) prepareLink(ctx context.Context, req c.PrepareRequest) (result c.PrepareResult, failure error) {
	result = c.PrepareResult{OperationID: req.OperationID, Status: "prepared", Statements: []string{}}
	closed := false
	defer func() {
		if failure == nil {
			return
		}
		if !closed {
			var operation, status, digest string
			err := h.cfg.Engine.DB().QueryRowContext(ctx, "SELECT operation_id,status,digest FROM af_data_gates WHERE tenant_id=? AND definition_id=?", req.Definition.TenantID, req.Definition.ID).Scan(&operation, &status, &digest)
			closed = err == nil && operation == req.OperationID && digest == req.Digest && (status == "preparing" || status == "prepared")
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				// Failure to establish that the gate is absent is not evidence that
				// rejecting the operation and releasing Metadata's guard is safe.
				closed = true
			}
		}
		if closed {
			// Do not wrap apiError with %%w: writeError deliberately inspects its
			// chain. A closed publication must remain a 5xx resumable operation.
			failure = fmt.Errorf("resume closed link publication: %v", failure)
		}
	}()
	plan, err := h.planLink(ctx, req.Definition)
	if err != nil {
		var rejected *apiError
		if errors.As(err, &rejected) && (rejected.status == 403 || rejected.status == 404 || (rejected.status == 409 && rejected.code != "publication_conflict" && rejected.code != "publication_busy")) {
			err = problem(422, rejected.code, rejected.message)
		}
		return result, err
	}
	defer plan.close()
	// Upgrading a metadata-only active Link has a previous digest but no gate.
	// Validate that precise prior definition rather than silently taking ownership.
	upgrade := false
	if req.PreviousDigest != "" {
		var present int
		if err = h.cfg.Engine.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM af_data_gates WHERE tenant_id=? AND definition_id=?", req.Definition.TenantID, req.Definition.ID).Scan(&present); err != nil {
			return result, err
		}
		if present == 0 {
			previous, e := h.current(ctx, req.Definition.TenantID, req.Definition.ID)
			if e != nil {
				return result, e
			}
			if previous.ETag != req.PreviousDigest || previous.Definition.Link == nil || previous.Definition.Link.StorageBinding != nil {
				return result, problem(409, "publication_conflict", "原关系定义并非可升级的未绑定当前定义")
			}
			before, after := *previous.Definition.Link, *req.Definition.Link
			before.ForwardName, before.ReverseName = "", ""
			after.ForwardName, after.ReverseName = "", ""
			after.StorageBinding = nil
			if digest(before) != digest(after) {
				return result, problem(422, "unsupported_migration", "补充映射不能同时改变原关系结构")
			}
			upgrade = true
		}
	}
	initial, ready := false, false
	signature := linkStorageSignature(req.Definition)
	err = h.cfg.Engine.WithTx(ctx, func(tx *sql.Tx) error {
		// Source configuration updates lock this same row before checking gates.
		// Persisting the binding under it closes the concurrent retargeting gap.
		if len(plan.sourceTables) != 0 {
			var etag string
			if err := tx.QueryRowContext(ctx, "SELECT etag FROM af_data_sources WHERE tenant_id=? AND id=? FOR UPDATE", req.Definition.TenantID, plan.sourceTables[0].source.ID).Scan(&etag); err != nil {
				return err
			}
			if etag != plan.sourceTables[0].source.ETag {
				return problem(409, "source_changed", "关系数据源配置已改变，请重新预览")
			}
		}
		if err := h.linkGates(ctx, tx, c.Principal{TenantID: req.Definition.TenantID}, plan, nil); err != nil {
			return err
		}
		var oldDigest, oldSignature, oldOperation, status string
		err := tx.QueryRowContext(ctx, "SELECT digest,storage_signature,operation_id,status FROM af_data_gates WHERE tenant_id=? AND definition_id=? FOR UPDATE", req.Definition.TenantID, req.Definition.ID).Scan(&oldDigest, &oldSignature, &oldOperation, &status)
		body, _ := json.Marshal(req.Definition)
		if errors.Is(err, sql.ErrNoRows) {
			if req.PreviousDigest != "" && !upgrade {
				return problem(409, "publication_conflict", "原关系映射不存在")
			}
			if req.Definition.Link.StorageBinding.Mode == "managed" {
				var exists int
				if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name=?", linkTableName(req.Definition.ID)).Scan(&exists); err != nil {
					return err
				}
				if exists != 0 {
					return problem(409, "unmanaged_table", "目标存在未纳管关系表，不能直接接管")
				}
			}
			_, err = tx.ExecContext(ctx, "INSERT INTO af_data_gates (tenant_id,definition_id,digest,previous_digest,operation_id,status,definition,storage_signature) VALUES (?,?,?,?,?,'preparing',?,?)", req.Definition.TenantID, req.Definition.ID, req.Digest, req.PreviousDigest, req.OperationID, string(body), signature)
			initial = true
			return err
		}
		if err != nil {
			return err
		}
		if oldOperation == req.OperationID {
			if oldDigest != req.Digest || oldSignature != signature {
				return problem(409, "publication_conflict", "同一发布操作不能改变关系内容")
			}
			ready = status == "ready"
			return nil
		}
		if status != "ready" {
			return problem(409, "publication_busy", "其他关系发布尚未恢复")
		}
		if oldDigest != req.PreviousDigest {
			return problem(409, "publication_conflict", "当前关系映射摘要已改变")
		}
		if oldSignature != signature {
			return problem(422, "unsupported_migration", "已生效的关系端点、存储映射和基数不能直接修改")
		}
		_, err = tx.ExecContext(ctx, "UPDATE af_data_gates SET digest=?,previous_digest=?,operation_id=?,status='preparing',definition=?,storage_signature=? WHERE tenant_id=? AND definition_id=?", req.Digest, req.PreviousDigest, req.OperationID, string(body), signature, req.Definition.TenantID, req.Definition.ID)
		return err
	})
	if err != nil {
		return result, err
	}
	closed = !ready
	if ready {
		result.Status = "ready"
		return result, nil
	}
	// The gate is durably closed. All later failures must retain the resumable
	// publication, so wrap even validation errors into an internal storage failure.
	if req.Definition.Link.StorageBinding.Mode == "managed" {
		statement, e := linkDDL(req.Definition)
		if e != nil {
			return result, fmt.Errorf("compile closed link publication: %w", e)
		}
		var exists int
		err = h.cfg.Engine.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name=?", linkTableName(req.Definition.ID)).Scan(&exists)
		if err == nil && exists == 0 {
			_, err = h.cfg.Engine.DB().ExecContext(ctx, statement)
		}
		if err != nil {
			return result, fmt.Errorf("apply link publication: %w", err)
		}
		if err = verifyLinkTable(ctx, h.cfg.Engine.DB(), req.Definition); err != nil {
			return result, fmt.Errorf("verify link publication: %w", err)
		}
		if initial {
			result.Statements = []string{statement}
		}
	} else {
		fresh, e := h.planLink(ctx, req.Definition)
		if e != nil {
			return result, fmt.Errorf("verify closed external link: %w", e)
		}
		fresh.close()
	}
	_, err = h.cfg.Engine.DB().ExecContext(ctx, "UPDATE af_data_gates SET status='prepared' WHERE tenant_id=? AND definition_id=? AND operation_id=? AND digest=? AND status IN ('preparing','prepared')", req.Definition.TenantID, req.Definition.ID, req.OperationID, req.Digest)
	return result, err
}
