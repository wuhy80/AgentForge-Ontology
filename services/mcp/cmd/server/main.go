package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
	gateway "github.com/wuhy80/AgentForge-Ontology/services/mcp"
)

func main() {
	if err := run(); err != nil {
		slog.Error("MCP server stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	metadata, data, execution := os.Getenv("ONTOLOGY_METADATA_URL"), os.Getenv("ONTOLOGY_DATA_URL"), os.Getenv("ONTOLOGY_EXECUTION_URL")
	if execution == "" {
		execution = "http://127.0.0.1:18082"
	}
	service := os.Getenv("ONTOLOGY_SERVICE_TOKEN")
	if len(service) < 32 {
		return errors.New("ONTOLOGY_SERVICE_TOKEN must contain at least 32 bytes")
	}
	if metadata == "" || data == "" {
		return errors.New("ONTOLOGY_METADATA_URL and ONTOLOGY_DATA_URL are required")
	}
	resource, issuer := os.Getenv("ONTOLOGY_MCP_RESOURCE"), os.Getenv("ONTOLOGY_AUTH_ISSUER")
	if resource == "" {
		resource = "http://127.0.0.1:18084/mcp"
	}
	if issuer == "" {
		issuer = "http://127.0.0.1:18084"
	}
	checker := &platform.RemoteSessionChecker{URL: metadata, ServiceToken: service}
	handler, err := gateway.NewHandler(gateway.Config{MetadataURL: metadata, DataURL: data, ExecutionURL: execution, ResourceURL: resource, AuthIssuer: issuer, AllowedOrigins: splitOrigins(os.Getenv("ONTOLOGY_MCP_ALLOWED_ORIGINS")), SessionChecker: checker})
	if err != nil {
		return err
	}
	addr := os.Getenv("ONTOLOGY_MCP_LISTEN")
	if addr == "" {
		addr = "127.0.0.1:18084"
	}
	server := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 45 * time.Second, WriteTimeout: 50 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	slog.Info("Ontology MCP listening", "address", addr, "resource", resource, "issuer", issuer)
	go func() {
		<-ctx.Done()
		closeCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = server.Shutdown(closeCtx)
	}()
	err = server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
func splitOrigins(raw string) []string {
	var out []string
	for _, item := range strings.Split(raw, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}
