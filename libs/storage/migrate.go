package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrMigrationBusy        = errors.New("migration lock is busy")
	ErrMigrationChanged     = errors.New("recorded migration checksum differs")
	ErrMigrationNeedsRepair = errors.New("migration needs verified repair")
)

type Migration struct {
	ID  string
	SQL string
}

const migrationTableDDL = `CREATE TABLE IF NOT EXISTS af_storage_migrations (
 service VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 migration_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 checksum CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 status VARCHAR(24) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 error_text TEXT NULL,
 started_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 completed_at TIMESTAMP(6) NULL,
 PRIMARY KEY (service, migration_id)
) ENGINE=InnoDB`

// MigrationChecksum identifies the exact statement. It is not a business model
// version and cannot be used to select a historical definition.
func MigrationChecksum(migration Migration) string {
	sum := sha256.Sum256([]byte(migration.SQL))
	return hex.EncodeToString(sum[:])
}

func validateMigration(service string, migrations []Migration) error {
	if !identifier.MatchString(service) {
		return errors.New("migration service must be an ASCII identifier of at most 64 characters")
	}
	seen := make(map[string]bool, len(migrations))
	for _, migration := range migrations {
		if migration.ID == "" || len(migration.ID) > 128 {
			return errors.New("migration ID is required and cannot exceed 128 bytes")
		}
		for _, r := range migration.ID {
			if r < 33 || r > 126 {
				return errors.New("migration ID must contain printable ASCII without spaces")
			}
		}
		if seen[migration.ID] {
			return fmt.Errorf("duplicate migration ID: %s", migration.ID)
		}
		seen[migration.ID] = true
		if strings.TrimSpace(migration.SQL) == "" {
			return errors.New("migration SQL cannot be empty")
		}
	}
	return nil
}

// Migrate executes one trusted SQL statement per migration, with a dedicated
// connection holding a database/service advisory lock for the whole operation.
// MySQL DDL is not transactional. A previously running or failed statement is
// never automatically retried: its actual structure must first be reconciled.
func Migrate(ctx context.Context, db *sql.DB, service string, migrations []Migration) error {
	if err := validateMigration(service, migrations); err != nil {
		return err
	}
	return withMigrationLock(ctx, db, service, func(conn *sql.Conn) error {
		if _, err := conn.ExecContext(ctx, migrationTableDDL); err != nil {
			return fmt.Errorf("create migration journal: %w", err)
		}
		for _, migration := range migrations {
			checksum := MigrationChecksum(migration)
			var recorded, status string
			err := conn.QueryRowContext(ctx, "SELECT checksum, status FROM af_storage_migrations WHERE service = ? AND migration_id = ?", service, migration.ID).Scan(&recorded, &status)
			if err == nil {
				if recorded != checksum {
					return fmt.Errorf("%w: %s/%s", ErrMigrationChanged, service, migration.ID)
				}
				if status == "succeeded" {
					continue
				}
				return fmt.Errorf("%w: %s/%s (%s)", ErrMigrationNeedsRepair, service, migration.ID, status)
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("read migration journal: %w", err)
			}
			if _, err := conn.ExecContext(ctx, "INSERT INTO af_storage_migrations (service, migration_id, checksum, status) VALUES (?, ?, ?, 'running')", service, migration.ID, checksum); err != nil {
				return fmt.Errorf("register migration: %w", err)
			}
			if _, err := conn.ExecContext(ctx, migration.SQL); err != nil {
				recordMigrationFailure(conn, service, migration.ID, err)
				return fmt.Errorf("%w: %s/%s: %w", ErrMigrationNeedsRepair, service, migration.ID, err)
			}
			result, err := conn.ExecContext(ctx, "UPDATE af_storage_migrations SET status = 'succeeded', error_text = NULL, completed_at = CURRENT_TIMESTAMP(6) WHERE service = ? AND migration_id = ? AND checksum = ? AND status = 'running'", service, migration.ID, checksum)
			if err == nil {
				err = requireOneMigrationRow(result)
			}
			if err != nil {
				return fmt.Errorf("%w: statement completed but journal confirmation failed for %s/%s: %w", ErrMigrationNeedsRepair, service, migration.ID, err)
			}
		}
		return nil
	})
}

func recordMigrationFailure(conn *sql.Conn, service, id string, cause error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	message := cause.Error()
	if len(message) > 4096 {
		message = message[:4096]
	}
	_, _ = conn.ExecContext(ctx, "UPDATE af_storage_migrations SET status = 'needs_repair', error_text = ? WHERE service = ? AND migration_id = ? AND status = 'running'", message, service, id)
}

// CompleteMigration repairs journal state only after a caller-supplied verifier
// inspects the actual target structure on the locked connection. Verification
// must establish that the migration's full intended effect is already present.
// It never executes the migration again and never marks an unknown DDL as done.
func CompleteMigration(ctx context.Context, db *sql.DB, service string, migration Migration, verify func(context.Context, *sql.Conn) error) error {
	if err := validateMigration(service, []Migration{migration}); err != nil {
		return err
	}
	if verify == nil {
		return errors.New("migration completion requires a target structure verifier")
	}
	return withMigrationLock(ctx, db, service, func(conn *sql.Conn) error {
		var checksum, status string
		if err := conn.QueryRowContext(ctx, "SELECT checksum, status FROM af_storage_migrations WHERE service = ? AND migration_id = ?", service, migration.ID).Scan(&checksum, &status); err != nil {
			return fmt.Errorf("read migration to repair: %w", err)
		}
		if checksum != MigrationChecksum(migration) {
			return ErrMigrationChanged
		}
		if status == "succeeded" {
			return nil
		}
		if status != "running" && status != "needs_repair" {
			return fmt.Errorf("unexpected migration state: %s", status)
		}
		if err := verify(ctx, conn); err != nil {
			return fmt.Errorf("%w: target verification failed: %w", ErrMigrationNeedsRepair, err)
		}
		result, err := conn.ExecContext(ctx, "UPDATE af_storage_migrations SET status = 'succeeded', error_text = NULL, completed_at = CURRENT_TIMESTAMP(6) WHERE service = ? AND migration_id = ? AND checksum = ? AND status = ?", service, migration.ID, checksum, status)
		if err != nil {
			return fmt.Errorf("%w: record verified migration: %w", ErrMigrationNeedsRepair, err)
		}
		return requireOneMigrationRow(result)
	})
}

func requireOneMigrationRow(result sql.Result) error {
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%w: cannot confirm migration journal update: %w", ErrMigrationNeedsRepair, err)
	}
	if rows != 1 {
		return fmt.Errorf("%w: migration journal update affected %d rows", ErrMigrationNeedsRepair, rows)
	}
	return nil
}

func withMigrationLock(ctx context.Context, db *sql.DB, service string, fn func(*sql.Conn) error) (retErr error) {
	if ctx == nil || db == nil {
		return errors.New("migration context and database are required")
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	defer conn.Close()
	var database sql.NullString
	if err := conn.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&database); err != nil {
		return fmt.Errorf("read migration database: %w", err)
	}
	if !database.Valid || database.String == "" {
		return errors.New("migration connection has no current database")
	}
	hash := sha256.Sum256([]byte(database.String + "\x00" + service))
	lockName := "af:migrate:" + hex.EncodeToString(hash[:24])
	var acquired sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, ?)", lockName, 5).Scan(&acquired); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	if !acquired.Valid || acquired.Int64 != 1 {
		return ErrMigrationBusy
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var released sql.NullInt64
		err := conn.QueryRowContext(releaseCtx, "SELECT RELEASE_LOCK(?)", lockName).Scan(&released)
		if err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("release migration lock: %w", err))
		}
		if err == nil && (!released.Valid || released.Int64 != 1) {
			retErr = errors.Join(retErr, errors.New("migration connection lost its advisory lock"))
		}
	}()
	return fn(conn)
}
