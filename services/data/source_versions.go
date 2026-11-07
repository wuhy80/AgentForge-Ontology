package data

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/storage"
)

const sourceReferenceCountSQL = "SELECT COUNT(*) FROM af_data_gates WHERE tenant_id=? AND (JSON_UNQUOTE(JSON_EXTRACT(definition,'$.schema.storageBinding.dataSourceId'))=? OR JSON_SEARCH(definition,'one',?,NULL,'$.schema.properties[*].mapping.dataSourceId') IS NOT NULL OR JSON_UNQUOTE(JSON_EXTRACT(definition,'$.link.storageBinding.dataSourceId'))=?)"

func sourceVersionError(err error) error {
	var versionErr *storage.VersionError
	if errors.As(err, &versionErr) {
		return problem(422, versionErr.Code, versionErr.Message)
	}
	var apiErr *apiError
	if errors.As(err, &apiErr) {
		return err
	}
	return problem(422, "source_connection_failed", "无法连接数据源，请核对地址、凭据、TLS和数据库权限")
}

func (h *handler) sourceVersions(w http.ResponseWriter, r *http.Request) error {
	if _, err := h.sourceCatalogPrincipal(r); err != nil {
		return err
	}
	writeJSON(w, 200, map[string]any{"data": storage.DatabaseVersionOptions(), "hasMore": false})
	return nil
}

func sourceConnector(s c.DataSource, password string) (driver.Connector, error) {
	if s.Driver == "mysql" {
		cfg := mysql.NewConfig()
		cfg.User, cfg.Passwd, cfg.Net, cfg.Addr, cfg.DBName = s.Username, password, "tcp", net.JoinHostPort(s.Host, strconv.Itoa(s.Port)), s.Database
		cfg.ParseTime, cfg.Loc, cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout = true, time.UTC, 5*time.Second, 10*time.Second, 10*time.Second
		cfg.Params = map[string]string{"charset": "utf8mb4"}
		switch s.TLSMode {
		case "require":
			cfg.TLSConfig = "skip-verify"
		case "verify-full":
			cfg.TLSConfig = "true"
		}
		return mysql.NewConnector(cfg)
	}
	if s.Driver == "postgresql" {
		u := url.URL{Scheme: "postgres", User: url.UserPassword(s.Username, password), Host: net.JoinHostPort(s.Host, strconv.Itoa(s.Port)), Path: "/" + s.Database}
		u.RawQuery = (url.Values{"sslmode": {s.TLSMode}, "connect_timeout": {"5"}, "timezone": {"UTC"}, "application_name": {"ontology-data"}, "client_encoding": {"UTF8"}}).Encode()
		cfg, err := pgx.ParseConfig(u.String())
		if err != nil {
			return nil, sourceVersionError(err)
		}
		return stdlib.GetConnector(*cfg), nil
	}
	return nil, problem(422, "unsupported_driver", "不支持的数据源类型")
}

// Every physical connection is inspected before being handed to database/sql.
// All connections in one request must have the same server/configuration stamp,
// including connections created later by the pool or by a retry.
type checkedSourceConnector struct {
	base      driver.Connector
	source    c.DataSource
	mu        sync.Mutex
	baseline  string
	observed  storage.DatabaseVersion
	connected bool
}

func (c *checkedSourceConnector) Driver() driver.Driver { return c.base.Driver() }
func (c *checkedSourceConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.base.Connect(ctx)
	if err != nil {
		return nil, sourceVersionError(err)
	}
	probe := sql.OpenDB(borrowedSourceConnector{conn: conn, base: c.base.Driver()})
	probe.SetMaxOpenConns(1)
	version, err := storage.InspectDatabaseVersion(ctx, probe, c.source.Driver, c.source.VersionPolicy, c.source.ExpectedVersionLine)
	_ = probe.Close()
	c.mu.Lock()
	c.connected, c.observed = true, version
	if err == nil && c.baseline != "" && c.baseline != version.Signature {
		err = problem(422, "source_version_changed", "数据源连接的产品、版本或配置已改变，请重新检查后重试")
	}
	if err == nil {
		c.baseline = version.Signature
	}
	c.mu.Unlock()
	if err != nil {
		_ = conn.Close()
		return nil, sourceVersionError(err)
	}
	return conn, nil
}

// Inspection borrows exactly the new connection without closing it. Ordinary
// execution receives the original driver.Conn and retains its optional APIs.
type borrowedSourceConnector struct {
	conn driver.Conn
	base driver.Driver
}
type borrowedSourceConn struct{ driver.Conn }

func (b borrowedSourceConnector) Driver() driver.Driver { return b.base }
func (b borrowedSourceConnector) Connect(context.Context) (driver.Conn, error) {
	return borrowedSourceConn{b.conn}, nil
}
func (b borrowedSourceConn) Close() error { return nil }

func connectSource(ctx context.Context, s c.DataSource, password string) (*sql.DB, *c.SourceCompatibility, error) {
	observed := &c.SourceCompatibility{CompatibilityStatus: "failed", Capabilities: []string{}, CheckedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	base, err := sourceConnector(s, password)
	if err != nil {
		observed.Code, observed.Message = "invalid_source", "数据源连接配置不合法"
		return nil, observed, sourceVersionError(err)
	}
	connector := &checkedSourceConnector{base: base, source: s}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(3)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(time.Minute)
	db.SetConnMaxIdleTime(15 * time.Second)
	probe, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	err = db.PingContext(probe)
	connector.mu.Lock()
	v := connector.observed
	observed.Connected = connector.connected
	connector.mu.Unlock()
	observed.DetectedProduct, observed.ServerVersion, observed.VersionLine = v.Product, v.ServerVersion, v.VersionLine
	observed.CapabilityProfile, observed.Signature = v.CapabilityProfile, v.Signature
	if v.Capabilities != nil {
		observed.Capabilities = v.Capabilities
	}
	if err == nil {
		observed.CompatibilityStatus = "supported"
		return db, observed, nil
	}
	err = sourceVersionError(err)
	var failure *apiError
	if errors.As(err, &failure) {
		observed.Code, observed.Message = failure.code, failure.message
		if failure.code == "database_version_mismatch" {
			observed.CompatibilityStatus = "mismatch"
		} else if observed.Connected {
			observed.CompatibilityStatus = "unsupported"
		}
	}
	_ = db.Close()
	return nil, observed, err
}

func sourceVersionSignature(s c.DataSource) string {
	if s.Compatibility == nil {
		return ""
	}
	return s.Compatibility.Signature
}
