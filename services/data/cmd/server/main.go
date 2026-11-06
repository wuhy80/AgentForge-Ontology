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
	data "github.com/wuhy80/AgentForge-Ontology/services/data"
)

func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
func run() error {
	migrate := flag.Bool("migrate", false, "显式应用 Data 平台迁移后退出")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	start, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	e, err := storage.Open(start, storage.Config{Driver: "mysql", DSN: os.Getenv("ONTOLOGY_MYSQL_DSN"), MaxOpen: 10, MaxIdle: 5})
	if err != nil {
		return fmt.Errorf("Data 存储连接失败: %w", err)
	}
	defer e.Close()
	if *migrate {
		return storage.Migrate(ctx, e.DB(), "data", data.Migrations)
	}
	secret, service, cursor := os.Getenv("ONTOLOGY_AUTH_SECRET"), os.Getenv("ONTOLOGY_SERVICE_TOKEN"), os.Getenv("ONTOLOGY_CURSOR_SECRET")
	if len(secret) < 32 || len(service) < 32 || len(cursor) < 32 || secret == service || secret == cursor || service == cursor {
		return fmt.Errorf("认证、服务和游标密钥须分别配置，至少32字节且不能相同")
	}
	if os.Getenv("ONTOLOGY_OPA_URL") == "" || os.Getenv("ONTOLOGY_METADATA_URL") == "" {
		return fmt.Errorf("须配置 ONTOLOGY_OPA_URL 与 ONTOLOGY_METADATA_URL")
	}
	app := data.NewHandler(data.Config{Engine: e, Authorizer: &platform.OPAAuthorizer{URL: os.Getenv("ONTOLOGY_OPA_URL"), BearerToken: os.Getenv("ONTOLOGY_OPA_TOKEN")}, MetadataURL: os.Getenv("ONTOLOGY_METADATA_URL"), ServiceToken: service, CursorSecret: cursor})
	addr := os.Getenv("ONTOLOGY_LISTEN")
	if addr == "" {
		addr = "127.0.0.1:18081"
	}
	log.Printf("Data listening on %s", addr)
	return platform.Serve(ctx, addr, platform.ProtectRoutes(secret, service, false, app))
}
