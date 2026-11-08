package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func (g *gateway) newServer(mask int) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "agentforge-ontology", Title: "AgentForge Ontology", Version: "0.1.0", Description: "Authorized ontology query and pure computation service"}, &mcp.ServerOptions{Instructions: "Use discovery before querying. Stable IDs and precise integer/decimal values are strings. Tools never expose source code, credentials, physical SQL, or unauthorized data.", SupportedProtocolVersions: []string{"2026-07-28", "2025-11-25", "2025-06-18", "2025-03-26"}})
	if mask&1 != 0 {
		g.addDiscoverTools(s)
	}
	if mask&2 != 0 {
		g.addQueryTools(s)
	}
	if mask&4 != 0 {
		g.addInvokeTools(s)
	}
	return s
}

func tool(name, title, description string, schema map[string]any, readonly bool) *mcp.Tool {
	destructive := !readonly
	return &mcp.Tool{Name: name, Title: title, Description: description, InputSchema: schema, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: readonly, IdempotentHint: readonly, DestructiveHint: &destructive, OpenWorldHint: ptr(false)}}
}
func ptr(v bool) *bool { return &v }
func objectSchema(properties map[string]any, required ...string) map[string]any {
	return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
}
func stringProperty(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}
func integerProperty(description string) map[string]any {
	return map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "description": description}
}

func (g *gateway) addDiscoverTools(s *mcp.Server) {
	s.AddTool(tool("get_context", "读取当前上下文", "返回当前账号、连接范围和已授权的本体能力。", objectSchema(nil), true), g.contextTool)
	s.AddTool(tool("list_categories", "列出分类", "按当前账号授权列出可用分类。", objectSchema(map[string]any{"after": stringProperty("cursor"), "limit": integerProperty("page size")}), true), g.categoriesTool)
	s.AddTool(tool("list_models", "列出模型", "列出当前账号可查询的对象类型、字段和授权上下文。", objectSchema(map[string]any{"categoryId": stringProperty("category ID"), "schemaId": stringProperty("schema ID"), "search": stringProperty("name search"), "cursor": stringProperty("query cursor"), "limit": integerProperty("page size")}), true), g.modelsTool)
	s.AddTool(tool("describe_model", "查看模型", "读取当前授权下单个模型的字段能力，不返回物理映射。", objectSchema(map[string]any{"schemaId": stringProperty("schema ID")}, "schemaId"), true), g.describeModelTool)
	s.AddTool(tool("list_callables", "列出可调用定义", "列出当前授权且可执行的纯计算操作和函数。", objectSchema(map[string]any{"kind": stringProperty("action or function"), "categoryId": stringProperty("category ID"), "after": stringProperty("cursor"), "limit": integerProperty("page size")}), true), g.callablesTool)
	s.AddTool(tool("describe_callable", "查看可调用定义", "读取当前授权下的调用签名，不返回源码或制品。", objectSchema(map[string]any{"definitionId": stringProperty("definition ID")}, "definitionId"), true), g.describeCallableTool)
}

func (g *gateway) addQueryTools(s *mcp.Server) {
	s.AddTool(tool("get_object", "读取对象", "按当前行列权限读取一个对象。", objectSchema(map[string]any{"schemaId": stringProperty("schema ID"), "entityId": stringProperty("object ID")}, "schemaId", "entityId"), true), g.objectTool)
	s.AddTool(tool("query_objects", "查询对象", "执行有界、分页且受当前行列权限保护的查询。", objectSchema(map[string]any{"query": map[string]any{"type": "object", "description": "Data QuerySpec"}, "idempotencyKey": stringProperty("optional idempotency key")}, "query"), true), g.queryTool)
	s.AddTool(tool("analyze_query", "分析中文查询", "只解析和授权检查 OntologySQL，不读取业务行。", objectSchema(map[string]any{"text": map[string]any{"type": "string", "maxLength": 65536}, "parameters": map[string]any{"type": "object"}, "defaultCategoryId": stringProperty("default category ID")}, "text"), true), g.analyzeTool)
	s.AddTool(tool("query_ontology_sql", "执行中文查询", "执行支持的 OntologySQL，并复用统一查询授权和执行器。", objectSchema(map[string]any{"text": map[string]any{"type": "string", "maxLength": 65536}, "parameters": map[string]any{"type": "object"}, "defaultCategoryId": stringProperty("default category ID"), "requestId": stringProperty("request ID"), "cursorOffset": map[string]any{"type": "integer", "minimum": 0}}, "text"), true), g.ontologySQLTool)
}

func (g *gateway) addInvokeTools(s *mcp.Server) {
	s.AddTool(tool("invoke_action", "调用操作", "调用当前授权的纯计算操作；不接受草稿、源码或副作用。", objectSchema(map[string]any{"definitionId": stringProperty("Action ID"), "input": map[string]any{"type": "object"}, "draftEtag": stringProperty("draft ETag; unsupported"), "idempotencyKey": stringProperty("idempotency key")}, "definitionId", "input", "idempotencyKey"), false), g.invokeActionTool)
	s.AddTool(tool("invoke_function", "调用函数", "调用当前授权的纯计算函数；不接受源码或外部依赖。", objectSchema(map[string]any{"definitionId": stringProperty("Function ID"), "input": map[string]any{"type": "object"}, "draftEtag": stringProperty("draft ETag; unsupported"), "idempotencyKey": stringProperty("idempotency key")}, "definitionId", "input", "idempotencyKey"), false), g.invokeFunctionTool)
	s.AddTool(tool("get_invocation", "读取调用结果", "重新核验当前授权后读取本主体发起的执行结果。", objectSchema(map[string]any{"invocationId": stringProperty("invocation ID")}, "invocationId"), true), g.invocationTool)
}

func args(req *mcp.CallToolRequest, out any) error {
	if len(req.Params.Arguments) > MaxRequestBytes {
		return invalid("arguments exceed budget")
	}
	dec := json.NewDecoder(strings.NewReader(string(req.Params.Arguments)))
	dec.DisallowUnknownFields()
	dec.UseNumber()
	if err := dec.Decode(out); err != nil {
		return invalid("arguments must be one JSON object")
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return invalid("arguments must contain one JSON object")
	}
	return nil
}
func reqContext(ctx context.Context) *requestIdentity {
	v := ctx.Value(identityKey{})
	if i, ok := v.(requestIdentity); ok {
		return &i
	}
	return nil
}

func requireText(value, field string) error {
	if strings.TrimSpace(value) == "" || len(value) > 256 {
		return invalid(field + " is required")
	}
	return nil
}

func (g *gateway) contextTool(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	identity := reqContext(ctx)
	if identity == nil {
		return toolResult(nil, invalid("identity unavailable"))
	}
	var metadata map[string]any
	if err := g.call(ctx, g.cfg.MetadataURL, http.MethodGet, "/metadata/capabilities", nil, &metadata, ""); err != nil {
		return toolResult(nil, err)
	}
	return toolResult(map[string]any{"tenantId": identity.session.User.TenantID, "subject": identity.session.User.ID, "scopes": identity.session.AccessScopes, "capabilities": metadata, "resource": g.cfg.ResourceURL}, nil)
}
func (g *gateway) categoriesTool(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var in struct {
		After string `json:"after"`
		Limit int    `json:"limit"`
	}
	if err := args(req, &in); err != nil {
		return toolResult(nil, err)
	}
	q := url.Values{}
	if in.After != "" {
		q.Set("after", in.After)
	}
	if in.Limit > 0 {
		q.Set("limit", strconv.Itoa(in.Limit))
	}
	var out any
	err := g.call(ctx, g.cfg.MetadataURL, http.MethodGet, "/metadata/categories?"+q.Encode(), nil, &out, "")
	return toolResult(out, err)
}
func (g *gateway) modelsTool(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var in struct {
		CategoryID string `json:"categoryId"`
		SchemaID   string `json:"schemaId"`
		Search     string `json:"search"`
		Cursor     string `json:"cursor"`
		Limit      int    `json:"limit"`
	}
	if err := args(req, &in); err != nil {
		return toolResult(nil, err)
	}
	q := url.Values{}
	q.Set("categoryId", in.CategoryID)
	q.Set("schemaId", in.SchemaID)
	q.Set("search", in.Search)
	q.Set("cursor", in.Cursor)
	if in.Limit > 0 {
		q.Set("limit", strconv.Itoa(in.Limit))
	}
	var out c.QueryContext
	err := g.call(ctx, g.cfg.DataURL, http.MethodGet, "/data/query/context?"+q.Encode(), nil, &out, "")
	return toolResult(out, err)
}
func (g *gateway) describeModelTool(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var in struct {
		SchemaID string `json:"schemaId"`
	}
	if err := args(req, &in); err != nil {
		return toolResult(nil, err)
	}
	if err := requireText(in.SchemaID, "schemaId"); err != nil {
		return toolResult(nil, err)
	}
	var out c.QueryContext
	err := g.call(ctx, g.cfg.DataURL, http.MethodGet, "/data/query/context?schemaId="+url.QueryEscape(in.SchemaID)+"&limit=1", nil, &out, "")
	if err == nil && len(out.Models) == 0 {
		err = &toolProblem{Code: "not_found", Message: "model not found or inaccessible", Status: 404}
	}
	return toolResult(out, err)
}
func (g *gateway) callablesTool(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var in struct {
		Kind       string `json:"kind"`
		CategoryID string `json:"categoryId"`
		After      string `json:"after"`
		Limit      int    `json:"limit"`
	}
	if err := args(req, &in); err != nil {
		return toolResult(nil, err)
	}
	q := url.Values{}
	for k, v := range map[string]string{"kind": in.Kind, "categoryId": in.CategoryID, "after": in.After} {
		if v != "" {
			q.Set(k, v)
		}
	}
	if in.Limit > 0 {
		q.Set("limit", strconv.Itoa(in.Limit))
	}
	var out c.CallableDirectory
	err := g.call(ctx, g.cfg.MetadataURL, http.MethodGet, "/metadata/callables?"+q.Encode(), nil, &out, "")
	return toolResult(out, err)
}
func (g *gateway) describeCallableTool(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var in struct {
		DefinitionID string `json:"definitionId"`
	}
	if err := args(req, &in); err != nil {
		return toolResult(nil, err)
	}
	if err := requireText(in.DefinitionID, "definitionId"); err != nil {
		return toolResult(nil, err)
	}
	var out c.CallableSummary
	err := g.call(ctx, g.cfg.MetadataURL, http.MethodGet, "/metadata/callables/"+url.PathEscape(in.DefinitionID), nil, &out, "")
	return toolResult(out, err)
}
func (g *gateway) objectTool(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var in struct {
		SchemaID string `json:"schemaId"`
		EntityID string `json:"entityId"`
	}
	if err := args(req, &in); err != nil {
		return toolResult(nil, err)
	}
	if err := requireText(in.SchemaID, "schemaId"); err != nil {
		return toolResult(nil, err)
	}
	if err := requireText(in.EntityID, "entityId"); err != nil {
		return toolResult(nil, err)
	}
	var out c.Instance
	err := g.call(ctx, g.cfg.DataURL, http.MethodGet, "/data/schemas/"+url.PathEscape(in.SchemaID)+"/instances/"+url.PathEscape(in.EntityID), nil, &out, "")
	return toolResult(out, err)
}
func (g *gateway) queryTool(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var in struct {
		Query          c.QuerySpec `json:"query"`
		IdempotencyKey string      `json:"idempotencyKey"`
	}
	if err := args(req, &in); err != nil {
		return toolResult(nil, err)
	}
	if in.Query.Page.Limit <= 0 || in.Query.Page.Limit > 100 {
		return toolResult(nil, invalid("query.page.limit must be between 1 and 100"))
	}
	var out c.QueryResult
	err := g.call(ctx, g.cfg.DataURL, http.MethodPost, "/data/query", in.Query, &out, in.IdempotencyKey)
	return toolResult(out, err)
}
func (g *gateway) analyzeTool(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var in c.OntologySQLRequest
	if err := args(req, &in); err != nil {
		return toolResult(nil, err)
	}
	if strings.TrimSpace(in.Text) == "" {
		return toolResult(nil, invalid("text is required"))
	}
	var out c.OntologySQLAnalysis
	err := g.call(ctx, g.cfg.DataURL, http.MethodPost, "/data/query/ontology-sql/analyze", in, &out, "")
	return toolResult(out, err)
}
func (g *gateway) ontologySQLTool(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var in c.OntologySQLRequest
	if err := args(req, &in); err != nil {
		return toolResult(nil, err)
	}
	if strings.TrimSpace(in.Text) == "" {
		return toolResult(nil, invalid("text is required"))
	}
	var out c.OntologySQLResult
	err := g.call(ctx, g.cfg.DataURL, http.MethodPost, "/data/query/ontology-sql", in, &out, "")
	return toolResult(out, err)
}
func (g *gateway) invokeActionTool(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return g.invokeTool(ctx, req, c.KindAction)
}
func (g *gateway) invokeFunctionTool(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return g.invokeTool(ctx, req, c.KindFunction)
}
func (g *gateway) invokeTool(ctx context.Context, req *mcp.CallToolRequest, kind c.Kind) (*mcp.CallToolResult, error) {
	var in struct {
		DefinitionID   string                     `json:"definitionId"`
		Input          map[string]json.RawMessage `json:"input"`
		DraftETag      string                     `json:"draftEtag"`
		IdempotencyKey string                     `json:"idempotencyKey"`
	}
	if err := args(req, &in); err != nil {
		return toolResult(nil, err)
	}
	if err := requireText(in.DefinitionID, "definitionId"); err != nil {
		return toolResult(nil, err)
	}
	if in.Input == nil {
		return toolResult(nil, invalid("input is required"))
	}
	if in.DraftETag != "" {
		return toolResult(nil, invalid("draft invocation is not available through MCP"))
	}
	if err := requireText(in.IdempotencyKey, "idempotencyKey"); err != nil {
		return toolResult(nil, err)
	}
	var signature c.CallableSummary
	if err := g.call(ctx, g.cfg.MetadataURL, http.MethodGet, "/metadata/callables/"+url.PathEscape(in.DefinitionID), nil, &signature, ""); err != nil {
		return toolResult(nil, err)
	}
	if signature.ID != in.DefinitionID || signature.Kind != kind || signature.SideEffects != "none" {
		return toolResult(nil, invalid("definition must be a current pure computation of the requested kind"))
	}
	var out c.Invocation
	reqBody := c.InvocationRequest{DefinitionID: in.DefinitionID, Input: in.Input}
	err := g.call(ctx, g.cfg.ExecutionURL, http.MethodPost, "/execution/invocations", reqBody, &out, in.IdempotencyKey)
	return toolResult(out, err)
}
func (g *gateway) invocationTool(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var in struct {
		InvocationID string `json:"invocationId"`
	}
	if err := args(req, &in); err != nil {
		return toolResult(nil, err)
	}
	if err := requireText(in.InvocationID, "invocationId"); err != nil {
		return toolResult(nil, err)
	}
	var out c.Invocation
	err := g.call(ctx, g.cfg.ExecutionURL, http.MethodGet, "/execution/invocations/"+url.PathEscape(in.InvocationID), nil, &out, "")
	return toolResult(out, err)
}
