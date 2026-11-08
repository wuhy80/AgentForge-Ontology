package data

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
)

const unavailableOverviewMetric = "当前数据库权限或驱动未提供此指标"

func overviewMetric(unit, scope, basis string) c.SourceOverviewMetric {
	return c.SourceOverviewMetric{Unit: unit, Scope: scope, Basis: basis, Reason: unavailableOverviewMetric}
}
func availableOverviewMetric(metric *c.SourceOverviewMetric, value string) {
	metric.Available, metric.Value, metric.Reason = true, value, ""
}

func readSourceOverview(ctx context.Context, db *sql.DB, source c.DataSource) c.SourceOverview {
	out := c.SourceOverview{SourceID: source.ID, ETag: source.ETag, Database: source.Database}
	if source.Compatibility != nil {
		out.ServerVersion = source.Compatibility.ServerVersion
	}
	if source.Driver == "mysql" {
		out.Metrics.Connections = overviewMetric("connections", "instance", "Threads_connected：当前实例连接数的瞬时值，包含本管理连接")
		out.Metrics.StorageBytes = overviewMetric("bytes", "database", "配置数据库内当前账号可见普通表的 DATA_LENGTH + INDEX_LENGTH 目录估算，不含日志和临时空间")
		out.Metrics.QPS = overviewMetric("queries/s", "instance", "实例全局 Questions 计数的短时差分，目标采样约250毫秒，包含本次管理查询开销，不代表单个业务数据库QPS")
		_ = overviewQuery(ctx, db, `SELECT DEFAULT_CHARACTER_SET_NAME FROM information_schema.SCHEMATA WHERE SCHEMA_NAME=?`, []any{source.Database}, &out.Charset)
		var total, known int
		var size string
		if overviewQuery(ctx, db, `SELECT COUNT(*),COUNT(DATA_LENGTH+INDEX_LENGTH),COALESCE(SUM(DATA_LENGTH+INDEX_LENGTH),0) FROM information_schema.tables WHERE TABLE_SCHEMA=? AND TABLE_TYPE='BASE TABLE'`, []any{source.Database}, &total, &known, &size) == nil && total == known {
			if _, err := strconv.ParseUint(size, 10, 64); err == nil {
				availableOverviewMetric(&out.Metrics.StorageBytes, size)
			}
		}
		first, err := mysqlOverviewStatus(ctx, db)
		if err == nil {
			if connected, ok := first["Threads_connected"]; ok {
				availableOverviewMetric(&out.Metrics.Connections, strconv.FormatUint(connected, 10))
			}
			if questions, ok := first["Questions"]; ok {
				start := time.Now()
				timer := time.NewTimer(250 * time.Millisecond)
				select {
				case <-timer.C:
				case <-ctx.Done():
					timer.Stop()
					out.CheckedAt = time.Now().UTC().Format(time.RFC3339Nano)
					return out
				}
				second, err := mysqlOverviewStatus(ctx, db)
				elapsed := time.Since(start)
				if current, ok := second["Questions"]; err == nil && ok && current >= questions && elapsed > 0 {
					availableOverviewMetric(&out.Metrics.QPS, strconv.FormatFloat(float64(current-questions)/elapsed.Seconds(), 'f', 2, 64))
					out.Metrics.QPS.SampleWindowMS = elapsed.Milliseconds()
				}
			}
		}
	} else {
		out.Metrics.Connections = overviewMetric("connections", "database", "pg_stat_database.numbackends：配置数据库当前后端连接数，包含本管理连接")
		out.Metrics.StorageBytes = overviewMetric("bytes", "database", "pg_database_size(current_database())：配置数据库的实际存储字节数")
		out.Metrics.QPS = overviewMetric("queries/s", "database", "未启用可归属且可比的语句计数；事务数不能作为查询数")
		out.Metrics.QPS.Reason = "PostgreSQL默认统计不提供同口径QPS，未将TPS当作QPS"
		_ = overviewQuery(ctx, db, `SHOW server_encoding`, nil, &out.Charset)
		var connections, size string
		if overviewQuery(ctx, db, `SELECT numbackends FROM pg_catalog.pg_stat_database WHERE datname=current_database()`, nil, &connections) == nil {
			if _, err := strconv.ParseUint(connections, 10, 64); err == nil {
				availableOverviewMetric(&out.Metrics.Connections, connections)
			}
		}
		if overviewQuery(ctx, db, `SELECT pg_catalog.pg_database_size(current_database())`, nil, &size) == nil {
			if _, err := strconv.ParseUint(size, 10, 64); err == nil {
				availableOverviewMetric(&out.Metrics.StorageBytes, size)
			}
		}
	}
	out.CheckedAt = time.Now().UTC().Format(time.RFC3339Nano)
	return out
}

func overviewQuery(ctx context.Context, db *sql.DB, query string, args []any, dest ...any) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return db.QueryRowContext(ctx, query, args...).Scan(dest...)
}

func mysqlOverviewStatus(ctx context.Context, db *sql.DB) (map[string]uint64, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	rows, err := db.QueryContext(ctx, `SHOW GLOBAL STATUS WHERE Variable_name IN ('Threads_connected','Questions')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := map[string]uint64{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		if count, err := strconv.ParseUint(value, 10, 64); err == nil {
			values[key] = count
		}
	}
	return values, rows.Err()
}

func (h *handler) sourceOverviewSession(r *http.Request, p c.Principal) error {
	checker := h.cfg.SessionChecker
	if checker == nil {
		checker, _ = h.cfg.Authorizer.(platform.SessionChecker)
	}
	if checker == nil {
		return problem(503, "identity_unavailable", "无法复核当前数据源管理员会话")
	}
	check, err := platform.SessionRequest(r)
	if err != nil {
		var access *platform.AccessError
		if errors.As(err, &access) {
			return problem(access.Status, access.Code, access.Message)
		}
		return problem(401, "unauthorized", "当前登录校验失败")
	}
	session, err := checker.CheckSession(r.Context(), check)
	if err != nil {
		var access *platform.AccessError
		if errors.As(err, &access) && (access.Status == 401 || access.Status == 403 || access.Status == 503) {
			return problem(access.Status, access.Code, access.Message)
		}
		return problem(503, "identity_unavailable", "无法复核当前数据源管理员会话")
	}
	if !session.User.Enabled || session.User.ID != p.Subject || session.User.TenantID != p.TenantID || session.ExpiresAt <= time.Now().Unix() {
		return problem(401, "unauthorized", "当前登录已失效")
	}
	if !session.User.Administrator {
		return problem(403, "administrator_required", "只有当前租户管理员可以查看数据库概览")
	}
	return nil
}

func (h *handler) sourceOverviewHTTP(w http.ResponseWriter, r *http.Request) error {
	p, err := h.sourceAdmin(r)
	if err != nil {
		return err
	}
	if err = h.sourceOverviewSession(r, p); err != nil {
		return err
	}
	db, source, err := h.openSource(r.Context(), p.TenantID, r.PathValue("id"))
	if err != nil {
		return err
	}
	defer db.Close()
	result := readSourceOverview(r.Context(), db, source)
	if r.Context().Err() != nil {
		return problem(503, "source_overview_failed", "数据库概览采样超时或请求已取消")
	}
	if err = h.sourceOverviewSession(r, p); err != nil {
		return err
	}
	current, _, err := h.loadSource(r.Context(), p.TenantID, source.ID)
	if err != nil {
		return err
	}
	if !current.Enabled || current.ETag != source.ETag {
		return problem(412, "precondition_failed", "采样期间数据源已改变，请刷新后重试")
	}
	w.Header().Set("ETag", `"`+source.ETag+`"`)
	writeJSON(w, 200, result)
	return nil
}
