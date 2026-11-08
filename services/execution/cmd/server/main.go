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
	"strconv"
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
	checkIsolation := flag.Bool("check-isolation", false, "只检查执行隔离环境后退出")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *checkIsolation {
		return (&execution.DockerRunner{Binary: os.Getenv("ONTOLOGY_DOCKER_BINARY"), Isolation: os.Getenv("ONTOLOGY_EXECUTION_ISOLATION")}).CheckIsolation(ctx)
	}
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
	isolation := os.Getenv("ONTOLOGY_EXECUTION_ISOLATION")
	if err := execution.ValidateIsolationMode(isolation); err != nil {
		return err
	}
	maxConcurrent := 4
	if raw := os.Getenv("ONTOLOGY_EXECUTION_MAX_CONCURRENT"); raw != "" {
		maxConcurrent, err = strconv.Atoi(raw)
		if err != nil || maxConcurrent < 1 || maxConcurrent > 4 {
			return fmt.Errorf("ONTOLOGY_EXECUTION_MAX_CONCURRENT须为1到4的整数")
		}
	}
	app := execution.NewHandler(execution.Config{Store: &execution.SQLStore{DB: engine.DB()}, Authorizer: &platform.RemoteAuthorizer{URL: metadataURL, ServiceToken: service}, SessionChecker: checker, Metadata: &execution.RemoteMetadata{URL: metadataURL, ServiceToken: service}, Runner: &execution.DockerRunner{Isolation: isolation, Binary: os.Getenv("ONTOLOGY_DOCKER_BINARY"), Image: os.Getenv("ONTOLOGY_PYTHON_IMAGE"), JavaScriptImage: os.Getenv("ONTOLOGY_JAVASCRIPT_IMAGE"), GoImage: os.Getenv("ONTOLOGY_GO_IMAGE")}, MaxConcurrent: maxConcurrent, DataTransactions: &execution.RemoteDataTransactions{URL: os.Getenv("ONTOLOGY_DATA_URL"), ServiceToken: service}})
	addr := os.Getenv("ONTOLOGY_LISTEN")
	if addr == "" {
		addr = "127.0.0.1:18082"
	}
	// A 30-second computation needs room for current authorization, image
	// inspection, durable outcome recording and response checks as well.
	protected, err := platform.TrustTLSProxy(platform.ProtectSessionRoutes(service, false, checker, app), os.Getenv("ONTOLOGY_TRUSTED_PROXY_CIDRS"))
	if err != nil {
		return err
	}
	server := &http.Server{Addr: addr, Handler: protected, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 90 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
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
