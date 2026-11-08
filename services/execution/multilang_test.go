package execution

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func dockerLanguage(t *testing.T, language string) (*DockerRunner, string) {
	t.Helper()
	if os.Getenv("EXECUTION_TEST_DOCKER") != "1" {
		t.Skip("set EXECUTION_TEST_DOCKER=1 to run real isolated language containers")
	}
	runner := &DockerRunner{Image: os.Getenv("ONTOLOGY_PYTHON_IMAGE"), JavaScriptImage: os.Getenv("ONTOLOGY_JAVASCRIPT_IMAGE"), GoImage: os.Getenv("ONTOLOGY_GO_IMAGE"), Binary: os.Getenv("ONTOLOGY_DOCKER_BINARY")}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	imageID, err := runner.ReadyRuntime(ctx, language)
	if err != nil {
		t.Fatal(err)
	}
	return runner, imageID
}

func TestDockerJavaScriptExactValuesAndScalarOutput(t *testing.T) {
	runner, image := dockerLanguage(t, "javascript")
	input := map[string]json.RawMessage{"金额": json.RawMessage(`"9007199254740993.10"`), "数量": json.RawMessage(`"9223372036854775807"`), "空": json.RawMessage(`null`)}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	source := `console.log('private initialization');
function main(inputs) {
  console.error('private output');
  return {金额:inputs.金额, 数量:String(BigInt(inputs.数量)), 空:inputs.空, 环境:typeof process, 模块:typeof require};
}`
	out, err := runner.Run(ctx, &c.ScriptRuntime{Language: "javascript", Entrypoint: "main", Source: source, TimeoutSeconds: 10}, input, image)
	if err != nil {
		t.Fatal(err)
	}
	if string(out["金额"]) != `"9007199254740993.10"` || string(out["数量"]) != `"9223372036854775807"` || string(out["空"]) != "null" || string(out["环境"]) != `"undefined"` || string(out["模块"]) != `"undefined"` {
		t.Fatalf("wrong exact values or globals: %s", mustJSON(out))
	}
	// JSON input dictionaries are valid ordinary output dictionaries as well.
	if _, err := runner.Run(ctx, &c.ScriptRuntime{Language: "javascript", Entrypoint: "main", Source: "function main(inputs) { return inputs; }", TimeoutSeconds: 5}, input, image); err != nil {
		t.Fatal(err)
	}
}

func TestDockerJavaScriptRejectsLossyOrNonScalarOutputs(t *testing.T) {
	runner, image := dockerLanguage(t, "javascript")
	for _, value := range []string{"{a:NaN}", "{a:Infinity}", "{a:undefined}", "{a:1n}", "{a(){}}", "{a:new Date()}", "{a:[]}", "{a:Promise.resolve(1)}", "Promise.resolve({})", "new Date()", "[]", "{[Symbol('private')]:1}", "{get a(){return 1}}", "Object.defineProperty({},'a',{value:1})"} {
		t.Run(value, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, err := runner.Run(ctx, &c.ScriptRuntime{Language: "javascript", Entrypoint: "main", Source: "function main(inputs) { return " + value + "; }", TimeoutSeconds: 5}, nil, image)
			var failure *RunError
			if !errors.As(err, &failure) || failure.Status != "failed" {
				t.Fatalf("invalid output silently serialized: %v", err)
			}
		})
	}
}

func TestDockerJavaScriptSafeLocationsAndTimeout(t *testing.T) {
	runner, image := dockerLanguage(t, "javascript")
	for _, test := range []struct {
		source, code string
		line         int
	}{
		{"function main(inputs) {\n const 中文 = ;\n}", "syntax_error", 2},
		{"function helper(){\n throw new Error('private credential');\n}\nfunction main(inputs){return helper()}", "javascript_error", 2},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, err := runner.Run(ctx, &c.ScriptRuntime{Language: "javascript", Entrypoint: "main", Source: test.source, TimeoutSeconds: 5}, nil, image)
		cancel()
		var failure *DiagnosticRunError
		if !errors.As(err, &failure) || failure.Code != test.code || len(failure.Diagnostics) != 1 || failure.Diagnostics[0].Line != test.line || strings.Contains(mustJSON(failure), "private") {
			t.Fatalf("unsafe/missing JS diagnostic: %v", err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := runner.Run(ctx, &c.ScriptRuntime{Language: "javascript", Entrypoint: "main", Source: "function main(inputs){while(true){}}", TimeoutSeconds: 1}, nil, image)
	var failure *RunError
	if !errors.As(err, &failure) || failure.Status != "timed_out" || !strings.Contains(failure.Message, "JavaScript") {
		t.Fatalf("JS timeout: %v", err)
	}
}

func TestDockerJavaScriptDoesNotExposeHostConstructors(t *testing.T) {
	runner, image := dockerLanguage(t, "javascript")
	for _, expression := range []string{"inputs.constructor.constructor('return process')()", "console.log.constructor('return process')()", "this.constructor.constructor('return process')()"} {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		out, err := runner.Run(ctx, &c.ScriptRuntime{Language: "javascript", Entrypoint: "main", Source: "function main(inputs) { return {escaped:typeof (" + expression + ")}; }", TimeoutSeconds: 5}, map[string]json.RawMessage{}, image)
		cancel()
		var failure *RunError
		if !errors.As(err, &failure) || failure.Status != "failed" || out != nil {
			t.Fatalf("host constructor exposed: %s %s %v", expression, mustJSON(out), err)
		}
	}
}

func TestDockerGoIsolationExactValuesAndLogs(t *testing.T) {
	runner, image := dockerLanguage(t, "go")
	source := `package main
import ("fmt";"os";"net";"math/big")
var printed = func() bool { fmt.Println("private package initializer"); return true }()
func Main(inputs map[string]any) (map[string]any,error) {
  fmt.Println("private function output")
  amount,_:=new(big.Rat).SetString(inputs["金额"].(string)); amount.Add(amount,big.NewRat(1,10))
  _,writeErr:=os.Create("/execution-write-probe")
  connection,networkErr:=net.DialTimeout("tcp","1.1.1.1:53",100000000); if connection!=nil {connection.Close()}
  return map[string]any{"金额":amount.FloatString(2),"数量":inputs["数量"],"空":inputs["空"],"uid":fmt.Sprint(os.Getuid()),"只读":writeErr!=nil,"禁网":networkErr!=nil,"无凭据":os.Getenv("ONTOLOGY_SERVICE_TOKEN")==""},nil
}`
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := runner.Run(ctx, &c.ScriptRuntime{Language: "go", Entrypoint: "Main", Source: source, TimeoutSeconds: 30}, map[string]json.RawMessage{"金额": json.RawMessage(`"9007199254740993.10"`), "数量": json.RawMessage(`"9223372036854775807"`), "空": json.RawMessage(`null`)}, image)
	if err != nil {
		t.Fatal(err)
	}
	if string(out["金额"]) != `"9007199254740993.20"` || string(out["数量"]) != `"9223372036854775807"` || string(out["空"]) != "null" || string(out["uid"]) != `"65534"` || string(out["只读"]) != "true" || string(out["禁网"]) != "true" || string(out["无凭据"]) != "true" {
		t.Fatalf("Go isolation/exact values: %s", mustJSON(out))
	}
}

func TestDockerGoSafeFailuresAndSourceRestrictions(t *testing.T) {
	runner, image := dockerLanguage(t, "go")
	for _, test := range []struct {
		name, source, code string
		line               int
	}{
		{"syntax", "package main\nfunc Main(inputs map[string]any)(map[string]any,error){\n中文 := ;return nil,nil\n}", "syntax_error", 3},
		{"compile", "package main\nfunc Main(inputs map[string]any)(map[string]any,error){\nreturn missingPrivateVariable,nil\n}", "go_compile_error", 3},
		{"panic", "package main\nfunc helper(){\npanic(\"private credential\")\n}\nfunc Main(inputs map[string]any)(map[string]any,error){helper();return nil,nil}", "go_error", 3},
		{"error", "package main\nimport \"errors\"\nfunc Main(inputs map[string]any)(map[string]any,error){return nil,errors.New(\"private credential\")}", "go_error", 3},
		{"external import", "package main\nimport _ \"example.com/private\"\nfunc Main(inputs map[string]any)(map[string]any,error){return nil,nil}", "go_source_rejected", 2},
		{"main", "package main\nfunc main(){}\nfunc Main(inputs map[string]any)(map[string]any,error){return nil,nil}", "go_source_rejected", 2},
		{"init", "package main\nfunc init(){}\nfunc Main(inputs map[string]any)(map[string]any,error){return nil,nil}", "go_source_rejected", 2},
		{"directive", "package main\n//go:noinline\nfunc Main(inputs map[string]any)(map[string]any,error){return nil,nil}", "go_source_rejected", 2},
		{"reserved identifier", "package main\nvar __ontologyOut = 1\nfunc Main(inputs map[string]any)(map[string]any,error){return nil,nil}", "go_source_rejected", 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_, err := runner.Run(ctx, &c.ScriptRuntime{Language: "go", Entrypoint: "Main", Source: test.source, TimeoutSeconds: 30}, nil, image)
			var failure *DiagnosticRunError
			if !errors.As(err, &failure) || failure.Code != test.code || len(failure.Diagnostics) != 1 || failure.Diagnostics[0].Line != test.line || strings.Contains(mustJSON(failure), "private") {
				t.Fatalf("unsafe/missing Go diagnostic: %v", err)
			}
		})
	}
}

func TestDockerGoSourceToolsDoNotExecuteOrTypecheck(t *testing.T) {
	runner, image := dockerLanguage(t, "go")
	source := "package main\n// 中文注释\nvar run = func() bool { panic(\"private\"); return false }()\nfunc Main(inputs map[string]any)(map[string]any,error){return undefinedTypeName,nil}\n"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	runtime := &c.ScriptRuntime{Language: "go", Entrypoint: "Main", Source: source, TimeoutSeconds: 30}
	result, err := runner.SourceTools(ctx, runtime, image, "format")
	if err != nil || len(result.Diagnostics) != 0 || !strings.Contains(result.Source, "// 中文注释") || !strings.Contains(result.Source, "func Main(inputs map[string]any) (map[string]any, error)") {
		t.Fatalf("format ran/typechecked or lost source: %+v %v", result, err)
	}
	runtime.Source = "package main\n//line private.go:5000\nfunc Main(inputs map[string]any) (map[string]any,error) { 中文 := ;return nil,nil}\n"
	result, err = runner.SourceTools(ctx, runtime, image, "check")
	if err != nil || len(result.Diagnostics) == 0 || result.Diagnostics[0].Line != 3 || result.Diagnostics[0].Column < 1 || strings.Contains(mustJSON(result.Diagnostics), "private") {
		t.Fatalf("unsafe parser coordinates: %+v %v", result, err)
	}
}
