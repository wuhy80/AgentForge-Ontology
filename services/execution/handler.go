// Package execution owns callable invocation and durable execution evidence.
// It accesses identity and metadata only through their public/internal contracts.
package execution

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
)

type Config struct {
	Store          Store
	Authorizer     platform.Authorizer
	SessionChecker platform.SessionChecker
	Metadata       MetadataReader
	Runner         Runner
	MaxConcurrent  int
}

type handler struct {
	cfg   Config
	slots chan struct{}
}
type apiError struct {
	status        int
	code, message string
}

func (e *apiError) Error() string                    { return e.code + ": " + e.message }
func problem(status int, code, message string) error { return &apiError{status, code, message} }
func digest(v any) string {
	b, _ := json.Marshal(v)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
func sourceDigest(source string) string {
	s := sha256.Sum256([]byte(source))
	return hex.EncodeToString(s[:])
}
func validID(id string) bool {
	return id != "" && id != "*" && len(id) <= 128 && utf8.ValidString(id) && strings.TrimSpace(id) == id && strings.IndexFunc(id, unicode.IsControl) < 0 && !strings.ContainsAny(id, "/\\?#")
}

var requestKeyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
var draftETagPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func NewHandler(cfg Config) http.Handler {
	limit := cfg.MaxConcurrent
	if limit <= 0 || limit > 4 {
		limit = 4
	}
	h := &handler{cfg: cfg, slots: make(chan struct{}, limit)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /readyz", h.ready)
	mux.HandleFunc("GET /execution/capabilities", h.wrap(h.capabilities))
	mux.HandleFunc("POST /execution/source-tools", h.wrap(h.sourceTools))
	mux.HandleFunc("POST /execution/invocations", h.wrap(h.invoke))
	mux.HandleFunc("GET /execution/invocations/{id}", h.wrap(h.get))
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func writeProblem(w http.ResponseWriter, err error) {
	var ae *apiError
	if errors.As(err, &ae) {
		writeJSON(w, ae.status, c.Error{Code: ae.code, Message: ae.message})
		return
	}
	var access *platform.AccessError
	if errors.As(err, &access) && (access.Status == 401 || access.Status == 403 || access.Status == 422 || access.Status == 503) {
		writeJSON(w, access.Status, c.Error{Code: access.Code, Message: access.Message})
		return
	}
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, 404, c.Error{Code: "not_found", Message: "未找到当前账号的执行记录"})
		return
	}
	writeJSON(w, 500, c.Error{Code: "execution_error", Message: "执行记录处理失败，请使用原请求标识核对状态"})
}

func (h *handler) wrap(next func(http.ResponseWriter, *http.Request, c.Principal) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := platform.Principal(r)
		if !ok || !validID(p.TenantID) || !validID(p.Subject) || p.ExpiresAt <= time.Now().Unix() {
			writeProblem(w, problem(401, "unauthenticated", "请登录后继续"))
			return
		}
		if err := h.session(r.Context(), r, p); err != nil {
			writeProblem(w, err)
			return
		}
		if err := next(w, r, p); err != nil {
			writeProblem(w, err)
		}
	}
}

func (h *handler) session(ctx context.Context, r *http.Request, p c.Principal) error {
	if h.cfg.SessionChecker == nil {
		return problem(503, "identity_unavailable", "身份服务未配置")
	}
	check, err := platform.SessionRequest(r)
	if err != nil {
		return err
	}
	session, err := h.cfg.SessionChecker.CheckSession(ctx, check)
	if err != nil {
		return err
	}
	if !session.User.Enabled || session.User.ID != p.Subject || session.User.TenantID != p.TenantID || session.ExpiresAt <= time.Now().Unix() {
		return problem(401, "unauthenticated", "登录已失效，请重新登录")
	}
	return nil
}

func (h *handler) authorize(ctx context.Context, p c.Principal, kind c.Kind, id string) error {
	operation := "function.invoke"
	if kind == c.KindAction {
		operation = "action.invoke"
	} else if kind != c.KindFunction {
		return problem(404, "not_found", "未找到可调用的当前定义")
	}
	return h.authorizeOperation(ctx, p, operation, kind, id)
}

func (h *handler) authorizeOperation(ctx context.Context, p c.Principal, operation string, kind c.Kind, id string) error {
	if h.cfg.Authorizer == nil {
		return problem(503, "authorization_unavailable", "授权服务未配置")
	}
	decision, err := h.cfg.Authorizer.Authorize(ctx, p, c.AuthorizationRequest{Operation: operation, ResourceID: id, Kind: kind})
	if err != nil {
		return err
	}
	if !decision.Allow {
		return problem(403, "forbidden", "无权调用此定义或读取其执行结果")
	}
	if decision.State == "" {
		return problem(503, "authorization_unavailable", "无法确认当前授权状态")
	}
	if decision.RowFilter != nil || decision.NewRowFilter != nil || !wholeFields(decision.ReadFields) || !wholeFields(decision.WriteFields) {
		return problem(422, "unsupported_authorization", "调用权限暂不支持行条件或参数字段投影")
	}
	return nil
}
func wholeFields(fields []string) bool {
	return len(fields) == 0 || len(fields) == 1 && fields[0] == "*"
}

type plan struct{ root, implementation c.Definition }

func definitionCallable(d c.Definition) *c.Callable {
	if d.Kind == c.KindFunction {
		return d.Function
	}
	if d.Kind == c.KindAction {
		return d.Action
	}
	return nil
}

func (h *handler) plan(ctx context.Context, p c.Principal, id string) (plan, error) {
	var out plan
	if h.cfg.Metadata == nil {
		return out, problem(503, "metadata_unavailable", "定义服务未配置")
	}
	current, err := h.cfg.Metadata.Current(ctx, p.TenantID, id)
	if err != nil {
		return out, err
	}
	out.root = current.Definition
	if current.Status != "active" || out.root.ID != id || out.root.TenantID != p.TenantID {
		return out, problem(404, "not_found", "未找到可调用的当前定义")
	}
	return h.resolvePlan(ctx, p, out)
}

func (h *handler) draftPlan(ctx context.Context, p c.Principal, id, etag string) (plan, error) {
	var out plan
	reader, ok := h.cfg.Metadata.(DraftMetadataReader)
	if !ok {
		return out, problem(503, "draft_unavailable", "定义服务尚不支持草稿读取")
	}
	draft, err := reader.Draft(ctx, p.TenantID, id)
	if err != nil {
		return out, err
	}
	out.root = draft.Definition
	if draft.Status != "draft" || out.root.ID != id || out.root.TenantID != p.TenantID {
		return out, problem(404, "not_found", "未找到可试运行的草稿")
	}
	if err := h.authorizeOperation(ctx, p, "definition.edit", out.root.Kind, id); err != nil {
		return out, err
	}
	if err := h.authorize(ctx, p, out.root.Kind, id); err != nil {
		return out, err
	}
	if draft.ETag != etag {
		return out, problem(409, "draft_changed", "草稿已改变，请重新保存或载入后使用新的请求标识试运行")
	}
	return h.resolvePlan(ctx, p, out)
}

func (h *handler) resolvePlan(ctx context.Context, p c.Principal, out plan) (plan, error) {
	if err := h.authorize(ctx, p, out.root.Kind, out.root.ID); err != nil {
		return out, err
	}
	if out.root.Kind == c.KindAction {
		if out.root.Action == nil || out.root.Action.SideEffects != "none" || out.root.Action.ArtifactRef != "" {
			return out, problem(422, "unsupported_action", "仅执行内联源码或绑定无副作用Function的Action")
		}
		if out.root.Action.Runtime != nil {
			out.implementation = out.root
			return out, executable(out.implementation)
		}
		if out.root.Action.FunctionID == "" {
			return out, problem(422, "runtime_missing", "此Action尚未配置源码或绑定Function")
		}
		fn, err := h.cfg.Metadata.Current(ctx, p.TenantID, out.root.Action.FunctionID)
		if err != nil {
			return out, err
		}
		out.implementation = fn.Definition
		if fn.Status != "active" || out.implementation.Kind != c.KindFunction || out.implementation.Function == nil || out.implementation.TenantID != p.TenantID || out.implementation.ID != out.root.Action.FunctionID {
			return out, problem(422, "invalid_function", "动作实现必须是同租户已生效Function")
		}
		if err := h.authorize(ctx, p, c.KindFunction, out.implementation.ID); err != nil {
			return out, err
		}
		if !sameSignature(out.root.Action.Input, out.implementation.Function.Input) || !sameSignature(out.root.Action.Output, out.implementation.Function.Output) {
			return out, problem(422, "signature_mismatch", "动作与实现函数的输入输出声明不一致")
		}
	} else {
		out.implementation = out.root
	}
	if err := executable(out.implementation); err != nil {
		return out, err
	}
	return out, nil
}

func executable(definition c.Definition) error {
	callable := definitionCallable(definition)
	if callable == nil || callable.Runtime == nil {
		return problem(422, "runtime_missing", "此定义尚未配置源码实现")
	}
	runtime := callable.Runtime
	if callable.SideEffects != "none" || callable.FunctionID != "" || callable.ArtifactRef != "" || definition.Kind == c.KindFunction && len(callable.TargetSchemaIDs) > 0 {
		return problem(422, "unsupported_runtime", "仅支持无副作用的内联源码计算")
	}
	if c.ValidateScriptRuntime(runtime) != nil {
		return problem(422, "unsupported_runtime", "源码语言、内容、入口或运行时限无效")
	}
	if err := validateParameters(callable.Input); err != nil {
		return err
	}
	return validateParameters(callable.Output)
}

func (h *handler) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	if h.cfg.Store == nil || h.cfg.Store.Ping(ctx) != nil || h.cfg.Runner == nil {
		writeProblem(w, problem(503, "not_ready", "执行存储或运行器尚未就绪"))
		return
	}
	if len(availableRuntimes(ctx, h.cfg.Runner)) == 0 {
		writeProblem(w, problem(503, "runtime_unavailable", "运行器尚未就绪"))
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ready"})
}

func (h *handler) capabilities(w http.ResponseWriter, r *http.Request, p c.Principal) error {
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	runtimes := []string{}
	if h.cfg.Runner != nil && h.cfg.Store != nil && h.cfg.Store.Ping(ctx) == nil {
		runtimes = availableRuntimes(ctx, h.cfg.Runner)
	}
	available := len(runtimes) > 0
	sourceTools := []string{}
	if _, ok := h.cfg.Runner.(sourceToolRunner); ok {
		for _, language := range runtimes {
			if language == "go" {
				sourceTools = append(sourceTools, language)
			}
		}
	}
	out := map[string]any{"invocation": available, "draftInvocation": available, "sourceDiagnostics": true, "testCases": true, "runtimes": runtimes, "supportedRuntimes": supportedRuntimes, "sourceTools": sourceTools, "synchronous": true, "async": false, "sideEffects": []string{"none"}, "maxTimeoutSeconds": 30, "maxConcurrent": cap(h.slots), "maxPayloadBytes": MaxPayloadBytes, "actionInlineSource": true, "actionRequiresFunctionInvoke": true}
	if !available {
		out["reason"] = "运行器或执行存储尚未就绪"
	}
	writeJSON(w, 200, out)
	return nil
}

func decodeRequest(w http.ResponseWriter, r *http.Request, out any) error {
	r.Body = http.MaxBytesReader(w, r.Body, MaxPayloadBytes)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		return problem(413, "request_budget", "调用请求超过256KiB预算")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(out) != nil || decoder.Decode(new(any)) != io.EOF {
		return problem(400, "invalid_json", "请求须为已声明结构的单个JSON对象")
	}
	return nil
}

func (h *handler) invoke(w http.ResponseWriter, r *http.Request, p c.Principal) error {
	if h.cfg.Store == nil || h.cfg.Runner == nil {
		return problem(503, "execution_unavailable", "执行服务未完整配置")
	}
	key := r.Header.Get("Idempotency-Key")
	if !requestKeyPattern.MatchString(key) {
		return problem(400, "idempotency_required", "须提供1至128字符的Idempotency-Key请求标识")
	}
	var request c.InvocationRequest
	if err := decodeRequest(w, r, &request); err != nil {
		return err
	}
	if !validID(request.DefinitionID) {
		return problem(400, "invalid_definition", "定义标识无效")
	}
	if request.DraftETag != "" && !draftETagPattern.MatchString(request.DraftETag) {
		return problem(400, "invalid_draft_etag", "草稿并发凭证须为已保存草稿的ETag")
	}
	if request.DraftETag != "" && platform.ValidAccessToken(platform.Bearer(r)) {
		return problem(403, "connection_scope_denied", "外部连接仅允许调用当前生效定义，不允许草稿试运行")
	}
	hash := digest(request)
	previous, err := h.cfg.Store.FindKey(r.Context(), p.TenantID, p.Subject, key)
	if err == nil {
		return h.replay(w, r, p, previous, hash)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		return problem(429, "execution_busy", "计算并发已满，请稍后使用原请求标识重试")
	}
	mode := c.InvocationModeCurrent
	var initial plan
	if request.DraftETag != "" {
		mode = c.InvocationModeDraft
		initial, err = h.draftPlan(r.Context(), p, request.DefinitionID, request.DraftETag)
	} else {
		initial, err = h.plan(r.Context(), p, request.DefinitionID)
	}
	if err != nil {
		return err
	}
	rootCallable := definitionCallable(initial.root)
	runtime := definitionCallable(initial.implementation).Runtime
	namedInput, err := buildInput(rootCallable.Input, request.Input)
	if err != nil {
		return err
	}
	readyCtx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	imageID, err := readyRuntime(readyCtx, h.cfg.Runner, runtime.Language)
	cancel()
	if err != nil {
		return problem(503, "runtime_unavailable", runtimeLabel(runtime.Language)+"运行器尚未就绪")
	}
	now := time.Now().UTC()
	record := Record{TenantID: p.TenantID, SubjectID: p.Subject, RequestKey: key, RequestHash: hash, Root: initial.root, Implementation: initial.implementation, Input: namedInput, ImageID: imageID, DeadlineAt: now.Add(time.Duration(runtime.TimeoutSeconds)*time.Second + 90*time.Second), Invocation: c.Invocation{ID: c.NewID(), DefinitionID: initial.root.ID, DefinitionName: initial.root.Name, Kind: initial.root.Kind, Status: "running", CreatedAt: now.Format(time.RFC3339Nano), CodeDigest: sourceDigest(runtime.Source), Mode: mode, DraftETag: request.DraftETag}}
	// Recheck after setup and immediately before admission; no saved allow or
	// user-supplied viaAction context can substitute for these current checks.
	if err := h.checkRecord(r.Context(), r, p, record); err != nil {
		return err
	}
	claimed, err := h.cfg.Store.Claim(r.Context(), record)
	if err != nil {
		return err
	}
	if !claimed {
		other, err := h.cfg.Store.FindKey(r.Context(), p.TenantID, p.Subject, key)
		if err != nil {
			return err
		}
		return h.replay(w, r, p, other, hash)
	}
	w.Header().Set("Location", "/execution/invocations/"+record.Invocation.ID)
	// A definition or session can change after claiming. Such a record becomes
	// a failed invocation; it is never silently abandoned or automatically run.
	if err := h.checkRecord(r.Context(), r, p, record); err != nil {
		record.Invocation.Status = "failed"
		record.Invocation.Error = &c.Error{Code: "authorization_changed", Message: "启动前当前身份、授权或定义已改变"}
		return h.finish(w, r, p, record, 201)
	}
	runCtx, stop := context.WithTimeout(r.Context(), time.Duration(runtime.TimeoutSeconds)*time.Second)
	output, runErr := h.cfg.Runner.Run(runCtx, runtime, namedInput, imageID)
	stop()
	if runErr != nil {
		var re *RunError
		if errors.As(runErr, &re) {
			record.Invocation.Status = re.Status
			record.Invocation.Error = &c.Error{Code: re.Code, Message: re.Message}
			var diagnosticError *DiagnosticRunError
			if errors.As(runErr, &diagnosticError) {
				record.Invocation.Diagnostics = diagnosticError.Diagnostics
			}
		} else {
			record.Invocation.Status = "failed"
			record.Invocation.Error = &c.Error{Code: "runtime_failed", Message: runtimeLabel(runtime.Language) + "执行失败"}
		}
	} else {
		values, e := buildOutput(rootCallable.Output, output)
		if e != nil {
			record.Invocation.Status = "failed"
			record.Invocation.Error = &c.Error{Code: "invalid_output", Message: "函数输出未通过声明校验"}
		} else {
			record.Invocation.Status = "succeeded"
			record.Invocation.Output = values
		}
	}
	return h.finish(w, r, p, record, 201)
}

func (h *handler) finish(w http.ResponseWriter, r *http.Request, p c.Principal, record Record, status int) error {
	record.Invocation.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	// A disconnected browser must not prevent recording a terminal outcome.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	saved, err := h.cfg.Store.Finish(ctx, record)
	cancel()
	if err != nil {
		return err
	}
	if !saved {
		record, err = h.cfg.Store.FindID(r.Context(), p.TenantID, p.Subject, record.Invocation.ID)
		if err != nil {
			return err
		}
	}
	if err := h.checkRecord(r.Context(), r, p, record); err != nil {
		return err
	}
	writeJSON(w, status, record.Invocation)
	return nil
}

func (h *handler) checkRecord(ctx context.Context, r *http.Request, p c.Principal, record Record) error {
	if record.TenantID != p.TenantID || record.SubjectID != p.Subject {
		return sql.ErrNoRows
	}
	if err := h.session(ctx, r, p); err != nil {
		return err
	}
	var current plan
	var err error
	if record.Invocation.Mode == c.InvocationModeDraft {
		if platform.ValidAccessToken(platform.Bearer(r)) {
			return problem(403, "connection_scope_denied", "外部连接不允许读取草稿执行结果")
		}
		if !draftETagPattern.MatchString(record.Invocation.DraftETag) {
			return problem(409, "invalid_record", "草稿执行记录的并发凭证无效")
		}
		current, err = h.draftPlan(ctx, p, record.Root.ID, record.Invocation.DraftETag)
	} else if record.Invocation.Mode == "" || record.Invocation.Mode == c.InvocationModeCurrent {
		current, err = h.plan(ctx, p, record.Root.ID)
	} else {
		return problem(409, "invalid_record", "执行记录的实现模式无效")
	}
	if err != nil {
		return err
	}
	if digest(current.root) != digest(record.Root) || digest(current.implementation) != digest(record.Implementation) {
		return problem(409, "definition_changed", "当前定义已改变，无法按原执行上下文安全交付结果；请使用新请求标识重新运行")
	}
	return nil
}

func (h *handler) replay(w http.ResponseWriter, r *http.Request, p c.Principal, record Record, hash string) error {
	if record.RequestHash != hash {
		return problem(409, "idempotency_conflict", "此请求标识已用于不同的定义或参数")
	}
	return h.deliver(w, r, p, record)
}

func (h *handler) get(w http.ResponseWriter, r *http.Request, p c.Principal) error {
	if h.cfg.Store == nil {
		return problem(503, "execution_unavailable", "执行存储未配置")
	}
	if !validID(r.PathValue("id")) {
		return problem(404, "not_found", "未找到当前账号的执行记录")
	}
	record, err := h.cfg.Store.FindID(r.Context(), p.TenantID, p.Subject, r.PathValue("id"))
	if err != nil {
		return err
	}
	return h.deliver(w, r, p, record)
}

func (h *handler) deliver(w http.ResponseWriter, r *http.Request, p c.Principal, record Record) error {
	if err := h.checkRecord(r.Context(), r, p, record); err != nil {
		return err
	}
	if record.Invocation.Status == "running" && time.Now().After(record.DeadlineAt) {
		record.Invocation.Status = "interrupted"
		record.Invocation.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
		record.Invocation.Error = &c.Error{Code: "execution_interrupted", Message: "执行未在约定期限内完成，结果需重新发起计算核对"}
		saved, err := h.cfg.Store.Finish(r.Context(), record)
		if err != nil {
			return err
		}
		if !saved {
			record, err = h.cfg.Store.FindID(r.Context(), p.TenantID, p.Subject, record.Invocation.ID)
			if err != nil {
				return err
			}
		}
		if err := h.checkRecord(r.Context(), r, p, record); err != nil {
			return err
		}
	}
	status := 200
	if record.Invocation.Status == "running" {
		status = 202
	}
	if record.Invocation.Mode == "" {
		// Older persisted records predate the draft protocol and always invoked
		// the active definition. Preserve their wire identity on delivery.
		record.Invocation.Mode = c.InvocationModeCurrent
	}
	w.Header().Set("Location", "/execution/invocations/"+record.Invocation.ID)
	writeJSON(w, status, record.Invocation)
	return nil
}
