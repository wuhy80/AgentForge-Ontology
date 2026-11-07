package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
	"github.com/wuhy80/AgentForge-Ontology/libs/storage"
	"github.com/wuhy80/AgentForge-Ontology/services/execution"
)

func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
func run() error {
	migrate := flag.Bool("migrate", false, "显式应用Execution平台迁移后退出")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	start, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	engine, err := storage.Open(start, storage.Config{Driver: "mysql", DSN: os.Getenv("ONTOLOGY_MYSQL_DSN"), MaxOpen: 10, MaxIdle: 5})
	if err != nil {
		return fmt.Errorf("Execution存储连接失败: %w", err)
	}
	defer engine.Close()
	if *migrate {
		return storage.Migrate(ctx, engine.DB(), "execution", execution.Migrations)
	}
	service, metadataURL := os.Getenv("ONTOLOGY_SERVICE_TOKEN"), os.Getenv("ONTOLOGY_METADATA_URL")
	if len(service) < 32 || metadataURL == "" {
		return fmt.Errorf("须配置Metadata地址及至少32字节的内部服务凭据")
	}
	checker := &platform.RemoteSessionChecker{URL: metadataURL, ServiceToken: service}
	app := execution.NewHandler(execution.Config{Store: &execution.SQLStore{DB: engine.DB()}, Authorizer: &platform.RemoteAuthorizer{URL: metadataURL, ServiceToken: service}, SessionChecker: checker, Metadata: &execution.RemoteMetadata{URL: metadataURL, ServiceToken: service}, Runner: &execution.DockerRunner{Binary: os.Getenv("ONTOLOGY_DOCKER_BINARY"), Image: os.Getenv("ONTOLOGY_PYTHON_IMAGE")}})
	addr := os.Getenv("ONTOLOGY_LISTEN")
	if addr == "" {
		addr = "127.0.0.1:18082"
	}
	// A 30-second computation needs room for current authorization, image
	// inspection, durable outcome recording and response checks as well.
	server := &http.Server{Addr: addr, Handler: platform.ProtectSessionRoutes(service, false, checker, app), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 90 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	log.Printf("Execution listening on %s", addr)
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			_ = server.Close()
			return err
		}
		return nil
	}
}
