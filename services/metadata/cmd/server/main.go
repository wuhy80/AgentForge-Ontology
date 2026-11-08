package main

import (
	"context"
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
	metadata "github.com/wuhy80/AgentForge-Ontology/services/metadata"
	"github.com/wuhy80/AgentForge-Ontology/services/metadata/identity"
)

func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
func run() error {
	migrate := flag.Bool("migrate", false, "显式应用 Metadata 平台迁移后退出")
	bootstrap := flag.Bool("bootstrap-admin", false, "显式初始化首个租户管理员后退出")
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
		migrations := append(append([]storage.Migration{}, metadata.Migrations...), identity.Migrations...)
		return storage.Migrate(ctx, e.DB(), "metadata", migrations)
	}
	service := os.Getenv("ONTOLOGY_SERVICE_TOKEN")
	if len(service) < 32 {
		return fmt.Errorf("服务凭据须至少32字节")
	}
	if os.Getenv("ONTOLOGY_OPA_URL") == "" || os.Getenv("ONTOLOGY_DATA_URL") == "" {
		return fmt.Errorf("须配置 ONTOLOGY_OPA_URL 与 ONTOLOGY_DATA_URL")
	}
	origin := os.Getenv("ONTOLOGY_PUBLIC_ORIGIN")
	if origin == "" {
		origin = "http://127.0.0.1:5173"
	}
	accounts := identity.NewStore(e.DB(), &platform.OPAAuthorizer{URL: os.Getenv("ONTOLOGY_OPA_URL"), BearerToken: os.Getenv("ONTOLOGY_OPA_TOKEN")}, origin)
	if err := accounts.ConfigureExternal(os.Getenv("ONTOLOGY_AUTH_ISSUER"), os.Getenv("ONTOLOGY_MCP_RESOURCE")); err != nil {
		return err
	}
	if *bootstrap {
		return accounts.Bootstrap(ctx, os.Getenv("ONTOLOGY_BOOTSTRAP_TENANT"), os.Getenv("ONTOLOGY_BOOTSTRAP_USERNAME"), os.Getenv("ONTOLOGY_BOOTSTRAP_PASSWORD"))
	}
	app := metadata.NewHandler(metadata.Config{Engine: e, Authorizer: accounts, DataURL: os.Getenv("ONTOLOGY_DATA_URL"), ServiceToken: service})
	mux := http.NewServeMux()
	mux.Handle("/metadata/auth/", accounts.NewHandler())
	mux.Handle("/metadata/admin/", accounts.NewHandler())
	mux.Handle("/oauth/", accounts.NewHandler())
	mux.Handle("/.well-known/oauth-authorization-server", accounts.NewHandler())
	mux.Handle("POST /internal/identity/session", internalSession(service, accounts))
	mux.Handle("POST /internal/metadata/query-directory", metadata.NewQueryDirectoryHandler(service, accounts, app))
	mux.Handle("POST /internal/authorization/decision", internalDecision(service, accounts))
	mux.Handle("/", app)
	addr := os.Getenv("ONTOLOGY_LISTEN")
	if addr == "" {
		addr = "127.0.0.1:18080"
	}
	log.Printf("Metadata listening on %s", addr)
	return platform.Serve(ctx, addr, platform.ProtectSessionRoutes(service, true, accounts, mux))
}
