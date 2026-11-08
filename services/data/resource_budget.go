package data

import (
	"bytes"
	"context"
	"encoding"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

const dataRequestTimeout = 15 * time.Second

type dataResourceLimiter struct {
	slots   chan struct{}
	pending chan struct{}
}
type dataBudgetContextKey struct{}

// All Data handlers in one process share admission; a request context can only
// skip a nested wrapper belonging to the same limiter that already admitted it.
var dataServiceLimiter = &dataResourceLimiter{slots: make(chan struct{}, c.MaxDataConcurrentRequests), pending: make(chan struct{}, c.MaxDataQueuedRequests)}

func responseBudgetError() error {
	return problem(422, "response_budget", "响应超过 8 MiB，请减少查询字段或分页大小后重试")
}

// jsonBudget checks the encoded size before JSON encoding allocates its buffer.
// RawMessage, UTF-8 replacement and HTML escaping are counted conservatively;
// all other supported values are the plain JSON contract types.
type jsonBudget struct{ remaining int }

func (b *jsonBudget) take(n int) error {
	if n < 0 || n > b.remaining {
		return responseBudgetError()
	}
	b.remaining -= n
	return nil
}

func (b *jsonBudget) add(v any) error { return b.value(reflect.ValueOf(v), 0) }

func (b *jsonBudget) text(s string) error {
	if err := b.take(2); err != nil {
		return err
	}
	if len(s) > b.remaining {
		return responseBudgetError()
	}
	for i := 0; i < len(s); {
		size, width := 1, 1
		switch char := s[i]; {
		case char == '"' || char == '\\':
			size = 2
		case char == '\b' || char == '\f' || char == '\n' || char == '\r' || char == '\t':
			size = 2
		case char < 0x20 || char == '<' || char == '>' || char == '&':
			size = 6
		case char >= utf8.RuneSelf:
			r, n := utf8.DecodeRuneInString(s[i:])
			width = n
			size = n
			if r == utf8.RuneError && n == 1 {
				size = 6
			}
			if r == '\u2028' || r == '\u2029' {
				size = 6
			}
		}
		if err := b.take(size); err != nil {
			return err
		}
		i += width
	}
	return nil
}

func emptyJSONValue(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return v.Len() == 0
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64, reflect.Interface, reflect.Pointer:
		return v.IsZero()
	}
	return false
}

func (b *jsonBudget) value(v reflect.Value, depth int) error {
	if depth > 64 {
		return responseBudgetError()
	}
	if !v.IsValid() {
		return b.take(4)
	}
	typeOfValue := v.Type()
	knownType := typeOfValue
	if knownType.Kind() == reflect.Pointer {
		knownType = knownType.Elem()
	}
	if knownType != reflect.TypeFor[json.RawMessage]() && knownType != reflect.TypeFor[time.Time]() &&
		(typeOfValue.Implements(reflect.TypeFor[json.Marshaler]()) || typeOfValue.Implements(reflect.TypeFor[encoding.TextMarshaler]()) ||
			v.CanAddr() && (reflect.PointerTo(typeOfValue).Implements(reflect.TypeFor[json.Marshaler]()) || reflect.PointerTo(typeOfValue).Implements(reflect.TypeFor[encoding.TextMarshaler]()))) {
		return errors.New("custom JSON encoders require an explicit response size contract")
	}
	if v.Kind() == reflect.Interface || v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return b.take(4)
		}
		return b.value(v.Elem(), depth+1)
	}
	if v.Type() == reflect.TypeFor[json.RawMessage]() {
		raw := v.Bytes()
		if raw == nil {
			return b.take(4)
		}
		if err := b.take(len(raw)); err != nil {
			return err
		}
		// encoding/json may expand unescaped HTML characters in a RawMessage.
		for i := 0; i < len(raw); i++ {
			if raw[i] == '<' || raw[i] == '>' || raw[i] == '&' {
				if err := b.take(5); err != nil {
					return err
				}
			}
			if i+2 < len(raw) && raw[i] == 0xe2 && raw[i+1] == 0x80 && (raw[i+2] == 0xa8 || raw[i+2] == 0xa9) {
				if err := b.take(3); err != nil {
					return err
				}
				i += 2
			}
		}
		return nil
	}
	if v.Type() == reflect.TypeFor[time.Time]() {
		return b.take(40)
	}
	switch v.Kind() {
	case reflect.Bool:
		if v.Bool() {
			return b.take(4)
		}
		return b.take(5)
	case reflect.String:
		return b.text(v.String())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return b.take(len(strconv.FormatInt(v.Int(), 10)))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return b.take(len(strconv.FormatUint(v.Uint(), 10)))
	case reflect.Float32, reflect.Float64:
		return b.take(32)
	case reflect.Slice:
		if v.IsNil() {
			return b.take(4)
		}
		if v.Type().Elem().Kind() == reflect.Uint8 {
			return b.take(2 + (v.Len()+2)/3*4)
		}
		fallthrough
	case reflect.Array:
		if err := b.take(2 + v.Len()); err != nil {
			return err
		}
		for i := 0; i < v.Len(); i++ {
			if err := b.value(v.Index(i), depth+1); err != nil {
				return err
			}
		}
		return nil
	case reflect.Map:
		if v.IsNil() {
			return b.take(4)
		}
		if v.Type().Key().Kind() != reflect.String {
			return errors.New("unsupported JSON map key")
		}
		if err := b.take(2 + 2*v.Len()); err != nil {
			return err
		}
		iter := v.MapRange()
		for iter.Next() {
			if err := b.text(iter.Key().String()); err != nil {
				return err
			}
			if err := b.value(iter.Value(), depth+1); err != nil {
				return err
			}
		}
		return nil
	case reflect.Struct:
		if err := b.take(2); err != nil {
			return err
		}
		for i := 0; i < v.NumField(); i++ {
			field, value := v.Type().Field(i), v.Field(i)
			if field.PkgPath != "" {
				embeddedType := field.Type
				if embeddedType.Kind() == reflect.Pointer {
					embeddedType = embeddedType.Elem()
				}
				if !field.Anonymous || embeddedType.Kind() != reflect.Struct {
					continue
				}
			}
			tag := strings.Split(field.Tag.Get("json"), ",")
			if tag[0] == "-" {
				continue
			}
			omit := false
			for _, option := range tag[1:] {
				if option == "string" {
					return errors.New("quoted JSON fields require an explicit response size contract")
				}
				if option == "omitempty" && emptyJSONValue(value) {
					omit = true
				}
			}
			if omit {
				continue
			}
			if field.Anonymous && tag[0] == "" {
				if err := b.value(value, depth+1); err != nil {
					return err
				}
				continue
			}
			name := tag[0]
			if name == "" {
				name = field.Name
			}
			if err := b.take(2); err != nil {
				return err
			}
			if err := b.text(name); err != nil {
				return err
			}
			if err := b.value(value, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	return errors.New("unsupported JSON value")
}

func newQueryBudget(fields any) (*jsonBudget, error) {
	// Reserve the enclosing object, per-row separators, ETags and a maximal
	// continuation token before accumulating any business values.
	b := &jsonBudget{remaining: c.MaxDataResponseBytes - (16 << 10)}
	return b, b.add(fields)
}

func (b *jsonBudget) checkDatabaseRow(values []any) error {
	check := *b
	for _, value := range values {
		if raw, ok := value.([]byte); ok {
			if len(raw) > check.remaining-2 {
				return responseBudgetError()
			}
			if err := check.text(string(raw)); err != nil {
				return err
			}
		} else if err := check.add(value); err != nil {
			return err
		}
	}
	return nil
}

type boundedJSONBuffer struct {
	bytes.Buffer
	limit int
}

func (b *boundedJSONBuffer) Write(raw []byte) (int, error) {
	if len(raw) > b.limit-b.Len() {
		return 0, responseBudgetError()
	}
	return b.Buffer.Write(raw)
}

func encodeBudgetedJSON(v any) ([]byte, error) {
	budget := jsonBudget{remaining: c.MaxDataResponseBytes - 1}
	if err := budget.add(v); err != nil {
		return nil, err
	}
	buf := &boundedJSONBuffer{limit: c.MaxDataResponseBytes}
	if err := json.NewEncoder(buf).Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Queued requests have not parsed a session/body or begun a transaction. Both
// waiting count and duration are bounded independently of executing requests.
func (limiter *dataResourceLimiter) acquire(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case limiter.slots <- struct{}{}:
		return nil
	default:
	}
	select {
	case limiter.pending <- struct{}{}:
	default:
		return problem(429, "data_busy", "Data 请求较多，请稍后重试")
	}
	defer func() { <-limiter.pending }()
	timer := time.NewTimer(time.Duration(c.MaxDataQueueWaitMS) * time.Millisecond)
	defer timer.Stop()
	select {
	case limiter.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return problem(429, "data_busy", "Data 请求较多，请稍后重试")
	}
}

// Health probes bypass business admission so overload remains observable.
func withDataBudgets(next http.Handler) http.Handler {
	return dataServiceLimiter.protect(next)
}

// ProtectResources must wrap the public session middleware in the service
// entrypoint so identity authority calls consume the same bounded admission.
// NewHandler remains protected when used directly; nested wrappers share their
// limiter ownership rather than admitting or buffering the same request twice.
func ProtectResources(next http.Handler) http.Handler { return withDataBudgets(next) }

func (limiter *dataResourceLimiter) protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if owner, ok := r.Context().Value(dataBudgetContextKey{}).(*dataResourceLimiter); ok && owner == limiter {
			next.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			next.ServeHTTP(w, r)
			return
		}
		timeout := dataRequestTimeout
		if r.URL.Path == "/data/query/context" {
			// Include admission and authority RPCs in the directory deadline,
			// leaving transport margin within OntoApp's five-second upstream cap.
			timeout = 4 * time.Second
		}
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		if err := limiter.acquire(ctx); err != nil {
			var api *apiError
			if errors.As(err, &api) && api.status == 429 {
				w.Header().Set("Retry-After", "1")
			}
			writeError(w, err)
			return
		}
		defer func() { <-limiter.slots }()
		if err := ctx.Err(); err != nil {
			writeError(w, err)
			return
		}
		ctx = context.WithValue(ctx, dataBudgetContextKey{}, limiter)
		response := &dataResponse{header: w.Header().Clone(), body: boundedJSONBuffer{limit: c.MaxDataResponseBytes}}
		next.ServeHTTP(response, r.WithContext(ctx))
		if err := ctx.Err(); err != nil {
			writeError(w, err)
			return
		}
		if response.err != nil {
			writeError(w, response.err)
			return
		}
		for key, values := range response.header {
			w.Header()[key] = values
		}
		status := response.status
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		_, _ = io.Copy(w, bytes.NewReader(response.body.Bytes()))
	})
}

type dataResponse struct {
	header http.Header
	body   boundedJSONBuffer
	status int
	err    error
}

func (w *dataResponse) Header() http.Header { return w.header }
func (w *dataResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *dataResponse) Write(raw []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if w.err != nil {
		return 0, w.err
	}
	n, err := w.body.Write(raw)
	if err != nil {
		w.err = err
		w.body.Reset()
	}
	return n, err
}
