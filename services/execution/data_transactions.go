package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
)

type DataTransactionClient interface {
	Execute(context.Context, c.ExecutionTransactionRequest) (c.WriteTransactionResult, error)
}

type RemoteDataTransactions struct {
	URL, ServiceToken string
	Client            *http.Client
}

func dataTransactionsConfigured(client DataTransactionClient) bool {
	if client == nil {
		return false
	}
	if remote, ok := client.(*RemoteDataTransactions); ok {
		return remote.configured()
	}
	return true
}

func (d *RemoteDataTransactions) configured() bool {
	if d == nil || len(d.ServiceToken) < 32 {
		return false
	}
	u, err := url.Parse(d.URL)
	return err == nil && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && (u.Scheme == "http" || u.Scheme == "https")
}

func (d *RemoteDataTransactions) Execute(ctx context.Context, input c.ExecutionTransactionRequest) (c.WriteTransactionResult, error) {
	var out c.WriteTransactionResult
	if !d.configured() {
		return out, problem(503, "data_transactions_unavailable", "数据事务服务未正确配置")
	}
	raw, err := json.Marshal(input)
	if err != nil || len(raw) > c.MaxDataRequestBytes {
		return out, problem(422, "invalid_write_plan", "事务计划超过请求预算")
	}
	callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(callCtx, http.MethodPost, strings.TrimRight(d.URL, "/")+"/internal/execution/transactions", bytes.NewReader(raw))
	if err != nil {
		return out, problem(503, "data_transactions_unavailable", "数据事务地址无效")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+d.ServiceToken)
	client := http.Client{Timeout: 20 * time.Second}
	if d.Client != nil {
		client = *d.Client
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		return out, problem(503, "transaction_pending", "尚不能确认事务提交结果，请使用原请求标识核对")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return out, problem(503, "transaction_pending", "数据事务尚未返回可核对的终态，请使用原请求标识核对")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, c.MaxDataResponseBytes+1))
	if err != nil || len(body) > c.MaxDataResponseBytes {
		return out, problem(503, "transaction_pending", "事务回执暂不可读取")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&out) != nil || decoder.Decode(new(any)) != io.EOF || !out.Atomic || out.Committed != (out.Status == "committed") || (out.Status != "committed" && out.Status != "failed" && out.Status != "not_found") || (!input.Lookup && out.Status == "not_found") {
		return c.WriteTransactionResult{}, problem(503, "transaction_pending", "事务回执暂不可确认")
	}
	return out, nil
}

func supportedSideEffects(callable *c.Callable) bool {
	if callable.SideEffects == "none" {
		return len(callable.DataWrite) == 0
	}
	if callable.SideEffects != "dataWrite" || len(callable.DataWrite) == 0 || len(callable.DataWrite) > 32 {
		return false
	}
	seen := map[string]bool{}
	for _, grant := range callable.DataWrite {
		if !validID(grant.SchemaID) || seen[grant.SchemaID] || len(grant.Operations) == 0 || len(grant.Operations) > 4 {
			return false
		}
		seen[grant.SchemaID] = true
		operations := map[string]bool{}
		for _, operation := range grant.Operations {
			if operations[operation] || (operation != "create" && operation != "update" && operation != "delete" && operation != "upsert") {
				return false
			}
			operations[operation] = true
		}
	}
	return true
}

func sameDataWrite(a, b []c.DataWriteCapability) bool {
	normalize := func(in []c.DataWriteCapability) []c.DataWriteCapability {
		out := make([]c.DataWriteCapability, len(in))
		for i, v := range in {
			out[i] = c.DataWriteCapability{SchemaID: v.SchemaID, Operations: append([]string{}, v.Operations...)}
			sort.Strings(out[i].Operations)
		}
		sort.Slice(out, func(i, j int) bool { return out[i].SchemaID < out[j].SchemaID })
		return out
	}
	return digest(normalize(a)) == digest(normalize(b))
}

func allowsDataWrite(callable *c.Callable, op c.TransactionOperation) bool {
	if callable == nil || !supportedSideEffects(callable) || callable.SideEffects != "dataWrite" {
		return false
	}
	for _, grant := range callable.DataWrite {
		if grant.SchemaID == op.SchemaID {
			for _, name := range grant.Operations {
				if name == op.Operation {
					return true
				}
			}
		}
	}
	return false
}

func (h *handler) prepareDataTransaction(w http.ResponseWriter, r *http.Request, p c.Principal, record Record, operations []c.TransactionOperation) error {
	if len(operations) > c.MaxBulkWriteItems {
		record.Invocation.Status, record.Invocation.Output = "failed", nil
		record.Invocation.Error = &c.Error{Code: "invalid_write_plan", Message: "事务最多包含100个写操作"}
		return h.finish(w, r, p, record, 201)
	}
	for _, operation := range operations {
		if !allowsDataWrite(definitionCallable(record.Root), operation) || !allowsDataWrite(definitionCallable(record.Implementation), operation) {
			record.Invocation.Status, record.Invocation.Output = "failed", nil
			record.Invocation.Error = &c.Error{Code: "data_scope_denied", Message: "写入计划超出定义声明的数据操作范围，未提交任何写入"}
			return h.finish(w, r, p, record, 201)
		}
	}
	if err := h.checkRecord(r.Context(), r, p, record); err != nil {
		return err
	}
	record.Transaction = &c.WriteTransactionRequest{IdempotencyKey: "execution:" + record.Invocation.ID, Operations: operations}
	record.PreparedOutput, record.Invocation.Output = record.Invocation.Output, nil
	record.Invocation.Status = "committing"
	record.Invocation.Transaction = &c.InvocationTransaction{Status: "pending", OperationCount: len(operations)}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	saved, err := h.cfg.Store.(PreparingStore).Prepare(ctx, record)
	cancel()
	if err != nil {
		return err
	}
	if !saved {
		current, err := h.cfg.Store.FindID(r.Context(), p.TenantID, p.Subject, record.Invocation.ID)
		if err != nil {
			return err
		}
		return h.deliver(w, r, p, current)
	}
	return h.resolveDataTransaction(w, r, p, record, false, 201)
}

func (h *handler) resolveDataTransaction(w http.ResponseWriter, r *http.Request, p c.Principal, record Record, lookup bool, status int) error {
	if record.Transaction == nil || record.Invocation.Mode == c.InvocationModeDraft {
		return problem(409, "invalid_record", "事务恢复记录无效")
	}
	if err := h.checkRecord(r.Context(), r, p, record); err != nil {
		return err
	}
	check, err := platform.SessionRequest(r)
	if err != nil {
		return err
	}
	request := c.ExecutionTransactionRequest{Session: check, InvocationID: record.Invocation.ID, DefinitionID: record.Root.ID, DefinitionDigest: digest(record.Root), ImplementationID: record.Implementation.ID, ImplementationDigest: digest(record.Implementation), Transaction: *record.Transaction, Lookup: lookup}
	var result c.WriteTransactionResult
	if !dataTransactionsConfigured(h.cfg.DataTransactions) {
		err = problem(503, "data_transactions_unavailable", "数据事务服务未配置")
	} else {
		result, err = h.cfg.DataTransactions.Execute(r.Context(), request)
	}
	if err != nil || result.Status == "not_found" {
		// A missing receipt cannot rule out an in-flight or lost-response commit.
		// Never convert this durable state to failed/interrupted.
		record.Invocation.Output = nil
		record.Invocation.Error = &c.Error{Code: "transaction_pending", Message: "事务提交待确认；可查询状态，或使用原请求标识重试提交。不会重新运行脚本"}
		w.Header().Set("Location", "/execution/invocations/"+record.Invocation.ID)
		writeJSON(w, 202, record.Invocation)
		return nil
	}
	if result.Status != "committed" && result.Status != "failed" {
		return problem(503, "transaction_pending", "事务尚无可确认终态")
	}
	record.Invocation.Transaction = &c.InvocationTransaction{Status: result.Status, Committed: result.Committed, OperationCount: len(record.Transaction.Operations)}
	record.Invocation.Error = nil
	if result.Committed {
		record.Invocation.Status, record.Invocation.Output = "succeeded", record.PreparedOutput
	} else {
		record.Invocation.Status, record.Invocation.Output = "failed", nil
		record.Invocation.Error = result.Error
		if record.Invocation.Error == nil {
			record.Invocation.Error = &c.Error{Code: "transaction_failed", Message: "事务未提交，所有写入已回滚"}
		}
	}
	return h.finish(w, r, p, record, status)
}
