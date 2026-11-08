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
	service, cursor := os.Getenv("ONTOLOGY_SERVICE_TOKEN"), os.Getenv("ONTOLOGY_CURSOR_SECRET")
	if len(service) < 32 || len(cursor) < 32 || service == cursor {
		return fmt.Errorf("服务和游标密钥须分别配置，至少32字节且不能相同")
	}
	if os.Getenv("ONTOLOGY_METADATA_URL") == "" {
		return fmt.Errorf("须配置 ONTOLOGY_METADATA_URL")
	}
	metadataURL := os.Getenv("ONTOLOGY_METADATA_URL")
	sourceKey := os.Getenv("ONTOLOGY_DATA_SOURCE_KEY")
	if sourceKey != "" && (len(sourceKey) < 32 || sourceKey == service || sourceKey == cursor) {
		return fmt.Errorf("数据源加密密钥至少32字节，且必须独立于服务及游标密钥")
	}
	checker := &platform.RemoteSessionChecker{URL: metadataURL, ServiceToken: service}
	sourceNetwork, err := data.ParseSourceNetworkPolicy(os.Getenv("ONTOLOGY_SOURCE_NETWORK_RULES"))
	if err != nil {
		return err
	}
	app := data.NewHandler(data.Config{Engine: e, Authorizer: &platform.RemoteAuthorizer{URL: metadataURL, ServiceToken: service}, SessionChecker: checker, MetadataURL: metadataURL, ServiceToken: service, CursorSecret: cursor, DataSourceKey: sourceKey, SourceNetworkPolicy: sourceNetwork})
	addr := os.Getenv("ONTOLOGY_LISTEN")
	if addr == "" {
		addr = "127.0.0.1:18081"
	}
	log.Printf("Data listening on %s", addr)
	protected, err := platform.TrustTLSProxy(platform.ProtectSessionRoutes(service, false, checker, app), os.Getenv("ONTOLOGY_TRUSTED_PROXY_CIDRS"))
	if err != nil {
		return err
	}
	return platform.Serve(ctx, addr, protected)
}
