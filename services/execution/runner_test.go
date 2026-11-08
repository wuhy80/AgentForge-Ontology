package execution

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func dockerTestRunner(t *testing.T) (*DockerRunner, string) {
	t.Helper()
	if os.Getenv("EXECUTION_TEST_DOCKER") != "1" {
		t.Skip("set EXECUTION_TEST_DOCKER=1 to run real isolated Python containers")
	}
	runner := &DockerRunner{Image: os.Getenv("ONTOLOGY_PYTHON_IMAGE"), Binary: os.Getenv("ONTOLOGY_DOCKER_BINARY")}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	imageID, err := runner.Ready(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return runner, imageID
}

func TestDockerRunnerPythonIsolationAndExactValues(t *testing.T) {
	runner, imageID := dockerTestRunner(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	source := `def main(inputs):
    import os, socket
    from decimal import Decimal
    root_read_only = False
    try:
        with open("/execution-write-probe", "w") as handle:
            handle.write("unexpected")
    except OSError:
        root_read_only = True
    network_blocked = False
    probe = socket.socket()
    probe.settimeout(0.5)
    try:
        probe.connect(("1.1.1.1", 53))
    except OSError:
        network_blocked = True
    finally:
        probe.close()
    print("ordinary function output is not returned as execution logs")
    return {"uid": str(os.getuid()), "rootReadOnly": root_read_only, "networkBlocked": network_blocked,
            "noServiceSecret": "ONTOLOGY_SERVICE_TOKEN" not in os.environ,
            "金额": str(Decimal(inputs["金额"]) + Decimal("0.10")), "数量": str(int(inputs["数量"]))}
`
	out, err := runner.Run(ctx, &c.ScriptRuntime{Language: "python", Source: source, Entrypoint: "main", TimeoutSeconds: 10}, map[string]json.RawMessage{"金额": json.RawMessage(`"9007199254740993.10"`), "数量": json.RawMessage(`"9223372036854775807"`)}, imageID)
	if err != nil {
		t.Fatal(err)
	}
	if string(out["uid"]) != `"65534"` || string(out["rootReadOnly"]) != "true" || string(out["networkBlocked"]) != "true" || string(out["noServiceSecret"]) != "true" || string(out["金额"]) != `"9007199254740993.20"` || string(out["数量"]) != `"9223372036854775807"` {
		t.Fatalf("isolation or exact values failed: %s", mustJSON(out))
	}
}

func TestDockerRunnerTimeoutCancellationAndSafeFailures(t *testing.T) {
	runner, imageID := dockerTestRunner(t)
	for _, test := range []struct {
		name, source string
		timeout      time.Duration
		wantStatus   string
	}{
		{"timeout", "def main(inputs):\n    while True: pass", time.Second, "timed_out"},
		{"exception", "def main(inputs):\n    raise RuntimeError('sensitive credential should never escape')", 10 * time.Second, "failed"},
		{"non-object", "def main(inputs):\n    return ['secret']", 10 * time.Second, "failed"},
		{"oversized", "def main(inputs):\n    return {'result': 'x' * 300000}", 10 * time.Second, "failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), test.timeout)
			defer cancel()
			_, err := runner.Run(ctx, &c.ScriptRuntime{Language: "python", Source: test.source, Entrypoint: "main", TimeoutSeconds: 1}, map[string]json.RawMessage{}, imageID)
			var runError *RunError
			if !errors.As(err, &runError) || runError.Status != test.wantStatus || strings.Contains(err.Error(), "sensitive credential") {
				t.Fatalf("unsafe or wrong failure: %v", err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := runner.Run(ctx, &c.ScriptRuntime{Language: "python", Source: "def main(inputs): return {}", Entrypoint: "main", TimeoutSeconds: 1}, nil, imageID)
	var runError *RunError
	if !errors.As(err, &runError) || runError.Status != "canceled" {
		t.Fatalf("canceled request %v", err)
	}
}

func TestDockerRunnerReportsSafeSyntaxAndRuntimeLocations(t *testing.T) {
	runner, imageID := dockerTestRunner(t)
	for _, test := range []struct {
		name, source, code string
		line               int
	}{
		{"syntax", "def main(inputs):\n    结果 = (1 + )\n    return {}", "syntax_error", 2},
		{"runtime", "def helper():\n    raise RuntimeError('secret credential value')\ndef main(inputs):\n    return helper()", "python_error", 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, err := runner.Run(ctx, &c.ScriptRuntime{Language: "python", Source: test.source, Entrypoint: "main", TimeoutSeconds: 5}, nil, imageID)
			var diagnostic *DiagnosticRunError
			if !errors.As(err, &diagnostic) || diagnostic.Code != test.code || len(diagnostic.Diagnostics) != 1 || diagnostic.Diagnostics[0].Line != test.line || strings.Contains(mustJSON(diagnostic), "secret") {
				t.Fatalf("missing/unsafe diagnostic: %v", err)
			}
		})
	}
}

func TestSourceFailureRejectsUntrustedTextAndCoordinates(t *testing.T) {
	source := "def main(inputs):\n    中文 = 1 +\n"
	for _, raw := range []string{`{"code":"unknown","line":2}`, `{"code":"python_error","line":999}`, `{"code":"python_error","line":-2}`, `{"code":"python_error","line":2,"stack":"private"}`, `{"code":"python_error","line":2} {}`, `raw exception secret`} {
		err := sourceFailure(source, []byte(raw))
		var diagnostic *DiagnosticRunError
		if errors.As(err, &diagnostic) || strings.Contains(err.Error(), "secret") {
			t.Fatalf("unsafe diagnostic %v", err)
		}
	}
	err := sourceFailure(source, []byte(`{"code":"syntax_error","line":2,"column":100,"endLine":500,"endColumn":1,"message":"secret"}`))
	var diagnostic *DiagnosticRunError
	if !errors.As(err, &diagnostic) || diagnostic.Diagnostics[0].Column != 0 || diagnostic.Diagnostics[0].EndLine != 0 || strings.Contains(err.Error(), "secret") {
		t.Fatalf("unbounded coordinates or text: %v", err)
	}
	err = sourceFailure(source, []byte(`{"code":"syntax_error","line":2,"column":12,"endLine":2,"endColumn":13}`))
	if !errors.As(err, &diagnostic) || diagnostic.Diagnostics[0].Column != 12 {
		t.Fatalf("Unicode source coordinate rejected: %v", err)
	}
}

func mustJSON(v any) string { raw, _ := json.Marshal(v); return string(raw) }

// The child process writes only explicitly supplied fixture text. This tests
// real exec error/stream handling without requiring Docker for failure paths.
func TestExecutionCommandHelperProcess(t *testing.T) {
	if os.Getenv("EXECUTION_COMMAND_HELPER") != "1" {
		return
	}
	value, _ := base64.StdEncoding.DecodeString(os.Getenv("EXECUTION_COMMAND_OUTPUT"))
	fmt.Print(string(value))
	code, _ := strconv.Atoi(os.Getenv("EXECUTION_COMMAND_EXIT"))
	os.Exit(code)
}
func helperCommand(ctx context.Context, code int, output string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestExecutionCommandHelperProcess$")
	cmd.Env = append(os.Environ(), "EXECUTION_COMMAND_HELPER=1", "EXECUTION_COMMAND_EXIT="+strconv.Itoa(code), "EXECUTION_COMMAND_OUTPUT="+base64.StdEncoding.EncodeToString([]byte(output)))
	return cmd
}

func TestRunnerStopsAdmissionUntilContainerCleanupConfirmed(t *testing.T) {
	cleanupFails := true
	name := ""
	creates := 0
	runner := &DockerRunner{}
	runner.command = func(ctx context.Context, _ string, args ...string) *exec.Cmd {
		switch args[0] {
		case "create":
			creates++
			for i, value := range args {
				if value == "--name" {
					name = args[i+1]
				}
			}
			return helperCommand(ctx, 0, "container-id")
		case "start":
			return helperCommand(ctx, 0, `{}`)
		case "rm":
			if cleanupFails {
				return helperCommand(ctx, 1, "")
			}
			return helperCommand(ctx, 0, name)
		case "ps":
			if strings.Contains(strings.Join(args, " "), "name=^") {
				return helperCommand(ctx, 0, name)
			}
			return helperCommand(ctx, 0, "")
		case "image":
			return helperCommand(ctx, 0, "sha256:"+strings.Repeat("a", 64)+" linux")
		default:
			t.Fatalf("unexpected Docker command %v", args)
			return nil
		}
	}
	ctx := context.Background()
	runtime := &c.ScriptRuntime{Language: "python", Source: "def main(inputs): return {}", Entrypoint: "main", TimeoutSeconds: 1}
	_, err := runner.Run(ctx, runtime, nil, "sha256:"+strings.Repeat("a", 64))
	var failure *RunError
	if !errors.As(err, &failure) || failure.Code != "cleanup_unconfirmed" {
		t.Fatalf("cleanup failure disappeared: %v", err)
	}
	if _, err := runner.Run(ctx, runtime, nil, "sha256:"+strings.Repeat("a", 64)); err == nil || creates != 1 {
		t.Fatal("admitted work with an uncollected container")
	}
	if _, err := runner.Ready(ctx); err == nil {
		t.Fatal("runner reported ready while cleanup failed")
	}
	cleanupFails = false
	if _, err := runner.Ready(ctx); err != nil {
		t.Fatalf("runner did not recover after confirmed cleanup: %v", err)
	}
	if len(runner.pending) != 0 {
		t.Fatal("pending cleanup was not cleared")
	}
}

func TestRunnerRecoversOnlyExpiredExecutionContainers(t *testing.T) {
	oldName := "ontology-python-" + c.NewID()
	liveName := "ontology-python-" + c.NewID()
	removed := []string{}
	runner := &DockerRunner{command: func(ctx context.Context, _ string, args ...string) *exec.Cmd {
		switch args[0] {
		case "ps":
			return helperCommand(ctx, 0, oldName+" "+strconv.FormatInt(time.Now().Add(-time.Minute).Unix(), 10)+"\n"+liveName+" "+strconv.FormatInt(time.Now().Add(time.Minute).Unix(), 10)+"\n")
		case "rm":
			removed = append(removed, args[len(args)-1])
			return helperCommand(ctx, 0, "")
		case "image":
			return helperCommand(ctx, 0, "sha256:"+strings.Repeat("a", 64)+" linux")
		}
		t.Fatalf("unexpected Docker command %v", args)
		return nil
	}}
	if _, err := runner.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != oldName {
		t.Fatalf("recovered containers %v", removed)
	}
}
