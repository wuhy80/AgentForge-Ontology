package data

import (
	"context"
	"database/sql"
	"errors"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func transactionLinkAuthorization(link preparedTransactionLink) map[string]string {
	return map[string]string{"link": digest(link.decision), "source": digest(link.sourceDecision), "target": digest(link.targetDecision)}
}

// Deletes precede all object mutations and creates, so restrict FKs and 1:1
// uniqueness are satisfied without exposing an intermediate committed state.
func applyTransactionLinkDeletes(ctx context.Context, tx *sql.Tx, p c.Principal, links []preparedTransactionLink, saved *transactionSaved) ([]c.TransactionLinkResult, error) {
	results := []c.TransactionLinkResult{}
	for _, link := range links {
		if link.deletion == nil {
			continue
		}
		out := c.LinkInstance{LinkID: link.current.Definition.ID}
		err := tx.QueryRowContext(ctx, "SELECT id,source_id,target_id,etag FROM "+quote(linkTableName(out.LinkID))+" WHERE tenant_id=? AND id=? FOR UPDATE", p.TenantID, link.deletion.ID).Scan(&out.ID, &out.SourceID, &out.TargetID, &out.ETag)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, problem(404, "not_found", "关系实例不存在或不可访问")
		}
		if err != nil {
			return nil, err
		}
		source, err := selectInstance(ctx, tx, link.plan.source.Definition, p.TenantID, out.SourceID, link.sourceDecision.RowFilter, true)
		if err != nil {
			return nil, err
		}
		target, err := selectInstance(ctx, tx, link.plan.target.Definition, p.TenantID, out.TargetID, link.targetDecision.RowFilter, true)
		if err != nil {
			return nil, err
		}
		if out.ETag != bareETag(link.deletion.IfMatch) {
			return nil, problem(412, "precondition_failed", "关系实例已改变，请重新读取")
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM "+quote(linkTableName(out.LinkID))+" WHERE tenant_id=? AND id=? AND etag=?", p.TenantID, out.ID, out.ETag); err != nil {
			return nil, err
		}
		if err = emitLink(ctx, tx, p.TenantID, "link.deleted", out); err != nil {
			return nil, err
		}
		result := c.TransactionLinkResult{ClientKey: link.request.ClientKey, Instance: out}
		saved.LinkDeletes = append(saved.LinkDeletes, transactionSavedLink{Result: result, Source: source, Target: target, Authorization: transactionLinkAuthorization(link)})
		budget := jsonBudget{remaining: c.MaxDataResponseBytes - 1}
		if err = budget.add(saved); err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	return results, nil
}

func replayTransactionLinkDeletes(ctx context.Context, tx *sql.Tx, p c.Principal, links []preparedTransactionLink, saved transactionSaved) ([]c.TransactionLinkResult, error) {
	deletes := []preparedTransactionLink{}
	for _, link := range links {
		if link.deletion != nil {
			deletes = append(deletes, link)
		}
	}
	if len(deletes) != len(saved.LinkDeletes) {
		return nil, problem(500, "idempotency_corrupt", "无法核对原事务关系删除结果")
	}
	results := []c.TransactionLinkResult{}
	for i, link := range deletes {
		history := saved.LinkDeletes[i]
		out := history.Result.Instance
		if out.ID != link.deletion.ID || out.ETag != bareETag(link.deletion.IfMatch) || out.LinkID != link.current.Definition.ID || history.Result.ClientKey != link.request.ClientKey || !uuidPattern.MatchString(out.SourceID) || !uuidPattern.MatchString(out.TargetID) || history.Source.ID != out.SourceID || history.Target.ID != out.TargetID || history.Source.SchemaID != link.plan.source.Definition.ID || history.Target.SchemaID != link.plan.target.Definition.ID {
			return nil, problem(500, "idempotency_corrupt", "无法核对原事务关系删除结果")
		}
		// As with object-delete receipts, a deleted row cannot be re-read. A
		// changed authorization digest must never disclose its historical ID.
		for name, value := range transactionLinkAuthorization(link) {
			if history.Authorization[name] != value {
				return nil, problem(409, "authorization_changed", "授权已改变，不能交付原关系删除回执")
			}
		}
		for _, endpoint := range []struct {
			current    c.CurrentDefinition
			decision   c.Decision
			historical c.Instance
		}{
			{link.plan.source, link.sourceDecision, history.Source}, {link.plan.target, link.targetDecision, history.Target},
		} {
			for _, property := range endpoint.current.Definition.Schema.Properties {
				if _, exists := endpoint.historical.Values[property.ID]; !exists {
					return nil, problem(409, "definition_changed", "端点模型已改变，不能交付旧关系回执")
				}
			}
			if err := checkRowFilter(ctx, tx, endpoint.current.Definition.Schema, endpoint.historical.Values, endpoint.decision.RowFilter); err != nil {
				return nil, err
			}
			actual, err := selectInstance(ctx, tx, endpoint.current.Definition, p.TenantID, endpoint.historical.ID, nil, true)
			var api *apiError
			if errors.As(err, &api) && api.status == 404 {
				continue
			}
			if err != nil {
				return nil, err
			}
			if err = checkRowFilter(ctx, tx, endpoint.current.Definition.Schema, actual.Values, endpoint.decision.RowFilter); err != nil {
				return nil, err
			}
		}
		var id string
		err := tx.QueryRowContext(ctx, "SELECT id FROM "+quote(linkTableName(out.LinkID))+" WHERE tenant_id=? AND id=? FOR UPDATE", p.TenantID, out.ID).Scan(&id)
		if err == nil {
			return nil, problem(409, "link_changed", "原删除关系重新出现，不能交付旧回执")
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		results = append(results, history.Result)
	}
	return results, nil
}
