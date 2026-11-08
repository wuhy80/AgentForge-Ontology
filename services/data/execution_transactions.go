package data

import (
	"context"
	"crypto/subtle"
	"net/http"
	"net/url"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
)

// Execution supplies the original browser check over this service-authenticated
// channel. A service credential never supplies the business identity or grants.
func (h *handler) executionTransactionHTTP(w http.ResponseWriter, r *http.Request) {
	if !platform.EqualSecret(platform.Bearer(r), h.cfg.ServiceToken) || len(h.cfg.ServiceToken) < 32 {
		writeError(w, problem(401, "unauthorized", "内部认证失败"))
		return
	}
	var body c.ExecutionTransactionRequest
	if err := decode(w, r, &body); err != nil {
		writeError(w, err)
		return
	}
	if body.Session.AccessToken != "" || body.Session.Token == "" || body.Session.Path != "" || body.Session.Resource != "" || (body.Session.Method != http.MethodPost && !(body.Lookup && body.Session.Method == http.MethodGet)) || body.Transaction.IdempotencyKey != "execution:"+body.InvocationID || body.InvocationID == "" || len(body.InvocationID) > 64 {
		writeError(w, problem(403, "invalid_execution_context", "事务需要原调用者的浏览器会话及有效执行上下文"))
		return
	}
	if body.Session.Method == http.MethodPost {
		origin, err := url.Parse(body.Session.Origin)
		if err != nil || origin.Host == "" || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" || (origin.Scheme != "http" && origin.Scheme != "https") {
			writeError(w, problem(403, "invalid_origin", "原调用来源无效"))
			return
		}
	}
	p, err := h.executionTransactionPrincipal(r.Context(), body.Session)
	if err != nil {
		writeError(w, err)
		return
	}
	guard := func(ctx context.Context) error {
		fresh, err := h.executionTransactionPrincipal(ctx, body.Session)
		if err != nil {
			return err
		}
		if fresh.TenantID != p.TenantID || fresh.Subject != p.Subject {
			return problem(401, "unauthenticated", "调用者会话已改变")
		}
		return h.checkExecutionTransaction(ctx, p, body)
	}
	if err = guard(r.Context()); err != nil {
		writeError(w, err)
		return
	}
	result, err := h.executeGuardedWriteTransaction(r.Context(), p, body.Transaction, body.Lookup, guard)
	if err != nil {
		writeError(w, err)
		return
	}
	// Execution only needs durable commit evidence, never historical row values.
	result.Results = []c.WriteResult{}
	writeJSON(w, 200, result)
}

func (h *handler) executionTransactionPrincipal(ctx context.Context, check c.SessionCheck) (c.Principal, error) {
	if h.cfg.SessionChecker == nil {
		return c.Principal{}, problem(503, "identity_unavailable", "身份服务未配置")
	}
	session, err := h.cfg.SessionChecker.CheckSession(ctx, check)
	if err != nil {
		return c.Principal{}, problem(403, "unauthenticated", "原调用者会话无法确认")
	}
	if !session.User.Enabled || session.User.ID == "" || len(session.User.ID) > 255 || session.User.TenantID == "" || len(session.User.TenantID) > 128 || session.ExpiresAt <= time.Now().Unix() || session.AccessTokenID != "" {
		return c.Principal{}, problem(401, "unauthenticated", "原调用者会话已失效")
	}
	if platform.UnsafeMethod(check.Method) && (len(check.CSRFToken) < 32 || len(session.CSRFToken) < 32 || subtle.ConstantTimeCompare([]byte(check.CSRFToken), []byte(session.CSRFToken)) != 1) {
		return c.Principal{}, problem(403, "invalid_csrf", "原调用者请求校验已失效")
	}
	return c.Principal{TenantID: session.User.TenantID, Subject: session.User.ID, ExpiresAt: session.ExpiresAt}, nil
}

func transactionCallable(d c.Definition) *c.Callable {
	if d.Kind == c.KindAction {
		return d.Action
	}
	if d.Kind == c.KindFunction {
		return d.Function
	}
	return nil
}

func (h *handler) checkExecutionTransaction(ctx context.Context, p c.Principal, body c.ExecutionTransactionRequest) error {
	root, err := h.current(ctx, p.TenantID, body.DefinitionID)
	if err != nil {
		return err
	}
	implementation := root
	if body.ImplementationID != body.DefinitionID {
		implementation, err = h.current(ctx, p.TenantID, body.ImplementationID)
		if err != nil {
			return err
		}
	}
	if root.Definition.ID != body.DefinitionID || implementation.Definition.ID != body.ImplementationID || digest(root.Definition) != body.DefinitionDigest || digest(implementation.Definition) != body.ImplementationDigest {
		return problem(409, "definition_changed", "写入前当前调用定义已改变")
	}
	rootCall, implCall := transactionCallable(root.Definition), transactionCallable(implementation.Definition)
	if rootCall == nil || implCall == nil || rootCall.SideEffects != "dataWrite" || implCall.SideEffects != "dataWrite" || implCall.Runtime == nil || implCall.ArtifactRef != "" || implCall.FunctionID != "" || c.ValidateScriptRuntime(implCall.Runtime) != nil {
		return problem(422, "invalid_execution_context", "定义未声明可执行的数据写入实现")
	}
	if root.Definition.ID != implementation.Definition.ID && (root.Definition.Kind != c.KindAction || rootCall.FunctionID != implementation.Definition.ID || rootCall.Runtime != nil || rootCall.ArtifactRef != "" || implementation.Definition.Kind != c.KindFunction) {
		return problem(422, "invalid_execution_context", "Action实现绑定不一致")
	}
	for _, d := range []c.Definition{root.Definition, implementation.Definition} {
		op := "function.invoke"
		if d.Kind == c.KindAction {
			op = "action.invoke"
		}
		decision, err := h.authorizeKind(ctx, p, op, d.ID, d.Kind)
		if err != nil {
			return err
		}
		whole := func(fields []string) bool { return len(fields) == 0 || len(fields) == 1 && fields[0] == "*" }
		if decision.RowFilter != nil || decision.NewRowFilter != nil || !whole(decision.ReadFields) || !whole(decision.WriteFields) {
			return problem(422, "unsupported_authorization", "调用权限不能包含行或参数投影")
		}
	}
	for _, operation := range body.Transaction.Operations {
		for _, call := range []*c.Callable{rootCall, implCall} {
			allowed := false
			for _, grant := range call.DataWrite {
				if grant.SchemaID == operation.SchemaID {
					for _, name := range grant.Operations {
						if name == operation.Operation {
							allowed = true
						}
					}
				}
			}
			if !allowed {
				return problem(403, "data_scope_denied", "事务超出调用定义声明的数据写入范围")
			}
		}
	}
	return nil
}
