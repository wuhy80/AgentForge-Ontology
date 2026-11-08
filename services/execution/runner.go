package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

type Runner interface {
	Ready(context.Context) (string, error)
	Run(context.Context, *c.ScriptRuntime, map[string]json.RawMessage, string) (map[string]json.RawMessage, error)
}

type RunError struct{ Status, Code, Message string }

func (e *RunError) Error() string { return e.Code + ": " + e.Message }

type DiagnosticRunError struct {
	*RunError
	Diagnostics []c.SourceDiagnostic
}

func (e *DiagnosticRunError) Unwrap() error { return e.RunError }

// DockerRunner never invokes host language runtimes, mounts host paths, forwards host
// environment variables or pulls a user-selected image. The configured image
// is resolved to its actual local image ID before each admitted invocation.
type DockerRunner struct {
	Binary, Image, JavaScriptImage, GoImage string
	Isolation                               string
	mu                                      sync.Mutex
	pending                                 map[string]bool
	command                                 func(context.Context, string, ...string) *exec.Cmd
}

func (d *DockerRunner) exec(ctx context.Context, args ...string) *exec.Cmd {
	var cmd *exec.Cmd
	if d.command != nil {
		cmd = d.command(ctx, d.binary(), args...)
	} else {
		cmd = exec.CommandContext(ctx, d.binary(), args...)
	}
	cmd.WaitDelay = time.Second
	return cmd
}

func (d *DockerRunner) binary() string {
	if d.Binary != "" {
		return d.Binary
	}
	return "docker"
}
func (d *DockerRunner) imageFor(language string) string {
	switch language {
	case "python":
		if d.Image != "" {
			return d.Image
		}
		return "python:3.12-alpine"
	case "javascript":
		if d.JavaScriptImage != "" {
			return d.JavaScriptImage
		}
		return "node:22-alpine"
	case "go":
		if d.GoImage != "" {
			return d.GoImage
		}
		return "agentforge-go:1.26.10"
	}
	return ""
}

var imageIDPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

func (d *DockerRunner) Ready(ctx context.Context) (string, error) {
	return d.ReadyRuntime(ctx, "python")
}

func (d *DockerRunner) ReadyRuntime(ctx context.Context, language string) (string, error) {
	image := d.imageFor(language)
	if image == "" {
		return "", errors.New("unsupported runtime")
	}
	if err := d.CheckIsolation(ctx); err != nil {
		return "", err
	}
	if err := d.recoverContainers(ctx); err != nil {
		return "", err
	}
	cmd := d.exec(ctx, "image", "inspect", "--format", "{{.Id}} {{.Os}}", image)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	if cmd.Run() != nil {
		return "", errors.New("configured runtime image is unavailable")
	}
	parts := strings.Fields(out.String())
	if len(parts) != 2 || !imageIDPattern.MatchString(parts[0]) || parts[1] != "linux" {
		return "", errors.New("configured runtime image must be a Linux image")
	}
	return parts[0], nil
}

// Only expired containers carrying our fixed marker are recovered across
// process restarts. Live containers belonging to other replicas are untouched.
func (d *DockerRunner) recoverContainers(ctx context.Context) error {
	cmd := d.exec(ctx, "ps", "--all", "--filter", "label=ontology.execution.runtime", "--format", `{{.Names}} {{.Label "ontology.execution.deadline"}}`)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	if cmd.Run() != nil {
		return errors.New("cannot confirm execution container cleanup")
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) > 256 {
		return errors.New("execution container recovery budget exceeded")
	}
	d.mu.Lock()
	if d.pending == nil {
		d.pending = map[string]bool{}
	}
	for _, line := range lines {
		parts := strings.Fields(line)
		if len(parts) != 2 || !(strings.HasPrefix(parts[0], "ontology-python-") || strings.HasPrefix(parts[0], "ontology-javascript-") || strings.HasPrefix(parts[0], "ontology-go-")) {
			continue
		}
		deadline, err := strconv.ParseInt(parts[1], 10, 64)
		if err == nil && deadline < time.Now().Unix() {
			d.pending[parts[0]] = true
		}
	}
	names := make([]string, 0, len(d.pending))
	for name := range d.pending {
		names = append(names, name)
	}
	d.mu.Unlock()
	for _, name := range names {
		if err := d.cleanupName(ctx, name); err != nil {
			return err
		}
		d.mu.Lock()
		delete(d.pending, name)
		d.mu.Unlock()
	}
	return nil
}

func (d *DockerRunner) cleanupName(ctx context.Context, name string) error {
	for attempt := 0; attempt < 3; attempt++ {
		remove := d.exec(ctx, "rm", "--force", name)
		remove.Stdout = io.Discard
		remove.Stderr = io.Discard
		if remove.Run() == nil {
			return nil
		}
		// A normally completed --rm container may already be absent. A daemon
		// error is never treated as evidence that the container has stopped.
		inspect := d.exec(ctx, "ps", "--all", "--filter", "name=^/"+name+"$", "--format", "{{.Names}}")
		var out bytes.Buffer
		inspect.Stdout = &out
		inspect.Stderr = io.Discard
		if inspect.Run() == nil && strings.TrimSpace(out.String()) == "" {
			return nil
		}
		if ctx.Err() != nil {
			break
		}
		if attempt < 2 {
			select {
			case <-time.After(100 * time.Millisecond):
			case <-ctx.Done():
				return errors.New("cannot confirm execution container cleanup")
			}
		}
	}
	return errors.New("cannot confirm execution container cleanup")
}

func (d *DockerRunner) cleanup(name string) error {
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	if err := d.cleanupName(ctx, name); err != nil {
		d.mu.Lock()
		if d.pending == nil {
			d.pending = map[string]bool{}
		}
		d.pending[name] = true
		d.mu.Unlock()
		return err
	}
	return nil
}

const pythonWrapper = `import json, sys, contextlib, os
try:
    payload = json.load(sys.stdin)
    namespace = {"__name__": "ontology_function"}
    with open(os.devnull, "w") as quiet:
        with contextlib.redirect_stdout(quiet), contextlib.redirect_stderr(quiet):
            exec(compile(payload["source"], "<function>", "exec"), namespace, namespace)
            handler = namespace.get(payload["entrypoint"])
            if not callable(handler):
                raise ValueError("missing entrypoint")
            result = handler(payload["input"])
    if not isinstance(result, dict):
        raise ValueError("output must be an object")
    encoded = json.dumps(result, ensure_ascii=False, allow_nan=False, separators=(",", ":"))
    if len(encoded.encode("utf-8")) > 262144:
        raise ValueError("output budget")
    sys.stdout.write(encoded)
    sys.stdout.flush()
except BaseException as failure:
    try:
        diagnostic = {"code": "python_error", "line": 0}
        if isinstance(failure, SyntaxError) and failure.filename == "<function>":
            diagnostic.update(code="syntax_error", line=failure.lineno or 0,
                              column=failure.offset or 0, endLine=failure.end_lineno or 0,
                              endColumn=failure.end_offset or 0)
        else:
            frame = failure.__traceback__
            while frame is not None:
                if frame.tb_frame.f_code.co_filename == "<function>":
                    diagnostic["line"] = frame.tb_lineno
                frame = frame.tb_next
        sys.stderr.write(json.dumps(diagnostic, separators=(",", ":")))
        sys.stderr.flush()
    except BaseException:
        pass
    sys.exit(10)
`

// Treat subprocess diagnostics as untrusted data. Messages are selected here,
// never copied from Python exceptions; coordinates must fit this exact source.
func sourceFailure(source string, raw []byte) error {
	return runtimeSourceFailure("python", source, raw)
}

func runtimeSourceFailure(language, source string, raw []byte) error {
	label := runtimeLabel(language)
	fallback := &RunError{"failed", "runtime_error", label + "执行失败，请核对代码、入口与参数"}
	fallback.Code = map[string]string{"python": "python_error", "javascript": "javascript_error", "go": "go_error"}[language]
	var diagnostic c.SourceDiagnostic
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&diagnostic) != nil || decoder.Decode(new(any)) != io.EOF {
		return fallback
	}
	switch diagnostic.Code {
	case "syntax_error":
		diagnostic.Message = label + "语法错误，请检查此处的语句与符号"
	case "python_error":
		if language != "python" {
			return fallback
		}
		diagnostic.Message = "Python在此处执行失败，请核对参数、类型与计算逻辑"
	case "javascript_error":
		if language != "javascript" {
			return fallback
		}
		diagnostic.Message = "JavaScript在此处执行失败，请核对参数、类型与计算逻辑"
	case "go_compile_error":
		if language != "go" {
			return fallback
		}
		diagnostic.Message = "Go编译失败，请核对此处的类型、引用与声明"
	case "go_error":
		if language != "go" {
			return fallback
		}
		diagnostic.Message = "Go在此处执行失败，请核对参数、类型与计算逻辑"
	case "go_source_rejected":
		if language != "go" {
			return fallback
		}
		diagnostic.Message = "Go源码仅支持package main、标准库导入及普通入口，不能声明main、init或平台保留标识"
	default:
		return fallback
	}
	lines := strings.Split(source, "\n")
	if diagnostic.Line < 1 || diagnostic.Line > len(lines) {
		return fallback
	}
	columnFits := func(line, column int) bool {
		return line >= 1 && line <= len(lines) && column > 0 && column <= utf8.RuneCountInString(strings.TrimSuffix(lines[line-1], "\r"))+1
	}
	if !columnFits(diagnostic.Line, diagnostic.Column) {
		diagnostic.Column = 0
	}
	if diagnostic.EndLine < diagnostic.Line || !columnFits(diagnostic.EndLine, diagnostic.EndColumn) || diagnostic.EndLine == diagnostic.Line && diagnostic.EndColumn < diagnostic.Column {
		diagnostic.EndLine, diagnostic.EndColumn = 0, 0
	}
	return &DiagnosticRunError{RunError: &RunError{"failed", diagnostic.Code, diagnostic.Message}, Diagnostics: []c.SourceDiagnostic{diagnostic}}
}

type cappedBuffer struct {
	bytes.Buffer
	limit    int
	overflow bool
	cancel   context.CancelFunc
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	remaining := b.limit - b.Len()
	if len(p) > remaining {
		if remaining > 0 {
			_, _ = b.Buffer.Write(p[:remaining])
		}
		b.overflow = true
		b.cancel()
		return 0, errors.New("runner output budget exceeded")
	}
	return b.Buffer.Write(p)
}

func (d *DockerRunner) Run(ctx context.Context, runtime *c.ScriptRuntime, input map[string]json.RawMessage, imageID string) (map[string]json.RawMessage, error) {
	return d.run(ctx, runtime, input, imageID, "run")
}

func (d *DockerRunner) run(ctx context.Context, runtime *c.ScriptRuntime, input map[string]json.RawMessage, imageID, operation string) (map[string]json.RawMessage, error) {
	if c.ValidateScriptRuntime(runtime) != nil || d.imageFor(runtime.Language) == "" || !imageIDPattern.MatchString(imageID) {
		return nil, &RunError{"failed", "unsupported_runtime", "未配置可运行的源码实现"}
	}
	d.mu.Lock()
	pending := len(d.pending) > 0
	d.mu.Unlock()
	if pending {
		return nil, &RunError{"failed", "cleanup_unconfirmed", "此前计算容器仍待回收，暂时停止接收新计算"}
	}
	if ctx.Err() != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, &RunError{"timed_out", "execution_timeout", runtimeLabel(runtime.Language) + "计算超过执行时限"}
		}
		return nil, &RunError{"canceled", "execution_canceled", "请求已取消，计算未启动"}
	}
	payload, err := json.Marshal(map[string]any{"source": runtime.Source, "entrypoint": runtime.Entrypoint, "input": input, "operation": operation})
	if err != nil || len(payload) > (MaxPayloadBytes+(128<<10)) {
		return nil, &RunError{"failed", "input_budget", "运行输入超过预算"}
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	name := "ontology-" + runtime.Language + "-" + c.NewID()
	// No command string or shell is involved. All container security options
	// are fixed by the service, never by a callable definition or HTTP input.
	deadline := time.Now().Add(time.Duration(runtime.TimeoutSeconds)*time.Second + 90*time.Second).Unix()
	args := runtimeContainerArgs(runtime.Language, imageID, name, deadline)
	if d.CheckIsolation(ctx) != nil {
		return nil, &RunError{"failed", "isolation_unavailable", "安全执行环境尚未就绪，请联系平台管理员"}
	}
	if d.Isolation != "trusted" {
		args = append([]string{"create", "--runtime=runsc"}, args[1:]...)
	}
	// Complete creation separately before starting user code. Browser
	// cancellation cannot race a successful create response and its cleanup.
	createCtx, stopCreate := context.WithTimeout(context.Background(), 5*time.Second)
	create := d.exec(createCtx, args...)
	create.Stdout = io.Discard
	create.Stderr = io.Discard
	createErr := create.Run()
	stopCreate()
	if createErr != nil {
		if d.cleanup(name) != nil {
			return nil, &RunError{"failed", "cleanup_unconfirmed", "计算容器清理尚未确认，运行器已停止接收新计算"}
		}
		return nil, &RunError{"failed", "runtime_unavailable", "无法创建计算容器"}
	}
	cmd := d.exec(runCtx, "start", "--attach", "--interactive", name)
	cmd.Stdin = bytes.NewReader(payload)
	cmd.WaitDelay = 2 * time.Second
	stdout := &cappedBuffer{limit: MaxPayloadBytes, cancel: cancel}
	stderr := &cappedBuffer{limit: 16 << 10, cancel: cancel}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err = cmd.Run()
	// Killing the Docker client alone does not kill a container. Always remove
	// our unique container with an independent bounded cleanup context.
	if d.cleanup(name) != nil {
		return nil, &RunError{"failed", "cleanup_unconfirmed", "计算容器清理尚未确认，运行器已停止接收新计算"}
	}
	if stdout.overflow || stderr.overflow {
		return nil, &RunError{"failed", "output_budget", runtimeLabel(runtime.Language) + "输出超过运行预算"}
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, &RunError{"timed_out", "execution_timeout", runtimeLabel(runtime.Language) + "计算超过执行时限"}
	}
	if ctx.Err() != nil {
		return nil, &RunError{"canceled", "execution_canceled", "请求已取消，计算已停止"}
	}
	if err != nil {
		return nil, runtimeSourceFailure(runtime.Language, runtime.Source, stderr.Bytes())
	}
	var out map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	if decoder.Decode(&out) != nil || out == nil || decoder.Decode(new(any)) != io.EOF {
		return nil, &RunError{"failed", "invalid_output", "入口须返回单个JSON对象"}
	}
	return out, nil
}
