package data

import (
	"context"
	"database/sql"
	"sort"
	"strings"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

type preparedTransactionLink struct {
	request                                  c.TransactionLinkCreate
	current                                  c.CurrentDefinition
	plan                                     *linkPlan
	decision, sourceDecision, targetDecision c.Decision
}

// Endpoint snapshots remain private in the same budgeted receipt. Replay checks
// both historical and current row visibility before returning relation identity.
type transactionSavedLink struct {
	Result c.TransactionLinkResult `json:"result"`
	Source c.Instance              `json:"source"`
	Target c.Instance              `json:"target"`
}

func transactionEndpoint(endpoint c.TransactionEndpoint, schemaID string, operations map[string]c.TransactionOperation) (c.TransactionEndpoint, error) {
	if (endpoint.ID == "") == (endpoint.ClientKey == "") {
		return endpoint, problem(422, "invalid_transaction_endpoint", "关系端点须选择实例 ID 或对象操作 clientKey")
	}
	if endpoint.ID != "" {
		if !uuidPattern.MatchString(endpoint.ID) {
			return endpoint, problem(422, "invalid_transaction_endpoint", "关系端点须为 UUID")
		}
		endpoint.ID = strings.ToLower(endpoint.ID)
		return endpoint, nil
	}
	op, found := operations[endpoint.ClientKey]
	if !validWriteKey(endpoint.ClientKey) || !found || op.SchemaID != schemaID || op.Operation != "create" && op.Operation != "upsert" {
		return endpoint, problem(422, "invalid_transaction_endpoint", "关系端点引用须指向对应模型的 create/upsert 项")
	}
	return endpoint, nil
}

func (h *handler) prepareTransactionLinks(ctx context.Context, p c.Principal, body c.WriteTransactionRequest, mutations []preparedTransactionOperation, schemas []c.CurrentDefinition) ([]preparedTransactionLink, []c.CurrentDefinition, error) {
	if len(body.Links) == 0 {
		return nil, schemas, nil
	}
	operations := map[string]c.TransactionOperation{}
	clients, targets := map[string]bool{}, map[string]bool{}
	definitions := map[string]c.CurrentDefinition{}
	state := ""
	for _, op := range body.Operations {
		operations[op.ClientKey], clients[op.ClientKey], targets[op.SchemaID] = op, true, true
	}
	for _, current := range schemas {
		definitions[current.Definition.ID] = current
	}
	for _, mutation := range mutations {
		for _, decision := range mutation.decisions {
			state = decision.State
		}
	}
	links := make([]preparedTransactionLink, 0, len(body.Links))
	for _, request := range body.Links {
		if !validWriteKey(request.ClientKey) || clients[request.ClientKey] {
			return nil, nil, problem(422, "invalid_client_key", "对象和关系项目的 clientKey 须在整笔事务内唯一")
		}
		clients[request.ClientKey] = true
		if !uuidPattern.MatchString(request.LinkID) {
			return nil, nil, problem(422, "invalid_transaction_link", "关系事务须使用稳定 Link ID")
		}
		current, err := h.currentLink(ctx, p, request.LinkID)
		if err != nil {
			return nil, nil, err
		}
		if current.Definition.ID != request.LinkID {
			return nil, nil, problem(503, "metadata_unavailable", "关系权威响应标识不匹配")
		}
		decision, err := h.authorizeLink(ctx, p, "link.create", request.LinkID)
		if err != nil {
			return nil, nil, err
		}
		if current.Definition.Link.StorageBinding.Mode != "managed" {
			return nil, nil, problem(422, "transaction_link_storage_unsupported", "关系事务仅支持默认平台 managed Link")
		}
		plan, err := h.planLink(ctx, current.Definition)
		if err != nil {
			return nil, nil, err
		}
		sourceDecision, err := h.authorize(ctx, p, "instance.read", plan.source.Definition.ID)
		if err != nil {
			return nil, nil, err
		}
		targetDecision, err := h.authorize(ctx, p, "instance.read", plan.target.Definition.ID)
		if err != nil {
			return nil, nil, err
		}
		for _, d := range []c.Decision{decision, sourceDecision, targetDecision} {
			if state != "" && state != d.State {
				return nil, nil, problem(409, "authorization_changed", "事务授权状态已改变")
			}
			state = d.State
		}
		for _, endpoint := range []struct {
			current  c.CurrentDefinition
			decision c.Decision
		}{{plan.source, sourceDecision}, {plan.target, targetDecision}} {
			if previous, found := definitions[endpoint.current.Definition.ID]; found && previous.ETag != endpoint.current.ETag {
				return nil, nil, problem(409, "definition_changed", "事务端点模型已改变")
			}
			definitions[endpoint.current.Definition.ID] = endpoint.current
			if err := validateWriteContext(endpoint.current, []c.Decision{decision, endpoint.decision}, targets); err != nil {
				return nil, nil, err
			}
		}
		request.Source, err = transactionEndpoint(request.Source, plan.source.Definition.ID, operations)
		if err == nil {
			request.Target, err = transactionEndpoint(request.Target, plan.target.Definition.ID, operations)
		}
		if err != nil {
			return nil, nil, err
		}
		links = append(links, preparedTransactionLink{request: request, current: current, plan: plan, decision: decision, sourceDecision: sourceDecision, targetDecision: targetDecision})
	}
	schemas = make([]c.CurrentDefinition, 0, len(definitions))
	for _, current := range definitions {
		schemas = append(schemas, current)
	}
	sort.Slice(schemas, func(i, j int) bool { return schemas[i].Definition.ID < schemas[j].Definition.ID })
	return links, schemas, nil
}

// All endpoint Schema gates precede all Link gates, matching standalone Link
// writes/publication. Sorting every definition together would invert that order.
func transactionLinkGates(links []preparedTransactionLink) []c.CurrentDefinition {
	seen := map[string]bool{}
	gates := []c.CurrentDefinition{}
	for _, link := range links {
		if !seen[link.current.Definition.ID] {
			gates = append(gates, link.current)
			seen[link.current.Definition.ID] = true
		}
	}
	sort.Slice(gates, func(i, j int) bool { return gates[i].Definition.ID < gates[j].Definition.ID })
	return gates
}

func (h *handler) recheckTransactionLinks(ctx context.Context, p c.Principal, links []preparedTransactionLink) error {
	checked := map[string]bool{}
	for _, link := range links {
		if err := h.recheckLink(ctx, p, "link.create", link.current.Definition.ID, link.decision); err != nil {
			return err
		}
		for _, endpoint := range []struct {
			current  c.CurrentDefinition
			decision c.Decision
		}{{link.plan.source, link.sourceDecision}, {link.plan.target, link.targetDecision}} {
			if err := h.recheck(ctx, p, "instance.read", endpoint.current.Definition.ID, endpoint.decision); err != nil {
				return err
			}
		}
		for _, before := range []c.CurrentDefinition{link.current, link.plan.source, link.plan.target} {
			if checked[before.Definition.ID] {
				continue
			}
			fresh, err := h.current(ctx, p.TenantID, before.Definition.ID)
			if err != nil {
				return err
			}
			if fresh.Definition.ID != before.Definition.ID || fresh.ETag != before.ETag {
				return problem(409, "definition_changed", "事务关系或端点定义已改变")
			}
			checked[before.Definition.ID] = true
		}
	}
	return nil
}

func resolveTransactionEndpoint(endpoint c.TransactionEndpoint, schemaID string, completed map[string]c.Instance) (string, error) {
	if endpoint.ID != "" {
		return endpoint.ID, nil
	}
	instance, found := completed[endpoint.ClientKey]
	if !found || instance.SchemaID != schemaID || !uuidPattern.MatchString(instance.ID) {
		return "", problem(500, "transaction_reference_unresolved", "无法核对事务关系端点引用")
	}
	return instance.ID, nil
}

func applyTransactionLinks(ctx context.Context, tx *sql.Tx, p c.Principal, links []preparedTransactionLink, saved *transactionSaved) ([]c.TransactionLinkResult, error) {
	completed := map[string]c.Instance{}
	for _, mutation := range saved.Items {
		if len(mutation.Items) == 1 {
			completed[mutation.Items[0].ClientKey] = mutation.Items[0].Instance
		}
	}
	results := make([]c.TransactionLinkResult, 0, len(links))
	for _, link := range links {
		sourceID, err := resolveTransactionEndpoint(link.request.Source, link.plan.source.Definition.ID, completed)
		if err != nil {
			return nil, err
		}
		targetID, err := resolveTransactionEndpoint(link.request.Target, link.plan.target.Definition.ID, completed)
		if err != nil {
			return nil, err
		}
		source, err := selectInstance(ctx, tx, link.plan.source.Definition, p.TenantID, sourceID, link.sourceDecision.RowFilter, true)
		if err != nil {
			return nil, err
		}
		target, err := selectInstance(ctx, tx, link.plan.target.Definition, p.TenantID, targetID, link.targetDecision.RowFilter, true)
		if err != nil {
			return nil, err
		}
		out := c.LinkInstance{ID: c.NewID(), LinkID: link.current.Definition.ID, SourceID: sourceID, TargetID: targetID, ETag: c.NewID()}
		if _, err = tx.ExecContext(ctx, "INSERT INTO "+quote(linkTableName(out.LinkID))+" (tenant_id,id,source_id,target_id,etag) VALUES (?,?,?,?,?)", p.TenantID, out.ID, out.SourceID, out.TargetID, out.ETag); err != nil {
			return nil, err
		}
		if err = emitLink(ctx, tx, p.TenantID, "link.created", out); err != nil {
			return nil, err
		}
		result := c.TransactionLinkResult{ClientKey: link.request.ClientKey, Instance: out}
		saved.Links = append(saved.Links, transactionSavedLink{Result: result, Source: source, Target: target})
		budget := jsonBudget{remaining: c.MaxDataResponseBytes - 1}
		if err = budget.add(saved); err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	return results, nil
}

func replayTransactionLinks(ctx context.Context, tx *sql.Tx, p c.Principal, links []preparedTransactionLink, saved transactionSaved) ([]c.TransactionLinkResult, error) {
	if len(links) != len(saved.Links) {
		return nil, problem(500, "idempotency_corrupt", "无法核对原事务关系结果")
	}
	results := make([]c.TransactionLinkResult, 0, len(links))
	completed := map[string]c.Instance{}
	for _, mutation := range saved.Items {
		if len(mutation.Items) == 1 {
			completed[mutation.Items[0].ClientKey] = mutation.Items[0].Instance
		}
	}
	for i, link := range links {
		history := saved.Links[i]
		out := history.Result.Instance
		sourceID, sourceErr := resolveTransactionEndpoint(link.request.Source, link.plan.source.Definition.ID, completed)
		targetID, targetErr := resolveTransactionEndpoint(link.request.Target, link.plan.target.Definition.ID, completed)
		if sourceErr != nil || targetErr != nil || !uuidPattern.MatchString(out.ID) || !uuidPattern.MatchString(out.ETag) || sourceID != out.SourceID || targetID != out.TargetID || history.Result.ClientKey != link.request.ClientKey || out.LinkID != link.current.Definition.ID || history.Source.ID != out.SourceID || history.Target.ID != out.TargetID || history.Source.SchemaID != link.plan.source.Definition.ID || history.Target.SchemaID != link.plan.target.Definition.ID {
			return nil, problem(500, "idempotency_corrupt", "无法核对原事务关系结果")
		}
		for _, endpoint := range []struct {
			current    c.CurrentDefinition
			decision   c.Decision
			historical c.Instance
		}{{link.plan.source, link.sourceDecision, history.Source}, {link.plan.target, link.targetDecision, history.Target}} {
			for _, property := range endpoint.current.Definition.Schema.Properties {
				if _, exists := endpoint.historical.Values[property.ID]; !exists {
					return nil, problem(409, "definition_changed", "端点模型已改变，不能交付旧关系回执")
				}
			}
			if err := checkRowFilter(ctx, tx, endpoint.current.Definition.Schema, endpoint.historical.Values, endpoint.decision.RowFilter); err != nil {
				return nil, err
			}
			if _, err := selectInstance(ctx, tx, endpoint.current.Definition, p.TenantID, endpoint.historical.ID, endpoint.decision.RowFilter, true); err != nil {
				return nil, err
			}
		}
		var actualSourceID, actualTargetID, etag string
		err := tx.QueryRowContext(ctx, "SELECT source_id,target_id,etag FROM "+quote(linkTableName(out.LinkID))+" WHERE tenant_id=? AND id=? FOR UPDATE", p.TenantID, out.ID).Scan(&actualSourceID, &actualTargetID, &etag)
		if err == sql.ErrNoRows {
			return nil, problem(404, "not_found", "关系实例不存在或不可访问")
		}
		if err != nil {
			return nil, err
		}
		if actualSourceID != out.SourceID || actualTargetID != out.TargetID || etag != out.ETag {
			return nil, problem(409, "link_changed", "关系实例已改变，不能交付旧回执")
		}
		results = append(results, history.Result)
	}
	return results, nil
}
