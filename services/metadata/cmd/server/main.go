package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
	"github.com/wuhy80/AgentForge-Ontology/libs/storage"
	metadata "github.com/wuhy80/AgentForge-Ontology/services/metadata"
)

func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
func run() error {
	migrate := flag.Bool("migrate", false, "显式应用 Metadata 平台迁移后退出")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	start, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	e, err := storage.Open(start, storage.Config{Driver: "mysql", DSN: os.Getenv("ONTOLOGY_MYSQL_DSN"), MaxOpen: 10, MaxIdle: 5})
	if err != nil {
		return fmt.Errorf("Metadata 存储连接失败: %w", err)
	}
	defer e.Close()
	if *migrate {
		return storage.Migrate(ctx, e.DB(), "metadata", metadata.Migrations)
	}
	secret, service := os.Getenv("ONTOLOGY_AUTH_SECRET"), os.Getenv("ONTOLOGY_SERVICE_TOKEN")
	if len(secret) < 32 || len(service) < 32 || secret == service {
		return fmt.Errorf("认证密钥与服务凭据须分别配置，至少32字节且不能相同")
	}
	if os.Getenv("ONTOLOGY_OPA_URL") == "" || os.Getenv("ONTOLOGY_DATA_URL") == "" {
		return fmt.Errorf("须配置 ONTOLOGY_OPA_URL 与 ONTOLOGY_DATA_URL")
	}
	app := metadata.NewHandler(metadata.Config{Engine: e, Authorizer: &platform.OPAAuthorizer{URL: os.Getenv("ONTOLOGY_OPA_URL"), BearerToken: os.Getenv("ONTOLOGY_OPA_TOKEN")}, DataURL: os.Getenv("ONTOLOGY_DATA_URL"), ServiceToken: service})
	addr := os.Getenv("ONTOLOGY_LISTEN")
	if addr == "" {
		addr = "127.0.0.1:18080"
	}
	log.Printf("Metadata listening on %s", addr)
	return platform.Serve(ctx, addr, platform.ProtectRoutes(secret, service, true, app))
}
