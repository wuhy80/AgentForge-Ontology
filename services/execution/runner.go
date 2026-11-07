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

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

type Runner interface {
	Ready(context.Context) (string, error)
	Run(context.Context, *c.ScriptRuntime, map[string]json.RawMessage, string) (map[string]json.RawMessage, error)
}

type RunError struct{ Status, Code, Message string }

func (e *RunError) Error() string { return e.Code + ": " + e.Message }

// DockerRunner never invokes a host Python, mounts host paths, forwards host
// environment variables or pulls a user-selected image. The configured image
// is resolved to its actual local image ID before each admitted invocation.
type DockerRunner struct {
	Binary, Image string
	mu            sync.Mutex
	pending       map[string]bool
	command       func(context.Context, string, ...string) *exec.Cmd
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
func (d *DockerRunner) image() string {
	if d.Image != "" {
		return d.Image
	}
	return "python:3.12-alpine"
}

var imageIDPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

func (d *DockerRunner) Ready(ctx context.Context) (string, error) {
	if err := d.recoverContainers(ctx); err != nil {
		return "", err
	}
	cmd := d.exec(ctx, "image", "inspect", "--format", "{{.Id}} {{.Os}}", d.image())
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	if cmd.Run() != nil {
		return "", errors.New("configured Python image is unavailable")
	}
	parts := strings.Fields(out.String())
	if len(parts) != 2 || !imageIDPattern.MatchString(parts[0]) || parts[1] != "linux" {
		return "", errors.New("configured Python image must be a Linux image")
	}
	return parts[0], nil
}

// Only expired containers carrying our fixed marker are recovered across
// process restarts. Live containers belonging to other replicas are untouched.
func (d *DockerRunner) recoverContainers(ctx context.Context) error {
	cmd := d.exec(ctx, "ps", "--all", "--filter", "label=ontology.execution.runtime=python", "--format", `{{.Names}} {{.Label "ontology.execution.deadline"}}`)
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
		if len(parts) != 2 || !strings.HasPrefix(parts[0], "ontology-python-") {
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
except BaseException:
    sys.exit(10)
`

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
	if runtime == nil || runtime.Language != "python" || !imageIDPattern.MatchString(imageID) {
		return nil, &RunError{"failed", "unsupported_runtime", "未配置可运行的Python实现"}
	}
	d.mu.Lock()
	pending := len(d.pending) > 0
	d.mu.Unlock()
	if pending {
		return nil, &RunError{"failed", "cleanup_unconfirmed", "此前计算容器仍待回收，暂时停止接收新计算"}
	}
	if ctx.Err() != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, &RunError{"timed_out", "execution_timeout", "Python计算超过执行时限"}
		}
		return nil, &RunError{"canceled", "execution_canceled", "请求已取消，计算未启动"}
	}
	payload, err := json.Marshal(map[string]any{"source": runtime.Source, "entrypoint": runtime.Entrypoint, "input": input})
	if err != nil || len(payload) > (MaxPayloadBytes+(128<<10)) {
		return nil, &RunError{"failed", "input_budget", "运行输入超过预算"}
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	name := "ontology-python-" + c.NewID()
	// No command string or shell is involved. All container security options
	// are fixed by the service, never by a callable definition or HTTP input.
	deadline := time.Now().Add(time.Duration(runtime.TimeoutSeconds)*time.Second + 90*time.Second).Unix()
	args := []string{"create", "--interactive", "--rm", "--pull=never", "--name", name, "--label", "ontology.execution.runtime=python", "--label", "ontology.execution.deadline=" + strconv.FormatInt(deadline, 10), "--network", "none", "--read-only", "--user", "65534:65534", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--pids-limit", "64", "--memory", "128m", "--memory-swap", "128m", "--cpus", "1", "--tmpfs", "/tmp:rw,size=8m,noexec,nosuid,nodev", "--log-driver", "none", imageID, "python", "-I", "-S", "-u", "-c", pythonWrapper}
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
		return nil, &RunError{"failed", "runtime_unavailable", "无法创建Python计算容器"}
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
		return nil, &RunError{"failed", "output_budget", "Python输出超过运行预算"}
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, &RunError{"timed_out", "execution_timeout", "Python计算超过执行时限"}
	}
	if ctx.Err() != nil {
		return nil, &RunError{"canceled", "execution_canceled", "请求已取消，计算已停止"}
	}
	if err != nil {
		return nil, &RunError{"failed", "python_error", "Python执行失败，请核对代码、入口与参数"}
	}
	var out map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	if decoder.Decode(&out) != nil || out == nil || decoder.Decode(new(any)) != io.EOF {
		return nil, &RunError{"failed", "invalid_output", "Python须返回单个JSON对象"}
	}
	return out, nil
}
