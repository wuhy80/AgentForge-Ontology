package main

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/storage"
)

func TestMySQLServiceMaintenanceLocksOnSameServer(t *testing.T) {
	dsn := os.Getenv("STORAGE_RELOCATION_TEST_DSN")
	if dsn == "" {
		t.Skip("STORAGE_RELOCATION_TEST_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	first, err := storage.Open(ctx, storage.Config{Driver: "mysql", DSN: dsn, MaxOpen: 5, MaxIdle: 2})
	if err != nil {
		t.Fatal("cannot open test database")
	}
	defer first.Close()
	second, err := storage.Open(ctx, storage.Config{Driver: "mysql", DSN: dsn, MaxOpen: 5, MaxIdle: 2})
	if err != nil {
		t.Fatal("cannot open second test connection")
	}
	defer second.Close()
	name := "relocation-lock-test:" + c.NewID()[:24]
	releaseMeta, err := acquireLock(ctx, first, name+":metadata")
	if err != nil {
		t.Fatal(err)
	}
	defer releaseMeta()
	releaseData, err := acquireLock(ctx, second, name+":data")
	if err != nil {
		t.Fatal("independent service lock blocked on same server", err)
	}
	defer releaseData()
	if releaseDuplicate, err := acquireLock(ctx, second, name+":metadata"); err == nil {
		releaseDuplicate()
		t.Fatal("duplicate Metadata maintenance lock was admitted")
	}
}

func TestManifestRejectsUnknownFieldsAndTrailingData(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	for _, raw := range []string{
		`{"format":1,"items":[],"password":"must-not-be-accepted"}`,
		`{"format":1,"items":[{}]} {"format":1}`,
		`{"format":2,"items":[{}]}`,
		`{"format":1,"items":[]}`,
	} {
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := readManifest(path); err == nil {
			t.Fatalf("accepted invalid manifest: %s", raw)
		}
	}
	value := manifest{Format: 1, Items: []c.StorageRelocation{{OperationID: "fixed-operation", Definition: c.Definition{ID: "fixed-model"}}}}
	raw, _ := json.Marshal(value)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	_, first, err := readManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	indented, _ := json.MarshalIndent(value, "", "  ")
	if err := os.WriteFile(path, indented, 0600); err != nil {
		t.Fatal(err)
	}
	_, second, err := readManifest(path)
	if err != nil || first != second {
		t.Fatal("whitespace changed manifest identity", err)
	}
}

func TestOfflineGuardRejectsRunningServiceAndNonLoopback(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := ensureOffline([]string{listener.Addr().String()}); err == nil {
		t.Fatal("running service was accepted")
	}
	if err := ensureOffline([]string{"192.0.2.1:18080"}); err == nil {
		t.Fatal("remote maintenance probe was accepted")
	}
}

func TestReportCanBeReplacedWithoutLosingManifestIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	if err := writeReport(path, report{ManifestDigest: "fixed", Phase: "data_prepared"}); err != nil {
		t.Fatal(err)
	}
	if err := writeReport(path, report{ManifestDigest: "fixed", Phase: "complete"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var value report
	if err := json.Unmarshal(raw, &value); err != nil || value.ManifestDigest != "fixed" || value.Phase != "complete" {
		t.Fatal("invalid final report", err)
	}
}
