package data

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"unicode/utf8"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

const maxQueryPathCandidates = 20
const maxQueryPathExpansions = 500

type sqlAuthorizedLink struct {
	Current  c.CurrentDefinition
	Category string
	Source   c.QueryContextModel
	Target   c.QueryContextModel
}

// The graph stays on the server. Directory requests use the caller's identity,
// and every returned edge independently requires current discovery/query grants.
// Catalogs, publication gates and source state are rechecked at actual execution.
func (h *handler) sqlAuthorizedLinks(r *http.Request, p c.Principal) ([]sqlAuthorizedLink, error) {
	categories, err := h.sqlCategories(r, p)
	if err != nil {
		return nil, err
	}
	models := map[string]*c.QueryContextModel{}
	model := func(id string) (*c.QueryContextModel, error) {
		if cached, known := models[id]; known {
			return cached, nil
		}
		models[id] = nil
		current, e := h.current(r.Context(), p.TenantID, id)
		if e != nil {
			if hiddenSQLSource(e) {
				return nil, nil
			}
			return nil, e
		}
		d := current.Definition
		if d.ID != id || d.Kind != c.KindSchema || categories[d.CategoryID] == "" {
			return nil, nil
		}
		if e = validateSchema(d); e != nil {
			return nil, nil
		}
		definition, e := h.authorize(r.Context(), p, "definition.read", id)
		if e != nil {
			if hiddenSQLSource(e) {
				return nil, nil
			}
			return nil, e
		}
		if definition.RowFilter != nil || definition.NewRowFilter != nil {
			return nil, problem(422, "unsupported_authorization", "定义目录不支持行条件授权")
		}
		decision, e := h.authorize(r.Context(), p, "instance.query", id)
		if e != nil {
			if hiddenSQLSource(e) {
				return nil, nil
			}
			return nil, e
		}
		if e = validateOntologySQLDecision(d, decision); e != nil {
			return nil, e
		}
		out := queryContextModel(d, decision, categories[d.CategoryID])
		if len(out.Fields) == 0 {
			return nil, nil
		}
		models[id] = &out
		return &out, nil
	}
	result, after, scanned := []sqlAuthorizedLink{}, "", 0
	for page := 0; page < 11; page++ {
		values := url.Values{"kind": {"link"}, "view": {"current"}, "limit": {"100"}, "after": {after}}
		var list struct {
			Data       []c.CurrentDefinition `json:"data"`
			HasMore    bool                  `json:"hasMore"`
			NextCursor string                `json:"nextCursor"`
		}
		if err = h.sqlDirectoryGET(r, "/metadata/definitions?"+values.Encode(), &list); err != nil {
			return nil, err
		}
		for _, current := range list.Data {
			scanned++
			if scanned > sqlDirectoryBudget {
				return nil, problem(422, "directory_budget", "关系目录超过扫描预算，请指定更小的关系范围")
			}
			d := current.Definition
			if d.TenantID != p.TenantID {
				return nil, problem(503, "metadata_unavailable", "关系目录租户不一致")
			}
			if d.Kind != c.KindLink || current.Status != "active" || categories[d.CategoryID] == "" {
				continue
			}
			if validateLinkDefinition(d) != nil {
				continue
			}
			discovery, e := h.authorizeKind(r.Context(), p, "definition.read", d.ID, c.KindLink)
			if e != nil {
				if hiddenSQLSource(e) {
					continue
				}
				return nil, e
			}
			if discovery.RowFilter != nil || discovery.NewRowFilter != nil {
				return nil, problem(422, "unsupported_authorization", "关系定义目录不支持行条件授权")
			}
			if _, e = h.authorizeLink(r.Context(), p, "link.query", d.ID); e != nil {
				if hiddenSQLSource(e) {
					continue
				}
				var public *apiError
				if errors.As(e, &public) && public.status == 422 {
					continue
				}
				return nil, e
			}
			source, e := model(d.Link.SourceSchemaID)
			if e != nil {
				return nil, e
			}
			if source == nil {
				continue
			}
			target, e := model(d.Link.TargetSchemaID)
			if e != nil {
				return nil, e
			}
			if target == nil {
				continue
			}
			// Logical eligibility cannot pretend that a cross-source edge is a
			// supported plan. Actual mapping/catalog validation remains at execution.
			sc, e := h.current(r.Context(), p.TenantID, source.SchemaID)
			if e != nil {
				return nil, e
			}
			tc, e := h.current(r.Context(), p.TenantID, target.SchemaID)
			if e != nil {
				return nil, e
			}
			sb, tb, lb := sc.Definition.Schema.StorageBinding, tc.Definition.Schema.StorageBinding, d.Link.StorageBinding
			if lb.Mode == "managed" {
				if sb != nil || tb != nil {
					continue
				}
			} else if sb == nil || tb == nil || sb.DataSourceID != tb.DataSourceID || sb.DataSourceID != lb.DataSourceID {
				continue
			}
			result = append(result, sqlAuthorizedLink{Current: current, Category: categories[d.CategoryID], Source: *source, Target: *target})
		}
		if !list.HasMore {
			sort.Slice(result, func(i, j int) bool { return result[i].Current.Definition.ID < result[j].Current.Definition.ID })
			return result, nil
		}
		if list.NextCursor == "" || list.NextCursor == after {
			return nil, problem(503, "metadata_unavailable", "关系目录分页响应不合法")
		}
		after = list.NextCursor
	}
	return nil, problem(422, "directory_budget", "关系目录超过扫描预算，请缩小关系范围")
}

type queryPathEdge struct {
	link      sqlAuthorizedLink
	direction string
	from, to  c.QueryContextModel
}
type queryPathSearch struct {
	alias   string
	model   string
	edges   []queryPathEdge
	visited map[string]bool
}

func queryPathAlias(name string, used map[string]bool) string {
	runes := []rune(name)
	if len(runes) > 110 {
		name = string(runes[:110])
	}
	alias := name
	for i := 2; used[nameKey(alias)]; i++ {
		alias = fmt.Sprintf("%s_%d", name, i)
	}
	used[nameKey(alias)] = true
	return alias
}

func queryPathExtendText(text string, statement *ontologySQLStatement, steps []c.QueryPathStep) (string, error) {
	if len(steps) == 0 || len(statement.Projection) == 0 {
		return "", problem(422, "invalid_path", "关联路径或返回字段不完整")
	}
	var joins []string
	for _, step := range steps {
		joins = append(joins, "JOIN LINK "+sqlQuotedIdentifier(step.FromAlias)+" VIA "+sqlQuotedIdentifier(step.Category)+"."+sqlQuotedIdentifier(step.Name)+" DIRECTION "+strings.ToUpper(step.Direction)+" AS "+sqlQuotedIdentifier(step.ToAlias))
	}
	insert := lenUTF16(text)
	for _, token := range statement.Tokens {
		if token.Kind == "eof" {
			insert = token.Range.From
			break
		}
		if token.Kind == "word" && (token.Text == "WHERE" || token.Text == "ORDER" || token.Text == "LIMIT") || token.Kind == "punctuation" && token.Text == ";" {
			insert = token.Range.From
			break
		}
	}
	joinByte, valid := sqlCursorByte(text, insert)
	if !valid {
		return "", problem(422, "invalid_path", "无法定位查询的关联插入位置")
	}
	joined := text[:joinByte] + "\n" + strings.Join(joins, "\n") + "\n" + text[joinByte:]
	last := steps[len(steps)-1]
	fields := []string{}
	for _, field := range last.Target.Fields {
		if field.Filterable && field.DataType != "longtext" {
			fields = append(fields, sqlQuotedIdentifier(last.ToAlias)+"."+sqlQuotedIdentifier(field.Name))
			if len(fields) == 2 {
				break
			}
		}
	}
	if len(fields) == 0 {
		return "", problem(422, "unsupported_projection", "目标模型没有可用于关联行查询的主表属性")
	}
	if len(statement.Projection)+len(fields) > 100 {
		return "", problem(422, "budget_exceeded", "追加关联字段后超过100列，请先减少返回字段")
	}
	projectionByte, valid := sqlCursorByte(text, statement.Projection[len(statement.Projection)-1].Range.To)
	if !valid {
		return "", problem(422, "invalid_path", "无法定位SELECT字段位置")
	}
	return joined[:projectionByte] + ", " + strings.Join(fields, ", ") + joined[projectionByte:], nil
}

func (h *handler) queryPathsHTTP(w http.ResponseWriter, r *http.Request) {
	p, err := h.principal(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var req c.QueryPathsRequest
	if err = decode(w, r, &req); err != nil {
		writeError(w, err)
		return
	}
	if !utf8.ValidString(req.Text) || len(req.Text) > 64<<10 || len(req.Parameters) > 64 || len(req.RequestID) > 128 || len(req.DefaultCategoryID) > 128 || !uuidPattern.MatchString(req.TargetSchemaID) {
		writeError(w, problem(400, "invalid_path_request", "请提供有效的目标模型与有界查询文本"))
		return
	}
	out, err := h.queryPaths(r, p, req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, out)
}

func (h *handler) queryPaths(r *http.Request, p c.Principal, req c.QueryPathsRequest) (c.QueryPathsResult, error) {
	out := c.QueryPathsResult{RequestID: req.RequestID, TextHash: ontologySQLTextHash(req.Text), Candidates: []c.QueryPathCandidate{}, Diagnostics: []c.SQLDiagnostic{}}
	statement, diagnostics := parseOntologySQL(req.Text)
	if len(diagnostics) > 0 {
		out.Diagnostics = diagnostics
		return out, nil
	}
	analysis, err := h.analyzeOntologySQL(r, p, req.OntologySQLRequest)
	if err != nil {
		return out, err
	}
	if len(analysis.Diagnostics) > 0 {
		out.Diagnostics = analysis.Diagnostics
		return out, nil
	}
	scope, err := h.rowSQLScope(r, p, statement, req.DefaultCategoryID)
	if err != nil {
		return out, err
	}
	// Extending a single-object draft changes it to a row query. Check its
	// existing projection/conditions against that capability before suggesting
	// any joins; a secondary-source projection cannot be silently dropped.
	bound := bindRowOntologySQL(statement, scope, req.Parameters)
	if len(bound.Diagnostics) > 0 {
		out.Diagnostics = bound.Diagnostics
		return out, nil
	}
	qualifiedText, err := qualifyRowSQLReferences(req.Text, statement, scope)
	if err != nil {
		return out, err
	}
	statement, diagnostics = parseOntologySQL(qualifiedText)
	if len(diagnostics) > 0 {
		out.Diagnostics = diagnostics
		return out, nil
	}
	if len(scope.Nodes) >= 5 {
		out.Diagnostics = append(out.Diagnostics, sqlDiagnostic("budget_exceeded", "当前最多连接4条Link，请先减少关联", statement.SourceRange))
		return out, nil
	}
	context, err := h.ontologySQLContext(r, p, "", req.TargetSchemaID, "", "", 1)
	if err != nil {
		return out, err
	}
	if len(context.Models) == 0 {
		out.Diagnostics = append(out.Diagnostics, sqlDiagnostic("source_unavailable", "目标模型不存在或当前不可查询", statement.SourceRange))
		return out, nil
	}
	links, err := h.sqlAuthorizedLinks(r, p)
	if err != nil {
		var public *apiError
		if errors.As(err, &public) && public.code == "directory_budget" {
			out.Truncated = true
			out.Diagnostics = append(out.Diagnostics, sqlDiagnostic(public.code, public.message, statement.SourceRange))
			return out, nil
		}
		return out, err
	}
	graph := map[string][]queryPathEdge{}
	for _, link := range links {
		graph[link.Source.SchemaID] = append(graph[link.Source.SchemaID], queryPathEdge{link, "out", link.Source, link.Target})
		graph[link.Target.SchemaID] = append(graph[link.Target.SchemaID], queryPathEdge{link, "in", link.Target, link.Source})
	}
	queue := []queryPathSearch{}
	used := map[string]bool{}
	for _, node := range scope.Nodes {
		used[nameKey(node.Alias)] = true
		queue = append(queue, queryPathSearch{alias: node.Alias, model: node.Current.Definition.ID, visited: map[string]bool{node.Current.Definition.ID: true}})
	}
	best, expansions := 5, 0
	remaining := 5 - len(scope.Nodes)
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if len(current.edges) >= remaining || len(current.edges) >= best {
			continue
		}
		for _, edge := range graph[current.model] {
			expansions++
			if expansions > maxQueryPathExpansions {
				out.Truncated = true
				break
			}
			atTarget := edge.to.SchemaID == req.TargetSchemaID
			// A self-Link is a meaningful one-edge role addition. Longer cycles
			// are excluded; users can explicitly add another role in a later step.
			if current.visited[edge.to.SchemaID] && !(len(current.edges) == 0 && atTarget && edge.from.SchemaID == edge.to.SchemaID) {
				continue
			}
			path := append(append([]queryPathEdge(nil), current.edges...), edge)
			if atTarget {
				if len(path) < best {
					best = len(path)
					out.Candidates = []c.QueryPathCandidate{}
				}
				if len(path) != best {
					continue
				}
				if len(out.Candidates) >= maxQueryPathCandidates {
					out.Truncated = true
					break
				}
				aliases := map[string]bool{}
				for key, value := range used {
					aliases[key] = value
				}
				steps := []c.QueryPathStep{}
				fromAlias := current.alias
				// current.alias always denotes the original start, even after BFS.
				for _, part := range path {
					toAlias := queryPathAlias(part.to.Name, aliases)
					steps = append(steps, c.QueryPathStep{LinkID: part.link.Current.Definition.ID, Name: part.link.Current.Definition.Name, Category: part.link.Category, Direction: part.direction, FromAlias: fromAlias, ToAlias: toAlias, Target: part.to})
					fromAlias = toAlias
				}
				text, e := queryPathExtendText(qualifiedText, statement, steps)
				if e != nil {
					return out, e
				}
				if len(text) > 64<<10 {
					out.Candidates = []c.QueryPathCandidate{}
					out.Diagnostics = append(out.Diagnostics, sqlDiagnostic("ontology_sql_budget", "增加关系后查询文本超过64 KiB，请先缩短原稿", statement.SourceRange))
					return out, nil
				}
				if _, diagnostics := parseOntologySQL(text); len(diagnostics) > 0 {
					out.Candidates, out.Diagnostics = []c.QueryPathCandidate{}, diagnostics
					return out, nil
				}
				out.Candidates = append(out.Candidates, c.QueryPathCandidate{ID: digest(struct {
					Text  string
					Steps []c.QueryPathStep
				}{req.Text, steps}), FromAlias: current.alias, Steps: steps, Text: text})
			} else if len(path) < best {
				visited := map[string]bool{}
				for key, value := range current.visited {
					visited[key] = value
				}
				visited[edge.to.SchemaID] = true
				queue = append(queue, queryPathSearch{alias: current.alias, model: edge.to.SchemaID, edges: path, visited: visited})
			}
		}
		if out.Truncated {
			break
		}
	}
	if out.Truncated {
		out.Candidates = []c.QueryPathCandidate{}
		out.Diagnostics = append(out.Diagnostics, sqlDiagnostic("path_budget", "关联路径超过规划预算，请指定中间模型后重试", statement.SourceRange))
	} else if len(out.Candidates) == 0 {
		out.Diagnostics = append(out.Diagnostics, sqlDiagnostic("path_unavailable", "当前可用关系无法在剩余Link数量内连接目标模型；原查询已保留", statement.SourceRange))
	}
	return out, nil
}
