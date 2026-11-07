package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	mysqlsql "github.com/go-sql-driver/mysql"
	mysqlorm "gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func connectMySQL(ctx context.Context, cfg Config) (*sql.DB, gorm.Dialector, Capabilities, error) {
	dsn, err := mysqlsql.ParseDSN(cfg.DSN)
	if err != nil {
		return nil, nil, Capabilities{}, errors.New("invalid MySQL DSN")
	}
	if dsn.DBName == "" {
		return nil, nil, Capabilities{}, errors.New("MySQL DSN must name an existing database")
	}
	if dsn.MultiStatements || dsn.InterpolateParams {
		return nil, nil, Capabilities{}, errors.New("MySQL multiStatements and interpolateParams must be disabled")
	}
	dsn.ParseTime = true
	dsn.Loc = time.UTC
	if dsn.Timeout == 0 {
		dsn.Timeout = 5 * time.Second
	}
	connector, err := mysqlsql.NewConnector(dsn)
	if err != nil {
		return nil, nil, Capabilities{}, errors.New("invalid MySQL connector configuration")
	}
	db := sql.OpenDB(connector)
	fail := func(err error) (*sql.DB, gorm.Dialector, Capabilities, error) {
		_ = db.Close()
		return nil, nil, Capabilities{}, err
	}
	if err := db.PingContext(ctx); err != nil {
		return fail(fmt.Errorf("connect MySQL: %w", err))
	}
	version, err := InspectDatabaseVersion(ctx, db, "mysql", "auto", "")
	if err != nil {
		return fail(err)
	}
	if err := validatePlatformDatabaseVersion(version); err != nil {
		return fail(err)
	}
	capabilities := Capabilities{Driver: "mysql", ServerVersion: version.ServerVersion, LocalTransactions: true, ParameterizedSQL: true, TransactionalDDL: false}
	dialect := mysqlorm.New(mysqlorm.Config{Conn: db, SkipInitializeWithVersion: true})
	return db, dialect, capabilities, nil
}
