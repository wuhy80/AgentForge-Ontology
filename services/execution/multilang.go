package execution

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

var supportedRuntimes = []string{"python", "javascript", "go"}

func runtimeLabel(language string) string {
	return map[string]string{"python": "Python", "javascript": "JavaScript", "go": "Go"}[language]
}

type runtimeReadier interface {
	ReadyRuntime(context.Context, string) (string, error)
}
type sourceToolRunner interface {
	SourceTools(context.Context, *c.ScriptRuntime, string, string) (c.SourceToolResult, error)
}

func readyRuntime(ctx context.Context, runner Runner, language string) (string, error) {
	if runner == nil {
		return "", errors.New("runner unavailable")
	}
	if selected, ok := runner.(runtimeReadier); ok {
		return selected.ReadyRuntime(ctx, language)
	}
	if language != "python" {
		return "", errors.New("runtime unavailable")
	}
	return runner.Ready(ctx)
}

func availableRuntimes(ctx context.Context, runner Runner) []string {
	available := []string{}
	for _, language := range supportedRuntimes {
		if _, err := readyRuntime(ctx, runner, language); err == nil {
			available = append(available, language)
		}
	}
	return available
}

func runtimeContainerArgs(language, imageID, name string, deadline int64) []string {
	memory, pids, tmpfs := "128m", "64", "/tmp:rw,size=8m,noexec,nosuid,nodev"
	command := []string{"python", "-I", "-S", "-u", "-c", pythonWrapper}
	if language == "javascript" {
		command = []string{"node", "--disable-proto=throw", "--max-old-space-size=80", "--no-deprecation", "--eval", javascriptWrapper}
	}
	if language == "go" {
		memory, pids, tmpfs = "768m", "128", "/tmp:rw,size=384m,exec,nosuid,nodev"
		command = []string{"/opt/ontology/go-runner"}
	}
	args := []string{"create", "--interactive", "--rm", "--pull=never", "--name", name,
		"--label", "ontology.execution.runtime=" + language, "--label", "ontology.execution.deadline=" + strconv.FormatInt(deadline, 10),
		"--network", "none", "--read-only", "--user", "65534:65534", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"--pids-limit", pids, "--memory", memory, "--memory-swap", memory, "--cpus", "1", "--tmpfs", tmpfs, "--log-driver", "none", imageID}
	return append(args, command...)
}

func (d *DockerRunner) SourceTools(ctx context.Context, runtime *c.ScriptRuntime, imageID, operation string) (c.SourceToolResult, error) {
	result := c.SourceToolResult{Source: runtime.Source, Diagnostics: []c.SourceDiagnostic{}}
	if runtime.Language != "go" || (operation != "check" && operation != "format") {
		return result, errors.New("unsupported source tool")
	}
	out, err := d.run(ctx, runtime, nil, imageID, operation)
	if err != nil {
		return result, err
	}
	raw, err := json.Marshal(out)
	if err != nil || json.Unmarshal(raw, &result) != nil || !utf8.ValidString(result.Source) || len(result.Source) > c.MaxScriptSourceBytes || result.Diagnostics == nil || len(result.Diagnostics) > 50 {
		return c.SourceToolResult{}, errors.New("invalid source tool result")
	}
	for _, item := range result.Diagnostics {
		if item.Code != "syntax_error" || item.Message != "Go语法错误，请检查此处的语句与符号" {
			return c.SourceToolResult{}, errors.New("invalid source tool diagnostic")
		}
		lines := strings.Split(runtime.Source, "\n")
		if item.Line < 1 || item.Line > len(lines) || item.Column < 0 || item.Column > utf8.RuneCountInString(lines[item.Line-1])+1 || item.EndLine != 0 || item.EndColumn != 0 {
			return c.SourceToolResult{}, errors.New("invalid source tool coordinates")
		}
	}
	return result, nil
}

// The VM provides a minimal ECMAScript context; the security boundary remains
// the resource-limited, non-root, network-disabled container, never node:vm.
const javascriptWrapper = `const fs = require('node:fs');
const vm = require('node:vm');
let source = '';
try {
  const payload = JSON.parse(fs.readFileSync(0, 'utf8'));
  source = payload.source;
  const sandbox = Object.create(null);
  // Only primitives cross into the VM. Host objects/functions would expose
  // their host Function constructor despite the context's code-generation ban.
  sandbox.__ontologyInputJSON = JSON.stringify(payload.input);
  const context = vm.createContext(sandbox, {codeGeneration:{strings:false,wasm:false}});
  vm.runInContext('globalThis.__ontologyInput = JSON.parse(__ontologyInputJSON); delete globalThis.__ontologyInputJSON; globalThis.console = Object.freeze({log(){},info(){},warn(){},error(){},debug(){}});',context);
  const dataMode = payload.operation === 'execute_data';
  if (dataMode) vm.runInContext(String.raw` + "`" + javascriptDataSDK + "`" + `,context);
  const objectPrototype = vm.runInContext('Object.prototype', context);
  new vm.Script(source + '\n;__ontologyResult = ' + payload.entrypoint + (dataMode ? '(__ontologyInput,__ontologyWriteState.context);' : '(__ontologyInput);'), {filename:'<function>'}).runInContext(context);
  const result = sandbox.__ontologyResult;
  if (result === null || typeof result !== 'object' || Array.isArray(result) || ![null,objectPrototype].includes(Object.getPrototypeOf(result)) || typeof result.then === 'function') throw new Error('invalid result');
  const out = Object.create(null);
  for (const key of Reflect.ownKeys(result)) {
    if (typeof key !== 'string') throw new Error('invalid key');
    const property = Object.getOwnPropertyDescriptor(result, key);
    if (!property || !property.enumerable || !Object.hasOwn(property,'value')) throw new Error('invalid descriptor');
    const value = property.value;
    if (!(value === null || typeof value === 'string' || typeof value === 'boolean' || typeof value === 'number' && Number.isFinite(value))) throw new Error('invalid value');
    out[key] = value;
  }
  let encoded = JSON.stringify(out);
  if (dataMode) {
    sandbox.__ontologyOutputJSON = encoded;
    encoded = vm.runInContext('__ontologyWriteState.finish(__ontologyOutputJSON)',context);
    if (typeof encoded !== 'string') throw new Error('invalid plan');
  }
  if (Buffer.byteLength(encoded,'utf8') > 262144) throw new Error('output budget');
  fs.writeSync(1, encoded);
} catch (failure) {
  const diagnostic = {code:'javascript_error',line:0};
  const stack = typeof failure?.stack === 'string' ? failure.stack : '';
  const match = /<function>:(\d+)(?::(\d+))?/.exec(stack);
  if (match) diagnostic.line = Number(match[1]);
  if (failure?.name === 'SyntaxError') diagnostic.code = 'syntax_error';
  try { fs.writeSync(2, JSON.stringify(diagnostic)); } catch {}
  process.exitCode = 10;
}`
