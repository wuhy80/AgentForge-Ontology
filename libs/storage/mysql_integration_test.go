package storage

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"sync"
	"testing"
	"time"
)

// This test needs a disposable database supplied explicitly by the developer.
// It creates only uniquely named tables and removes those same tables afterward.
func TestMySQLORMAndSQLShareTransaction(t *testing.T) {
	dsn := os.Getenv("MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("MYSQL_TEST_DSN is not set; real MySQL transaction verification was not run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	engine, err := Open(ctx, Config{Driver: "mysql", DSN: dsn, MaxOpen: 4, MaxIdle: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	table := "af_storage_test_" + hex.EncodeToString(suffix[:])
	if _, err := engine.DB().ExecContext(ctx, "CREATE TABLE `"+table+"` (id VARCHAR(36) NOT NULL PRIMARY KEY, value VARCHAR(255) NOT NULL) ENGINE=InnoDB"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := engine.DB().ExecContext(cleanup, "DROP TABLE IF EXISTS `"+table+"`"); err != nil {
			t.Error(err)
		}
	}()
	rejected := errors.New("business constraint rejected")
	err = engine.WithSession(ctx, func(s *Session) error {
		if _, err := s.ExecContext(ctx, "INSERT INTO `"+table+"` (id, value) VALUES (?, ?)", "raw", "原始值"); err != nil {
			return err
		}
		if err := s.Create(table, &struct {
			ID    string
			Value string
		}{ID: "orm", Value: "映射值"}); err != nil {
			return err
		}
		return rejected
	})
	if !errors.Is(err, rejected) {
		t.Fatalf("rollback returned %v", err)
	}
	var count int
	if err := engine.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM `"+table+"`").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("rollback left %d records", count)
	}
	err = engine.WithSession(ctx, func(s *Session) error {
		if _, err := s.ExecContext(ctx, "INSERT INTO `"+table+"` (id, value) VALUES (?, ?)", "raw", "原始值"); err != nil {
			return err
		}
		return s.Create(table, &struct {
			ID    string
			Value string
		}{ID: "orm", Value: "映射值"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM `"+table+"`").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("commit stored %d records", count)
	}
}

func TestMySQLMigrationConcurrentAndVerifiedRecovery(t *testing.T) {
	dsn := os.Getenv("MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("MYSQL_TEST_DSN is not set; real MySQL migration verification was not run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	engine, err := Open(ctx, Config{Driver: "mysql", DSN: dsn, MaxOpen: 4, MaxIdle: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	service := "storage_test_" + hex.EncodeToString(suffix[:])
	table := "af_" + service
	migration := Migration{ID: "001_create", SQL: "CREATE TABLE `" + table + "` (id BIGINT NOT NULL PRIMARY KEY) ENGINE=InnoDB"}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := engine.DB().ExecContext(cleanup, "DROP TABLE IF EXISTS `"+table+"`"); err != nil {
			t.Error(err)
		}
		if _, err := engine.DB().ExecContext(cleanup, "DELETE FROM af_storage_migrations WHERE service = ?", service); err != nil {
			t.Error(err)
		}
	}()
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- Migrate(ctx, engine.DB(), service, []Migration{migration}) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := engine.DB().ExecContext(ctx, "UPDATE af_storage_migrations SET status = 'running' WHERE service = ?", service); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, engine.DB(), service, []Migration{migration}); !errors.Is(err, ErrMigrationNeedsRepair) {
		t.Fatalf("unknown DDL outcome replayed: %v", err)
	}
	err = CompleteMigration(ctx, engine.DB(), service, migration, func(ctx context.Context, conn *sql.Conn) error {
		rows, err := conn.QueryContext(ctx, "SELECT COLUMN_NAME, DATA_TYPE, IS_NULLABLE, COLUMN_KEY FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? ORDER BY ORDINAL_POSITION", table)
		if err != nil {
			return err
		}
		defer rows.Close()
		if !rows.Next() {
			return errors.New("target table not found")
		}
		var name, kind, nullable, key string
		if err := rows.Scan(&name, &kind, &nullable, &key); err != nil {
			return err
		}
		if name != "id" || kind != "bigint" || nullable != "NO" || key != "PRI" || rows.Next() {
			return errors.New("target structure mismatch")
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, engine.DB(), service, []Migration{migration}); err != nil {
		t.Fatal(err)
	}
}
