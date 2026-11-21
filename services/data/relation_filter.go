package data

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func validateRelationFilter(q c.QuerySpec) error {
	f := q.Relation
	if f == nil {
		return nil
	}
	if !uuidPattern.MatchString(f.LinkID) || (f.Direction != "out" && f.Direction != "in") ||
		(f.Operator != "exists" && f.Operator != "not_exists") {
		return problem(422, "invalid_relation_filter", "关联筛选需要稳定 Link ID、out/in 方向及 exists/not_exists 操作")
	}
	if q.Via != nil || q.Rows != nil || q.Aggregate != nil {
		return problem(422, "unsupported_relation_filter", "关联筛选仅用于根对象查询")
	}
	return nil
}

type relationFilterPlan struct {
	link, neighbor, root c.CurrentDefinition
	linkPlan             *linkPlan
	neighborDecision     c.Decision
	linkDecision         c.Decision
	discovery            [3]c.Decision
}

func (h *handler) planRelationFilter(ctx context.Context, p c.Principal, root c.CurrentDefinition, f *c.RelationFilter) (*relationFilterPlan, error) {
	link, err := h.currentLink(ctx, p, f.LinkID)
	if err != nil {
		return nil, err
	}
	linkDiscovery, err := h.authorizeLink(ctx, p, "definition.read", f.LinkID)
	if err != nil {
		return nil, err
	}
	linkDecision, err := h.authorizeLink(ctx, p, "link.query", f.LinkID)
	if err != nil {
		return nil, err
	}
	plan, err := h.logicalLinkPlan(ctx, link.Definition)
	if err != nil {
		return nil, err
	}
	start, neighbor, err := linkDirection(plan, f.Direction)
	if err != nil {
		return nil, err
	}
	if start.Definition.ID != root.Definition.ID || start.ETag != root.ETag {
		return nil, problem(422, "invalid_link_endpoint", "查询模型必须是关联筛选方向的起点")
	}
	discovery := [3]c.Decision{linkDiscovery}
	for i, d := range []c.CurrentDefinition{root, neighbor} {
		decision, err := h.authorize(ctx, p, "definition.read", d.Definition.ID)
		if err != nil {
			return nil, err
		}
		if decision.RowFilter != nil || decision.NewRowFilter != nil {
			return nil, problem(422, "unsupported_authorization", "定义发现不支持行条件")
		}
		discovery[i+1] = decision
	}
	neighborDecision, err := h.authorize(ctx, p, "instance.query", neighbor.Definition.ID)
	if err != nil {
		return nil, err
	}
	if err := validateOntologySQLDecision(neighbor.Definition, neighborDecision); err != nil {
		return nil, err
	}
	return &relationFilterPlan{link: link, neighbor: neighbor, root: root, linkPlan: plan,
		neighborDecision: neighborDecision, linkDecision: linkDecision, discovery: discovery}, nil
}

func (h *handler) recheckRelationFilter(ctx context.Context, p c.Principal, plan *relationFilterPlan) error {
	if err := h.recheckLink(ctx, p, "definition.read", plan.link.Definition.ID, plan.discovery[0]); err != nil {
		return err
	}
	if err := h.recheckLink(ctx, p, "link.query", plan.link.Definition.ID, plan.linkDecision); err != nil {
		return err
	}
	for i, d := range []c.CurrentDefinition{plan.root, plan.neighbor} {
		if err := h.recheck(ctx, p, "definition.read", d.Definition.ID, plan.discovery[i+1]); err != nil {
			return err
		}
	}
	if err := h.recheck(ctx, p, "instance.query", plan.neighbor.Definition.ID, plan.neighborDecision); err != nil {
		return err
	}
	for _, d := range []c.CurrentDefinition{plan.link, plan.root, plan.neighbor} {
		now, err := h.current(ctx, p.TenantID, d.Definition.ID)
		if err != nil {
			return err
		}
		if now.ETag != d.ETag || c.EnumContextDigest(now.Definition) != c.EnumContextDigest(d.Definition) {
			return problem(409, "definition_changed", "关联定义已改变，请重新提交查询")
		}
	}
	return nil
}

func (h *handler) queryRelation(ctx context.Context, p c.Principal, root c.CurrentDefinition, decision c.Decision, q c.QuerySpec) (c.QueryResult, error) {
	var result c.QueryResult
	plan, err := h.planRelationFilter(ctx, p, root, q.Relation)
	if err != nil {
		return result, err
	}
	physical, err := h.openLinkPlan(ctx, plan.linkPlan)
	if err != nil {
		return result, err
	}
	defer physical.close()
	// Link traversal correlates its returned endpoint with the root.
	direction := "in"
	if q.Relation.Direction == "in" {
		direction = "out"
	}
	mapped := root.Definition.Schema.StorageBinding != nil
	var predicate sqlPredicate
	if mapped {
		predicate, err = externalLinkPredicate(physical, direction, "", plan.neighborDecision)
	} else {
		predicate, err = managedLinkPredicate(physical, direction, "", plan.neighborDecision)
	}
	if err != nil {
		return result, err
	}
	if q.Relation.Operator == "not_exists" {
		predicate.SQL = "NOT " + predicate.SQL
	}
	bound := linkBoundDecision(decision, plan.neighborDecision, plan.linkDecision, plan.linkPlan, plan.link)
	bound.State = digest(struct {
		State     string
		Discovery [3]c.Decision
	}{bound.State, plan.discovery})
	var lastValue json.RawMessage
	var fingerprint string
	err = h.cfg.Engine.WithTx(ctx, func(tx *sql.Tx) error {
		if err := h.linkGates(ctx, tx, p, plan.linkPlan, &plan.link); err != nil {
			return err
		}
		if mapped {
			tables := physical.sourceTables
			if q.Relation.Direction == "in" {
				tables = physical.targetTables
			}
			if err := prepareMappedWriteProjection(ctx, tables, root); err != nil {
				return err
			}
			compiled, err := compileMappedQueryPredicate(root.Definition, tables, root.ETag, p, bound, q, h.cfg.CursorSecret, "", &predicate)
			if err != nil {
				return err
			}
			fingerprint = compiled.Fingerprint
			result, lastValue, err = readMappedProjection(ctx, tables, compiled, root.Definition.ID)
			if err != nil {
				return err
			}
		} else {
			compiled, err := compileQueryPredicate(root.Definition, root.ETag, p, bound, q, h.cfg.CursorSecret, &predicate)
			if err != nil {
				return err
			}
			fingerprint = compiled.Fingerprint
			result, lastValue, err = readLocalProjection(ctx, tx, compiled, root.Definition.ID)
			if err != nil {
				return err
			}
		}
		if err := h.recheck(ctx, p, "instance.query", root.Definition.ID, decision); err != nil {
			return err
		}
		return h.recheckRelationFilter(ctx, p, plan)
	})
	if err == nil && result.HasMore && len(result.Data) > 0 {
		result.NextCursor, err = signCursor(h.cfg.CursorSecret, pageCursor{
			Fingerprint: fingerprint, TenantID: p.TenantID, Subject: p.Subject,
			SchemaID: root.Definition.ID, Digest: bareETag(root.ETag), Authorization: digest(bound),
			LastID: result.Data[len(result.Data)-1].ID, LastValue: lastValue, Expires: time.Now().Add(time.Hour).Unix(),
		})
	}
	return result, err
}
