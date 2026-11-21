package data

import (
	"net/http"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func (h *handler) bindOntologySQLRelation(r *http.Request, p c.Principal, root c.CurrentDefinition, stmt *ontologySQLStatement) (*c.RelationFilter, error) {
	relation := stmt.Relation
	if nameKey(relation.Alias) != nameKey(stmt.Alias) {
		return nil, problem(422, "unknown_qualifier", "关联存在性筛选必须使用根对象别名")
	}
	links, err := h.sqlAuthorizedLinks(r, p)
	if err != nil {
		return nil, err
	}
	var result *c.RelationFilter
	for _, candidate := range links {
		d := candidate.Current.Definition
		for _, endpoint := range []struct{ id, name, direction string }{
			{d.Link.SourceSchemaID, d.Link.ForwardName, "out"},
			{d.Link.TargetSchemaID, d.Link.ReverseName, "in"},
		} {
			if endpoint.id != root.Definition.ID || nameKey(endpoint.name) != nameKey(relation.Entry) {
				continue
			}
			if result != nil {
				return nil, problem(422, "ambiguous_link", "关联方向入口名不唯一")
			}
			result = &c.RelationFilter{LinkID: d.ID, Direction: endpoint.direction, Operator: relation.Operator}
		}
	}
	if result == nil {
		return nil, problem(422, "link_unavailable", "关联入口不存在或当前不可查询")
	}
	return result, nil
}
