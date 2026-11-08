package execution

import (
	"context"
	"fmt"
	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestIsolationInfoProcess(t *testing.T) {
	if os.Getenv("ONTOLOGY_ISOLATION_TEST_HELPER") != "1" {
		return
	}
	fmt.Fprint(os.Stdout, os.Getenv("ONTOLOGY_ISOLATION_TEST_INFO"))
	os.Exit(0)
}

func TestPublicIsolationRequiresRootlessGVisorAndBudgets(t *testing.T) {
	valid := `{"security":["name=rootless"],"runtimes":{"runsc":{}},"cgroupVersion":"2","memory":true,"pids":true,"cpu":true}`
	for _, test := range []struct {
		name, info string
		allowed    bool
	}{
		{"safe", valid, true},
		{"rootful", strings.Replace(valid, "name=rootless", "name=seccomp", 1), false},
		{"no gvisor", strings.Replace(valid, "runsc", "runc", 1), false},
		{"no pid budget", strings.Replace(valid, `"pids":true`, `"pids":false`, 1), false},
		{"no memory budget", strings.Replace(valid, `"memory":true`, `"memory":false`, 1), false},
		{"no cpu budget", strings.Replace(valid, `"cpu":true`, `"cpu":false`, 1), false},
		{"old cgroups", strings.Replace(valid, `"2"`, `"1"`, 1), false},
		{"invalid", "daemon error", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &DockerRunner{command: func(ctx context.Context, _ string, args ...string) *exec.Cmd {
				if args[0] != "info" {
					t.Fatal("unsafe daemon reached container command")
				}
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestIsolationInfoProcess")
				cmd.Env = append(os.Environ(), "ONTOLOGY_ISOLATION_TEST_HELPER=1", "ONTOLOGY_ISOLATION_TEST_INFO="+test.info)
				return cmd
			}}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := runner.CheckIsolation(ctx); (err == nil) != test.allowed {
				t.Fatalf("isolation allowed=%v error=%v", test.allowed, err)
			}
			if !test.allowed {
				_, err := runner.Run(ctx, &c.ScriptRuntime{Language: "python", Source: "def main(inputs): return {}", Entrypoint: "main", TimeoutSeconds: 5}, nil, "sha256:"+strings.Repeat("a", 64))
				if err == nil {
					t.Fatal("direct run bypassed isolation")
				}
			}
		})
	}
}

func TestTrustedIsolationRequiresExplicitMode(t *testing.T) {
	if ValidateIsolationMode("automatic") == nil {
		t.Fatal("unknown mode accepted")
	}
	runner := &DockerRunner{Isolation: "trusted", command: func(context.Context, string, ...string) *exec.Cmd {
		t.Fatal("trusted mode unexpectedly probed daemon")
		return nil
	}}
	if runner.CheckIsolation(context.Background()) != nil {
		t.Fatal("controlled development mode unavailable")
	}
}

func TestPublicRunAlwaysSelectsGVisor(t *testing.T) {
	created := false
	runner := &DockerRunner{command: func(ctx context.Context, _ string, args ...string) *exec.Cmd {
		switch args[0] {
		case "info":
			return helperCommand(ctx, 0, `{"security":["name=rootless"],"runtimes":{"runsc":{}},"cgroupVersion":"2","memory":true,"pids":true,"cpu":true}`)
		case "create":
			created = true
			if !strings.Contains(strings.Join(args, " "), "--runtime=runsc") {
				t.Fatal("container inherited a weaker daemon default")
			}
			return helperCommand(ctx, 0, "container")
		case "start":
			return helperCommand(ctx, 0, `{}`)
		case "rm":
			return helperCommand(ctx, 0, "")
		default:
			t.Fatalf("unexpected command %v", args)
			return nil
		}
	}}
	_, err := runner.Run(context.Background(), &c.ScriptRuntime{Language: "python", Source: "def main(inputs): return {}", Entrypoint: "main", TimeoutSeconds: 5}, nil, "sha256:"+strings.Repeat("a", 64))
	if err != nil || !created {
		t.Fatalf("safe runner failed: %v", err)
	}
}
