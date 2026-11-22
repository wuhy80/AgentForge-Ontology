package execution

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
)

type jobAdmission struct {
	SessionFingerprint string    `json:"sessionFingerprint"`
	ExpiresAt          time.Time `json:"expiresAt"`
}

func (h *handler) enqueueJob(w http.ResponseWriter, r *http.Request, p c.Principal, key, hash string, initial plan, input map[string]json.RawMessage) error {
	if h.jobs == nil {
		return problem(503, "async_unavailable", "持久任务尚未配置")
	}
	if !pureJobCallable(initial.root) || !pureJobCallable(initial.implementation) {
		return problem(422, "unsupported_async", "持久任务仅支持无业务数据读取、无写入声明的当前态计算")
	}
	check, err := platform.SessionRequest(r)
	if err != nil {
		return err
	}
	if check.Token == "" {
		return problem(403, "connection_scope_denied", "持久任务仅支持浏览器会话")
	}
	fingerprint := sourceDigest(check.Token)
	now := time.Now().UTC()
	record := Record{TenantID: p.TenantID, SubjectID: p.Subject, RequestKey: key, RequestHash: hash,
		Root: initial.root, Implementation: initial.implementation, Input: input, DeadlineAt: now,
		Job: &jobAdmission{SessionFingerprint: fingerprint, ExpiresAt: now.Add(15 * time.Minute)},
		Invocation: c.Invocation{ID: c.NewID(), DefinitionID: initial.root.ID, DefinitionName: initial.root.Name,
			Kind: initial.root.Kind, Status: "queued", CreatedAt: now.Format(jobTimeLayout),
			CodeDigest: sourceDigest(definitionCallable(initial.implementation).Runtime.Source), Mode: c.InvocationModeCurrent}}
	claimed, err := h.jobs.admitJob(r.Context(), record)
	if err != nil {
		return err
	}
	if !claimed {
		previous, err := h.cfg.Store.FindKey(r.Context(), p.TenantID, p.Subject, key)
		if err != nil {
			return err
		}
		return h.replay(w, r, p, previous, hash)
	}
	return h.deliverJob(w, r, p, record)
}

func pureJobCallable(definition c.Definition) bool {
	callable := definitionCallable(definition)
	return callable != nil && (callable.SideEffects == "" || callable.SideEffects == "none") &&
		callable.RecordRead == nil && len(callable.DataWrite) == 0
}

func (h *handler) jobPrincipal(ctx context.Context, record Record) (c.Principal, error) {
	session, err := h.cfg.SessionChecker.CheckSession(ctx, c.SessionCheck{Method: http.MethodGet, SessionFingerprint: record.Job.SessionFingerprint})
	if err != nil {
		return c.Principal{}, err
	}
	if !session.User.Enabled || session.User.ID != record.SubjectID || session.User.TenantID != record.TenantID || session.ExpiresAt <= time.Now().Unix() {
		return c.Principal{}, problem(401, "unauthenticated", "任务提交会话已失效")
	}
	return c.Principal{Subject: session.User.ID, TenantID: session.User.TenantID, ExpiresAt: session.ExpiresAt}, nil
}

func (h *handler) jobPlan(ctx context.Context, p c.Principal, record Record) error {
	current, err := h.plan(ctx, p, record.Root.ID)
	if err != nil {
		return err
	}
	if digest(current.root) != digest(record.Root) || digest(current.implementation) != digest(record.Implementation) {
		return problem(409, "definition_changed", "任务的定义或实现已经改变")
	}
	return nil
}

func (h *handler) deliverJob(w http.ResponseWriter, r *http.Request, p c.Principal, record Record) error {
	if platform.ValidAccessToken(platform.Bearer(r)) {
		return problem(403, "connection_scope_denied", "持久任务仅允许浏览器会话读取")
	}
	status := http.StatusOK
	switch record.Invocation.Status {
	case "queued", "running":
		status = http.StatusAccepted
	case "succeeded":
		if _, err := h.jobPrincipal(r.Context(), record); err != nil {
			return err
		}
		if err := h.jobPlan(r.Context(), p, record); err != nil {
			return err
		}
	}
	w.Header().Set("Location", "/execution/invocations/"+record.Invocation.ID)
	writeJSON(w, status, record.Invocation)
	return nil
}

func (h *handler) cancelJob(w http.ResponseWriter, r *http.Request, p c.Principal) error {
	if h.jobs == nil || !validID(r.PathValue("id")) {
		return sql.ErrNoRows
	}
	if platform.ValidAccessToken(platform.Bearer(r)) {
		return problem(403, "connection_scope_denied", "持久任务仅允许浏览器会话取消")
	}
	record, err := h.jobs.cancelJob(r.Context(), p.TenantID, p.Subject, r.PathValue("id"))
	if err != nil {
		return err
	}
	return h.deliverJob(w, r, p, record)
}

const jobTimeLayout = "2006-01-02T15:04:05.000000000Z"

func (h *handler) listJobs(w http.ResponseWriter, r *http.Request, p c.Principal) error {
	if h.jobs == nil {
		return problem(503, "async_unavailable", "持久任务尚未配置")
	}
	if platform.ValidAccessToken(platform.Bearer(r)) {
		return problem(403, "connection_scope_denied", "任务列表仅支持浏览器会话")
	}
	created, id := "9999", ""
	if cursor := r.URL.Query().Get("cursor"); cursor != "" {
		if len(cursor) > 128 {
			return problem(400, "invalid_cursor", "任务分页凭证无效")
		}
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		var ok bool
		created, id, ok = strings.Cut(string(raw), "/")
		_, timeErr := time.Parse(time.RFC3339Nano, created)
		if err != nil || timeErr != nil || !ok || len(id) != 36 || !validID(id) {
			return problem(400, "invalid_cursor", "任务分页凭证无效")
		}
	}
	const limit = 20
	items, err := h.jobs.listJobs(r.Context(), p.TenantID, p.Subject, created, id, limit+1)
	if err != nil {
		return err
	}
	out := c.InvocationPage{Data: items[:min(len(items), limit)]}
	if len(items) > limit {
		last := out.Data[limit-1]
		out.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(last.CreatedAt + "/" + last.ID))
	}
	writeJSON(w, 200, out)
	return nil
}

func (h *handler) workJobs(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		select {
		case h.slots <- struct{}{}:
		default:
			continue
		}
		claimCtx, stop := context.WithTimeout(ctx, 5*time.Second)
		record, err := h.jobs.leaseJob(claimCtx)
		stop()
		if err == nil {
			created, _ := time.Parse(time.RFC3339Nano, record.Invocation.CreatedAt)
			log.Printf("execution job claimed attempt=%d queue_age_ms=%d", record.Invocation.Attempt, time.Since(created).Milliseconds())
			h.runJob(ctx, record)
		} else if ctx.Err() == nil && !errors.Is(err, sql.ErrNoRows) {
			log.Printf("execution job lease failed: %T", err)
		}
		<-h.slots
	}
}

func closeJob(record *Record, status, code, message string) {
	record.Invocation.Status = status
	record.Invocation.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if code != "" {
		record.Invocation.Error = &c.Error{Code: code, Message: message}
	}
}

func (h *handler) runJob(ctx context.Context, record Record) {
	leaseCtx, cancelLease := context.WithCancel(ctx)
	defer cancelLease()
	runCtx, cancel := context.WithTimeout(leaseCtx, 50*time.Second)
	defer cancel()
	go h.monitorJobLease(leaseCtx, cancelLease, record)
	h.computeJob(runCtx, &record)
	if leaseCtx.Err() != nil {
		return // A replacement worker can reclaim the expired lease.
	}
	saveCtx, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	if err := h.jobs.finishJob(saveCtx, record); err != nil {
		log.Printf("execution job completion failed: %T", err)
		return
	}
	created, _ := time.Parse(time.RFC3339Nano, record.Invocation.CreatedAt)
	code := ""
	if record.Invocation.Error != nil {
		code = record.Invocation.Error.Code
	}
	log.Printf("execution job attempt=%d status=%s code=%s age_ms=%d", record.Invocation.Attempt,
		record.Invocation.Status, code, time.Since(created).Milliseconds())
}

func (h *handler) monitorJobLease(ctx context.Context, cancel context.CancelFunc, record Record) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		checkCtx, stop := context.WithTimeout(ctx, 2*time.Second)
		active, err := h.jobs.jobActive(checkCtx, record)
		stop()
		if err != nil || !active {
			cancel()
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (h *handler) computeJob(ctx context.Context, record *Record) {
	if record.Invocation.Attempt > 3 || time.Now().After(record.Job.ExpiresAt) {
		closeJob(record, "failed", "job_retry_exhausted", "任务已超过执行期限或尝试次数")
		return
	}
	if err := h.checkJobAccess(ctx, *record); err != nil {
		failJobAccess(record, err)
		return
	}
	runtime := definitionCallable(record.Implementation).Runtime
	readinessCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	image, err := readyRuntime(readinessCtx, h.cfg.Runner, runtime.Language)
	cancel()
	if err != nil {
		retryJob(record, "runtime_unavailable", "运行器尚未就绪")
		return
	}
	scriptCtx, stop := context.WithTimeout(ctx, time.Duration(runtime.TimeoutSeconds)*time.Second)
	result, err := h.cfg.Runner.Run(scriptCtx, runtime, record.Input, image)
	stop()
	if err != nil {
		var failure *RunError
		if errors.As(err, &failure) {
			switch failure.Code {
			case "runtime_unavailable", "cleanup_unconfirmed":
				retryJob(record, failure.Code, failure.Message)
			default:
				closeJob(record, failure.Status, failure.Code, failure.Message)
			}
		} else {
			closeJob(record, "failed", "runtime_failed", "任务计算失败")
		}
		return
	}
	output, err := buildOutput(definitionCallable(record.Root).Output, result)
	if err != nil {
		closeJob(record, "failed", "invalid_output", "函数输出未通过声明校验")
		return
	}
	if err := h.checkJobAccess(ctx, *record); err != nil {
		failJobAccess(record, err)
		return
	}
	record.Invocation.Output = output
	record.ImageID = image
	closeJob(record, "succeeded", "", "")
}

func (h *handler) checkJobAccess(ctx context.Context, record Record) error {
	p, err := h.jobPrincipal(ctx, record)
	if err == nil {
		err = h.jobPlan(ctx, p, record)
	}
	return err
}

func failJobAccess(record *Record, err error) {
	if temporaryJobError(err) {
		retryJob(record, "job_authority_unavailable", "暂时无法确认任务权限或定义")
		return
	}
	closeJob(record, "failed", "authorization_or_definition_changed", "任务会话、权限或定义无法复核")
}

func temporaryJobError(err error) bool {
	var api *apiError
	var authority *platform.AccessError
	return errors.Is(err, context.DeadlineExceeded) || errors.As(err, &api) && api.status >= 500 ||
		errors.As(err, &authority) && authority.Status >= 500
}

func retryJob(record *Record, code, message string) {
	delay := time.Duration(5*record.Invocation.Attempt) * time.Second
	if record.Invocation.Attempt >= 3 || !time.Now().Add(delay).Before(record.Job.ExpiresAt) {
		closeJob(record, "failed", code, message)
		return
	}
	record.Invocation.Status = "queued"
	record.Invocation.Error = &c.Error{Code: code, Message: message}
}
