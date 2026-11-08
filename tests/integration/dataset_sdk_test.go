package integration_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

// The optional Node process exercises the built SDK through the same trusted
// TLS/Cookie/CSRF/OPA/Metadata/Data/MySQL fixture as the direct Go requests.
// Session material travels only through child stdin and is never logged or
// written to disk. Stdout contains fixed stage acknowledgements, never results.
type dataSetSDKProcess struct {
	t      *testing.T
	stdin  io.WriteCloser
	cmd    *exec.Cmd
	acks   chan dataSetSDKResponse
	done   chan struct{}
	closed bool
}

type dataSetSDKResponse struct {
	Stage      string `json:"stage"`
	OK         bool   `json:"ok"`
	Checkpoint string `json:"checkpoint,omitempty"`
}

func dataSetSDKCredentials(browser *publicSecurityHTTP) map[string]string {
	parts := make([]string, 0, len(browser.cookies))
	for _, cookie := range browser.cookies {
		parts = append(parts, cookie.Name+"="+cookie.Value)
	}
	return map[string]string{"cookie": strings.Join(parts, "; "), "csrf": browser.csrf}
}

func startDataSetSDKHTTP(t *testing.T, admin *publicSecurityHTTP, client *http.Client, dataURL, schemaID, code, state, priority string) *dataSetSDKProcess {
	t.Helper()
	if os.Getenv("ONTOLOGY_TEST_DATASET_SDK") != "1" {
		t.Run("DataSetSDK", func(t *testing.T) {
			t.Skip("set ONTOLOGY_TEST_DATASET_SDK=1 and build packages/dataset to run the real Node SDK/public HTTP chain")
		})
		return nil
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("explicit DataSet SDK integration requires Node >=22.12")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal("locate DataSet SDK integration workspace")
	}
	for _, relative := range []string{"packages/dataset/dist/index.js", "packages/dataset/test/integration.mjs"} {
		if info, err := os.Stat(filepath.Join(root, relative)); err != nil || info.IsDir() {
			t.Fatal("explicit DataSet SDK integration requires npm run build in packages/dataset and its integration runner")
		}
	}
	response, err := client.Get(dataURL + "/healthz")
	if err != nil {
		t.Fatal("read already trusted public TLS fixture certificate")
	}
	_ = response.Body.Close()
	if response.StatusCode != 200 || response.TLS == nil || len(response.TLS.PeerCertificates) == 0 {
		t.Fatal("DataSet SDK fixture lacks a verified TLS certificate")
	}
	var ca bytes.Buffer
	for _, certificate := range response.TLS.PeerCertificates {
		if pem.Encode(&ca, &pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw}) != nil {
			t.Fatal("encode public TLS fixture certificate")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	command := exec.CommandContext(ctx, node, filepath.Join(root, "packages", "dataset", "test", "integration.mjs"))
	command.Dir = root
	// Node needs only its operating-system environment. In particular, do not
	// inherit the Go fixture's database DSNs or service credentials.
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(key) {
		case "SYSTEMROOT", "WINDIR", "PATH", "PATHEXT", "TEMP", "TMP", "COMSPEC":
			command.Env = append(command.Env, entry)
		}
	}
	input, err := command.StdinPipe()
	if err != nil {
		cancel()
		t.Fatal("open DataSet SDK child input")
	}
	output, err := command.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatal("open DataSet SDK child result pipe")
	}
	// Child diagnostics are deliberately discarded. All useful failures are
	// represented by a safe stage failure, with no response or credential text.
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		cancel()
		t.Fatal("start DataSet SDK integration child")
	}
	process := &dataSetSDKProcess{t: t, stdin: input, cmd: command, acks: make(chan dataSetSDKResponse, 8), done: make(chan struct{})}
	go func() {
		scanner := bufio.NewScanner(output)
		scanner.Buffer(make([]byte, 1024), 4096)
		for scanner.Scan() {
			var ack dataSetSDKResponse
			if json.Unmarshal(scanner.Bytes(), &ack) != nil {
				process.acks <- dataSetSDKResponse{Stage: "invalid-child-output"}
				return
			}
			process.acks <- ack
		}
	}()
	go func() {
		_ = command.Wait()
		close(process.done)
	}()
	t.Cleanup(func() {
		defer cancel()
		if !process.closed {
			_ = json.NewEncoder(process.stdin).Encode(map[string]string{"stage": "stop"})
			_ = process.stdin.Close()
		}
		select {
		case <-process.done:
		case <-time.After(3 * time.Second):
			_ = process.cmd.Process.Kill()
			<-process.done
		}
	})
	process.stage("initial", map[string]any{
		"dataURL": dataURL, "ca": ca.String(), "host": "security.test:30011", "origin": publicSecurityOrigin,
		"auth": dataSetSDKCredentials(admin), "schemaId": schemaID, "code": code, "state": state, "priority": priority,
	})
	return process
}

func (process *dataSetSDKProcess) stage(stage string, payload map[string]any) {
	if process == nil {
		return
	}
	process.t.Helper()
	if payload == nil {
		payload = map[string]any{}
	}
	payload["stage"] = stage
	if json.NewEncoder(process.stdin).Encode(payload) != nil {
		process.t.Fatalf("DataSet SDK child input failed at stage %s (details redacted)", stage)
	}
	select {
	case ack := <-process.acks:
		if ack.Stage != stage || !ack.OK {
			checkpoint := "unknown"
			for _, safe := range []string{"initial-plan", "initial-bind", "initial-entity", "entity-pages", "summary-bind", "summary-result", "group-pages", "descriptor-copy", "renamed-existing", "renamed-fresh", "reader-page", "reader-hidden", "revoked-page", "revoked-execute"} {
				if ack.Checkpoint == safe {
					checkpoint = safe
				}
			}
			process.t.Fatalf("DataSet SDK public integration failed at stage %s/%s (details redacted)", stage, checkpoint)
		}
	case <-process.done:
		process.closed = true
		process.t.Fatalf("DataSet SDK child exited at stage %s (details redacted)", stage)
	case <-time.After(25 * time.Second):
		process.t.Fatalf("DataSet SDK public integration timed out at stage %s", stage)
	}
}

// Existing assertions above this hook keep their original one-row filter.
// Only this isolated fixture then allows two rows, so the real SDK can hold a
// live nextPage closure across the original subsequent permission revocation.
func (process *dataSetSDKProcess) prepareReader(admin, reader *publicSecurityHTTP, metadataURL, dataURL, schemaID, ruleID, code string) {
	if process == nil {
		return
	}
	process.t.Helper()
	var policy c.PolicyRecord
	admin.request("GET", metadataURL+"/metadata/admin/policy", nil, nil, 200, &policy)
	found := false
	for i := range policy.Current.Rules {
		if policy.Current.Rules[i].ID == ruleID {
			policy.Current.Rules[i].RowFilter = &c.Expression{Op: "in", Field: code, Value: json.RawMessage(`["new","old"]`)}
			found = true
		}
	}
	if !found {
		process.t.Fatal("locate isolated SDK pagination permission rule")
	}
	headers := admin.request("PUT", metadataURL+"/metadata/admin/policy", policy.Current, map[string]string{"If-Match": `"` + policy.ETag + `"`}, 200, &policy)
	admin.request("POST", metadataURL+"/metadata/admin/policy/publish", nil, map[string]string{"If-Match": headers.Get("ETag")}, 200, nil)
	// Prove the same live reader session retained exactly its authorized column.
	var result c.QueryResult
	reader.request("POST", dataURL+"/data/query", c.QuerySpec{From: c.Source{SchemaID: schemaID}, Select: []string{code}, Page: c.Page{Limit: 10}}, nil, 200, &result)
	if len(result.Data) != 2 || len(result.Data[0].Values) != 1 {
		process.t.Fatal("SDK fixture reader did not retain two authorized rows and one column")
	}
	process.stage("reader", map[string]any{"auth": dataSetSDKCredentials(reader)})
}
