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
	if h.cfg.Authorizer == nil {
		return problem(503, "authorization_unavailable", "授权服务未配置")
	}
	operation := "function.invoke"
	if kind == c.KindAction {
		operation = "action.invoke"
	} else if kind != c.KindFunction {
		return problem(404, "not_found", "未找到可调用的当前定义")
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

type plan struct{ root, fn c.Definition }

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
	if err := h.authorize(ctx, p, out.root.Kind, out.root.ID); err != nil {
		return out, err
	}
	if out.root.Kind == c.KindAction {
		if out.root.Action == nil || out.root.Action.FunctionID == "" || out.root.Action.Runtime != nil || out.root.Action.SideEffects != "none" {
			return out, problem(422, "unsupported_action", "首批仅执行绑定无副作用Function的Action")
		}
		fn, err := h.cfg.Metadata.Current(ctx, p.TenantID, out.root.Action.FunctionID)
		if err != nil {
			return out, err
		}
		out.fn = fn.Definition
		if fn.Status != "active" || out.fn.Kind != c.KindFunction || out.fn.Function == nil || out.fn.TenantID != p.TenantID || out.fn.ID != out.root.Action.FunctionID {
			return out, problem(422, "invalid_function", "动作实现必须是同租户已生效Function")
		}
		if err := h.authorize(ctx, p, c.KindFunction, out.fn.ID); err != nil {
			return out, err
		}
		if !sameSignature(out.root.Action.Input, out.fn.Function.Input) || !sameSignature(out.root.Action.Output, out.fn.Function.Output) {
			return out, problem(422, "signature_mismatch", "动作与实现函数的输入输出声明不一致")
		}
	} else {
		out.fn = out.root
	}
	if err := executable(out.fn); err != nil {
		return out, err
	}
	return out, nil
}

func executable(fn c.Definition) error {
	if fn.Kind != c.KindFunction || fn.Function == nil || fn.Function.Runtime == nil {
		return problem(422, "runtime_missing", "此Function尚未配置Python实现")
	}
	callable := fn.Function
	runtime := callable.Runtime
	if callable.SideEffects != "none" || callable.FunctionID != "" || callable.ArtifactRef != "" || len(callable.TargetSchemaIDs) > 0 {
		return problem(422, "unsupported_runtime", "首批仅支持无副作用的内联Python计算")
	}
	if c.ValidateScriptRuntime(runtime) != nil {
		return problem(422, "unsupported_runtime", "Python源码、入口或运行时限无效")
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
	if _, err := h.cfg.Runner.Ready(ctx); err != nil {
		writeProblem(w, problem(503, "runtime_unavailable", "Python运行器尚未就绪"))
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ready"})
}

func (h *handler) capabilities(w http.ResponseWriter, r *http.Request, p c.Principal) error {
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	available := false
	if h.cfg.Runner != nil && h.cfg.Store != nil && h.cfg.Store.Ping(ctx) == nil {
		_, err := h.cfg.Runner.Ready(ctx)
		available = err == nil
	}
	out := map[string]any{"invocation": available, "runtimes": []string{"python"}, "synchronous": true, "async": false, "sideEffects": []string{"none"}, "maxTimeoutSeconds": 30, "maxConcurrent": cap(h.slots), "maxPayloadBytes": MaxPayloadBytes, "actionRequiresFunctionInvoke": true}
	if !available {
		out["reason"] = "Python运行器或执行存储尚未就绪"
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
	initial, err := h.plan(r.Context(), p, request.DefinitionID)
	if err != nil {
		return err
	}
	rootCallable := initial.root.Function
	if initial.root.Action != nil {
		rootCallable = initial.root.Action
	}
	namedInput, err := buildInput(rootCallable.Input, request.Input)
	if err != nil {
		return err
	}
	readyCtx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	imageID, err := h.cfg.Runner.Ready(readyCtx)
	cancel()
	if err != nil {
		return problem(503, "runtime_unavailable", "Python运行器尚未就绪")
	}
	now := time.Now().UTC()
	record := Record{TenantID: p.TenantID, SubjectID: p.Subject, RequestKey: key, RequestHash: hash, Root: initial.root, Function: initial.fn, Input: namedInput, ImageID: imageID, DeadlineAt: now.Add(time.Duration(initial.fn.Function.Runtime.TimeoutSeconds)*time.Second + 90*time.Second), Invocation: c.Invocation{ID: c.NewID(), DefinitionID: initial.root.ID, DefinitionName: initial.root.Name, Kind: initial.root.Kind, Status: "running", CreatedAt: now.Format(time.RFC3339Nano), CodeDigest: sourceDigest(initial.fn.Function.Runtime.Source)}}
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
	runCtx, stop := context.WithTimeout(r.Context(), time.Duration(initial.fn.Function.Runtime.TimeoutSeconds)*time.Second)
	output, runErr := h.cfg.Runner.Run(runCtx, initial.fn.Function.Runtime, namedInput, imageID)
	stop()
	if runErr != nil {
		var re *RunError
		if errors.As(runErr, &re) {
			record.Invocation.Status = re.Status
			record.Invocation.Error = &c.Error{Code: re.Code, Message: re.Message}
		} else {
			record.Invocation.Status = "failed"
			record.Invocation.Error = &c.Error{Code: "runtime_failed", Message: "Python执行失败"}
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
	current, err := h.plan(ctx, p, record.Root.ID)
	if err != nil {
		return err
	}
	if digest(current.root) != digest(record.Root) || digest(current.fn) != digest(record.Function) {
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
	w.Header().Set("Location", "/execution/invocations/"+record.Invocation.ID)
	writeJSON(w, status, record.Invocation)
	return nil
}
