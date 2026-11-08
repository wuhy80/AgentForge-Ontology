package data

import (
	"context"
	"encoding/json"
	"errors"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

// Discovery describes conditional create fields; submitted endpoint IDs are
// checked again with row permissions under the source transaction.
func (h *handler) mappedCreateProtectedWriteFields(ctx context.Context, p c.Principal, current c.CurrentDefinition) (map[string]bool, error) {
	protected, err := h.mappedProtectedWriteFields(ctx, current)
	if err != nil {
		return nil, err
	}
	links, err := h.mappedWriteLinks(ctx, p.TenantID, current.Definition.ID)
	if err != nil {
		return nil, err
	}
	denied := map[string]bool{}
	for _, definition := range links {
		link := definition.Link
		b := link.StorageBinding
		owner, target := link.SourceSchemaID, link.TargetSchemaID
		if b.ForeignKeySide == "target" {
			owner, target = target, owner
		}
		if b.Mode != "foreign_key" || owner != current.Definition.ID {
			continue
		}
		_, err := h.authorizeLink(ctx, p, "link.create", definition.ID)
		if err == nil {
			_, err = h.authorize(ctx, p, "instance.read", owner)
		}
		if err == nil {
			_, err = h.authorize(ctx, p, "instance.read", target)
		}
		if err != nil {
			var api *apiError
			if !errors.As(err, &api) || api.status != 403 {
				return nil, err
			}
			denied[b.ForeignKeyColumn] = true
		}
	}
	for _, property := range current.Definition.Schema.Properties {
		if protected[property.ID] && !denied[property.Mapping.Column] {
			delete(protected, property.ID)
		}
	}
	return protected, nil
}

// External Link facts have not yet received a source-local mutation protocol.
// Keep destructive endpoint changes unavailable rather than relying on a
// preflight read that could race an independently written relationship.
func (h *handler) mappedWriteLinks(ctx context.Context, tenant, schema string) ([]c.Definition, error) {
	rows, err := h.cfg.Engine.DB().QueryContext(ctx, "SELECT definition FROM af_data_gates WHERE tenant_id=? AND JSON_UNQUOTE(JSON_EXTRACT(definition,'$.kind'))='link' AND (JSON_UNQUOTE(JSON_EXTRACT(definition,'$.link.sourceSchemaId'))=? OR JSON_UNQUOTE(JSON_EXTRACT(definition,'$.link.targetSchemaId'))=?) LIMIT 257", tenant, schema, schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []c.Definition{}
	for rows.Next() {
		if len(result) >= 256 {
			return nil, problem(422, "mapped_relation_budget", "关联约束数量超过来源写入预算")
		}
		var raw string
		var d c.Definition
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if json.Unmarshal([]byte(raw), &d) != nil || d.Link == nil {
			return nil, problem(503, "mapping_unavailable", "无法核对来源关系约束")
		}
		if d.Link.StorageBinding != nil {
			result = append(result, d)
		}
	}
	return result, rows.Err()
}

func (h *handler) mappedDeleteReadiness(ctx context.Context, current c.CurrentDefinition) (bool, error) {
	links, err := h.mappedWriteLinks(ctx, current.Definition.TenantID, current.Definition.ID)
	return len(links) == 0, err
}

func (h *handler) mappedProtectedWriteFields(ctx context.Context, current c.CurrentDefinition) (map[string]bool, error) {
	links, err := h.mappedWriteLinks(ctx, current.Definition.TenantID, current.Definition.ID)
	if err != nil {
		return nil, err
	}
	columns := map[string]bool{}
	for _, definition := range links {
		link := definition.Link
		b := link.StorageBinding
		owner := link.SourceSchemaID
		if b.ForeignKeySide == "target" {
			owner = link.TargetSchemaID
		}
		if b.Mode == "foreign_key" && owner == current.Definition.ID {
			columns[b.ForeignKeyColumn] = true
		}
	}
	protected := map[string]bool{}
	for _, property := range current.Definition.Schema.Properties {
		if property.Mapping != nil && columns[property.Mapping.Column] {
			protected[property.ID] = true
		}
	}
	return protected, nil
}

func (s *mappedMutationStore) checkDeleteRelations(ctx context.Context) error {
	allowed, err := s.h.mappedDeleteReadiness(ctx, s.current)
	if err != nil {
		return err
	}
	if !allowed {
		return problem(422, "mapped_relation_mutation_unsupported", "来源关系的原子变更尚未认证，暂不允许删除关系端点")
	}
	return nil
}

func (s *mappedMutationStore) checkRelationChanges(ctx context.Context, changed []string) error {
	links, err := s.h.mappedWriteLinks(ctx, s.p.TenantID, s.current.Definition.ID)
	if err != nil {
		return err
	}
	columns := map[string]bool{}
	for _, id := range changed {
		p, ok := findProperty(s.current.Definition.Schema, id)
		if ok {
			columns[p.Mapping.Column] = true
		}
	}
	for _, definition := range links {
		link := definition.Link
		b := link.StorageBinding
		if b.Mode != "foreign_key" {
			continue
		}
		owner := link.SourceSchemaID
		if b.ForeignKeySide == "target" {
			owner = link.TargetSchemaID
		}
		if owner == s.current.Definition.ID && columns[b.ForeignKeyColumn] {
			return problem(422, "mapped_relation_mutation_unsupported", "来源外键关系须经已认证的关系事务修改")
		}
	}
	return nil
}
