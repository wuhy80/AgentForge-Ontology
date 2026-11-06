// Package storage owns database connection and transaction facilities. Business
// repositories, model rules and SQL query plans remain in their owning service.
package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var (
	ErrUnsupportedDriver = errors.New("unsupported storage driver")
	ErrDuplicateDriver   = errors.New("storage driver already registered")
)

type Config struct {
	Driver          string
	DSN             string
	MaxOpen         int
	MaxIdle         int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
}

// Capabilities describe only the operations implemented by an adapter. They do
// not certify application features, server performance or untested versions.
type Capabilities struct {
	Driver            string `json:"driver"`
	ServerVersion     string `json:"serverVersion"`
	LocalTransactions bool   `json:"localTransactions"`
	TransactionalDDL  bool   `json:"transactionalDdl"`
	ParameterizedSQL  bool   `json:"parameterizedSql"`
}

// Connector is the adapter construction boundary. Only infrastructure adapters
// need to know GORM; service repositories use Engine and Session below.
type Connector func(context.Context, Config) (*sql.DB, gorm.Dialector, Capabilities, error)

type Registry struct {
	mu         sync.RWMutex
	connectors map[string]Connector
	frozen     bool
}

func NewRegistry() *Registry {
	r := &Registry{connectors: make(map[string]Connector)}
	_ = r.Register("mysql", connectMySQL)
	return r
}

func (r *Registry) Register(name string, connector Connector) error {
	name = strings.TrimSpace(name)
	if name == "" || connector == nil {
		return errors.New("driver name and connector are required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.frozen {
		return errors.New("storage registry is frozen after its first open")
	}
	if _, exists := r.connectors[name]; exists {
		return fmt.Errorf("%w: %s", ErrDuplicateDriver, name)
	}
	if r.connectors == nil {
		r.connectors = make(map[string]Connector)
	}
	r.connectors[name] = connector
	return nil
}

func (r *Registry) Open(ctx context.Context, cfg Config) (*Engine, error) {
	if ctx == nil {
		return nil, errors.New("storage context is required")
	}
	if cfg.Driver == "" {
		return nil, ErrUnsupportedDriver
	}
	if cfg.MaxOpen < 0 || cfg.MaxIdle < 0 {
		return nil, errors.New("connection limits cannot be negative")
	}
	if cfg.MaxOpen == 0 {
		cfg.MaxOpen = 10
	}
	if cfg.MaxIdle == 0 {
		cfg.MaxIdle = min(5, cfg.MaxOpen)
	}
	if cfg.MaxIdle > cfg.MaxOpen {
		return nil, errors.New("max idle connections exceeds max open connections")
	}
	if cfg.ConnMaxLifetime == 0 {
		cfg.ConnMaxLifetime = 5 * time.Minute
	}
	if cfg.ConnMaxIdleTime == 0 {
		cfg.ConnMaxIdleTime = time.Minute
	}
	if cfg.ConnMaxLifetime < 0 || cfg.ConnMaxIdleTime < 0 {
		return nil, errors.New("connection lifetime cannot be negative")
	}
	r.mu.Lock()
	r.frozen = true
	connector, exists := r.connectors[cfg.Driver]
	r.mu.Unlock()
	if !exists {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedDriver, cfg.Driver)
	}
	db, dialector, capabilities, err := connector(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if db == nil || dialector == nil {
		if db != nil {
			_ = db.Close()
		}
		return nil, errors.New("storage connector returned an incomplete adapter")
	}
	db.SetMaxOpenConns(cfg.MaxOpen)
	db.SetMaxIdleConns(cfg.MaxIdle)
	db.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	db.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)
	orm, err := gorm.Open(dialector, &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize ORM adapter: %w", err)
	}
	return &Engine{db: db, orm: orm, capabilities: capabilities}, nil
}

func Open(ctx context.Context, cfg Config) (*Engine, error) { return NewRegistry().Open(ctx, cfg) }

type Engine struct {
	db           *sql.DB
	orm          *gorm.DB
	capabilities Capabilities
}

func (e *Engine) DB() *sql.DB                { return e.db }
func (e *Engine) Close() error               { return e.db.Close() }
func (e *Engine) Capabilities() Capabilities { return e.capabilities }

// WithTx always binds statements to one connection and uses READ COMMITTED.
// A commit failure can mean an unknown outcome: callers must reconcile their
// operation ID instead of automatically retrying a non-idempotent transaction.
func (e *Engine) WithTx(ctx context.Context, fn func(*sql.Tx) error) error {
	if ctx == nil || fn == nil {
		return errors.New("transaction context and callback are required")
	}
	tx, err := e.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction (outcome may be unknown): %w", err)
	}
	return nil
}

// Session exposes the SQL executor and a small fixed-record ORM helper without
// putting gorm.DB into a domain interface. Both routes use the same sql.Tx.
type Session struct {
	tx  *sql.Tx
	orm *gorm.DB
}

func (e *Engine) WithSession(ctx context.Context, fn func(*Session) error) error {
	if fn == nil {
		return errors.New("session callback is required")
	}
	return e.WithTx(ctx, func(tx *sql.Tx) error {
		orm := e.orm.Session(&gorm.Session{NewDB: true, Context: ctx})
		orm.Statement.ConnPool = tx
		return fn(&Session{tx: tx, orm: orm})
	})
}

func (s *Session) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return s.tx.ExecContext(ctx, query, args...)
}
func (s *Session) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return s.tx.QueryContext(ctx, query, args...)
}
func (s *Session) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return s.tx.QueryRowContext(ctx, query, args...)
}

var identifier = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]{0,63}$`)

// Create is for fixed service record mappings. table must come from trusted
// adapter code; dynamic user values are bound by GORM, never SQL text.
func (s *Session) Create(table string, value any) error {
	if !identifier.MatchString(table) {
		return errors.New("invalid trusted table identifier")
	}
	if value == nil {
		return errors.New("record is required")
	}
	return s.orm.Table(table).Create(value).Error
}
