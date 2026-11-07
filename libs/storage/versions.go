package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// QueryRower permits version inspection on an explicitly selected connection.
// Callers must inspect the same physical session that performs the operation.
type QueryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// DatabaseVersion describes the inspected server and this adapter's operations.
// It does not grant database permissions or enable unimplemented application APIs.
type DatabaseVersion struct {
	Product           string   `json:"product"`
	ServerVersion     string   `json:"serverVersion"`
	VersionLine       string   `json:"versionLine"`
	CapabilityProfile string   `json:"capabilityProfile"`
	Capabilities      []string `json:"capabilities"`
	Signature         string   `json:"signature"`
}

type VersionOption struct {
	Driver      string `json:"driver"`
	VersionLine string `json:"versionLine"`
	Label       string `json:"label"`
	Status      string `json:"status"`
}

// VersionError contains a public diagnostic and never wraps driver/DSN errors.
type VersionError struct {
	Code    string
	Message string
}

func (e *VersionError) Error() string { return e.Message }

type versionRegistration struct {
	option           VersionOption
	minimum, maximum [3]int
	profile          string
}

// Each range is intentional. New series are not inferred from larger numbers.
// Pending targets become supported only after real-backend certification.
var databaseVersionRegistrations = []versionRegistration{
	{VersionOption{"mysql", "26.7", "MySQL 26.7", "supported"}, [3]int{26, 7, 0}, [3]int{26, 7, 0}, "mysql-26.7-v1"},
	{VersionOption{"mysql", "8.4", "MySQL 8.4", "pending"}, [3]int{8, 4, 0}, [3]int{8, 4, 99}, "mysql-8.4-v1"},
	{VersionOption{"mysql", "8.0", "MySQL 8.0", "pending"}, [3]int{8, 0, 17}, [3]int{8, 0, 99}, "mysql-8.0-v1"},
	{VersionOption{"postgresql", "17", "PostgreSQL 17", "supported"}, [3]int{17, 0, 0}, [3]int{17, 99, 0}, "postgresql-17-v1"},
	{VersionOption{"postgresql", "16", "PostgreSQL 16", "pending"}, [3]int{16, 0, 0}, [3]int{16, 99, 0}, "postgresql-16-v1"},
}

type platformVersionRegistration struct {
	product, versionLine, profile string
	minimum, maximum              [3]int
}

// Control-table storage has its own certification ranges. Certifying an
// external source does not certify platform migrations, identity transactions,
// locks, or recovery on that server. Keep these ranges independent even when
// they currently coincide with an external-source registration.
var platformVersionRegistrations = []platformVersionRegistration{
	{"mysql", "26.7", "mysql-26.7-v1", [3]int{26, 7, 0}, [3]int{26, 7, 0}},
}

func validatePlatformDatabaseVersion(version DatabaseVersion) error {
	parts := mysqlVersionPattern.FindStringSubmatch(version.ServerVersion)
	if version.Product == "mysql" && parts != nil {
		var number [3]int
		for i := range number {
			number[i], _ = strconv.Atoi(parts[i+1])
		}
		for _, registration := range platformVersionRegistrations {
			if version.Product == registration.product && version.VersionLine == registration.versionLine && version.CapabilityProfile == registration.profile && compareVersion(number, registration.minimum) >= 0 && compareVersion(number, registration.maximum) <= 0 {
				return nil
			}
		}
	}
	return versionProblem("unsupported_platform_database_version", "当前平台控制库存储尚未认证该数据库版本")
}

// DatabaseVersionOptions returns a copy: consumers cannot mutate the registry.
func DatabaseVersionOptions() []VersionOption {
	options := make([]VersionOption, len(databaseVersionRegistrations))
	for i, registration := range databaseVersionRegistrations {
		options[i] = registration.option
	}
	return options
}

func versionProblem(code, message string) error {
	return &VersionError{Code: code, Message: message}
}

// ValidateDatabaseVersionPolicy checks administrator input, without connecting.
// An omitted policy means automatic detection for existing stored configurations.
func ValidateDatabaseVersionPolicy(driver, policy, expected string) error {
	if driver != "mysql" && driver != "postgresql" {
		return versionProblem("unsupported_driver", "不支持的数据源类型")
	}
	if policy == "" {
		policy = "auto"
	}
	if policy == "auto" {
		if expected != "" {
			return versionProblem("invalid_version_policy", "自动识别不能同时指定预期版本")
		}
		return nil
	}
	if policy != "pinned" || expected == "" {
		return versionProblem("invalid_version_policy", "数据库版本须选择自动识别或指定已支持的版本系列")
	}
	for _, registration := range databaseVersionRegistrations {
		if registration.option.Driver == driver && registration.option.VersionLine == expected && registration.option.Status == "supported" {
			return nil
		}
	}
	return versionProblem("unsupported_database_version", "所选数据库版本系列尚未通过适配验证")
}

// InspectDatabaseVersion performs read-only product, version and configuration
// checks. Even on mismatch or unsupported versions it returns detected identity.
func InspectDatabaseVersion(ctx context.Context, db QueryRower, driver, policy, expected string) (DatabaseVersion, error) {
	var result DatabaseVersion
	if err := ValidateDatabaseVersionPolicy(driver, policy, expected); err != nil {
		return result, err
	}
	if ctx == nil || db == nil {
		return result, versionProblem("database_version_unavailable", "无法读取数据库版本信息")
	}
	var number [3]int
	var settings []string
	var err error
	if driver == "mysql" {
		result, number, settings, err = inspectMySQLVersion(ctx, db)
	} else {
		result, number, settings, err = inspectPostgreSQLVersion(ctx, db)
	}
	if err != nil {
		return result, err
	}
	if policy == "pinned" && result.VersionLine != expected {
		return result, versionProblem("database_version_mismatch", fmt.Sprintf("数据库版本不匹配：预期%s，实际%s", expected, result.VersionLine))
	}
	for _, registration := range databaseVersionRegistrations {
		if registration.option.Driver != driver || registration.option.VersionLine != result.VersionLine || registration.option.Status != "supported" || compareVersion(number, registration.minimum) < 0 || compareVersion(number, registration.maximum) > 0 {
			continue
		}
		result.CapabilityProfile = registration.profile
		result.Capabilities = []string{"catalog", "read", "query", "createTable", "addColumn", "createIndex"}
		body, _ := json.Marshal(struct {
			Product, ServerVersion, Profile string
			Settings                        []string
		}{result.Product, result.ServerVersion, result.CapabilityProfile, settings})
		sum := sha256.Sum256(body)
		result.Signature = hex.EncodeToString(sum[:])
		return result, nil
	}
	return result, versionProblem("unsupported_database_version", fmt.Sprintf("%s %s尚未通过当前适配范围验证", result.Product, result.ServerVersion))
}

func compareVersion(a, b [3]int) int {
	for i := range a {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	return 0
}

var mysqlVersionPattern = regexp.MustCompile(`^([0-9]{1,3})\.([0-9]{1,3})\.([0-9]{1,3})(?:[-+][a-zA-Z0-9][a-zA-Z0-9.+_-]*)?$`)
var postgresVersionPattern = regexp.MustCompile(`^([0-9]{1,3})\.([0-9]{1,3})(?:\s+\([^\r\n]*\))?$`)

func inspectMySQLVersion(ctx context.Context, db QueryRower) (DatabaseVersion, [3]int, []string, error) {
	result := DatabaseVersion{Product: "mysql"}
	var number [3]int
	var comment, engine, charset, collation, sqlMode, zone string
	var binaryCollation bool
	err := db.QueryRowContext(ctx, `SELECT VERSION(),@@version_comment,@@default_storage_engine,@@character_set_connection,@@collation_connection,@@sql_mode,@@time_zone,EXISTS(SELECT 1 FROM information_schema.COLLATIONS WHERE COLLATION_NAME='utf8mb4_0900_bin')`).Scan(&result.ServerVersion, &comment, &engine, &charset, &collation, &sqlMode, &zone, &binaryCollation)
	if err != nil {
		return result, number, nil, versionProblem("database_version_unavailable", "无法读取MySQL版本或必要配置，请核对目录权限")
	}
	identity := strings.ToLower(result.ServerVersion + " " + comment)
	for _, other := range []string{"mariadb", "tidb", "percona", "vitess", "oceanbase", "polardb", "aurora", "doris"} {
		if strings.Contains(identity, other) {
			result.Product = other
			return result, number, nil, versionProblem("unsupported_database_product", "该兼容MySQL协议的数据库产品需单独适配")
		}
	}
	if !strings.Contains(strings.ToLower(comment), "mysql") {
		result.Product = "unknown"
		return result, number, nil, versionProblem("unsupported_database_product", "无法确认目标为已认证的MySQL产品")
	}
	parts := mysqlVersionPattern.FindStringSubmatch(result.ServerVersion)
	if parts == nil || strings.Contains(identity, "alpha") || strings.Contains(identity, "beta") || strings.Contains(identity, "-rc") || strings.Contains(identity, "-dev") {
		return result, number, nil, versionProblem("unsupported_database_version", "MySQL版本格式无法识别或为未认证的预发布版本")
	}
	for i := range number {
		number[i], _ = strconv.Atoi(parts[i+1])
	}
	result.VersionLine = fmt.Sprintf("%d.%d", number[0], number[1])
	if !strings.EqualFold(engine, "InnoDB") || !binaryCollation || !strings.EqualFold(charset, "utf8mb4") {
		return result, number, nil, versionProblem("unsupported_database_configuration", "MySQL需要InnoDB、utf8mb4连接字符集及utf8mb4_0900_bin排序规则")
	}
	return result, number, []string{comment, strings.ToLower(engine), strings.ToLower(charset), collation, sqlMode, zone}, nil
}

func inspectPostgreSQLVersion(ctx context.Context, db QueryRower) (DatabaseVersion, [3]int, []string, error) {
	result := DatabaseVersion{Product: "postgresql"}
	var number [3]int
	var banner, numeric, serverEncoding, clientEncoding, standardStrings, zone string
	err := db.QueryRowContext(ctx, `SELECT version(),current_setting('server_version'),current_setting('server_version_num'),current_setting('server_encoding'),current_setting('client_encoding'),current_setting('standard_conforming_strings'),current_setting('TimeZone')`).Scan(&banner, &result.ServerVersion, &numeric, &serverEncoding, &clientEncoding, &standardStrings, &zone)
	if err != nil {
		return result, number, nil, versionProblem("database_version_unavailable", "无法读取PostgreSQL版本或必要配置，请核对目录权限")
	}
	identity := strings.ToLower(banner + " " + result.ServerVersion)
	for _, other := range []string{"cockroach", "yugabyte", "redshift", "greenplum", "enterprisedb", "opengauss", "kingbase"} {
		if strings.Contains(identity, other) {
			result.Product = other
			return result, number, nil, versionProblem("unsupported_database_product", "该兼容PostgreSQL协议的数据库产品需单独适配")
		}
	}
	if !strings.HasPrefix(banner, "PostgreSQL ") {
		result.Product = "unknown"
		return result, number, nil, versionProblem("unsupported_database_product", "无法确认目标为已认证的PostgreSQL产品")
	}
	parts := postgresVersionPattern.FindStringSubmatch(result.ServerVersion)
	value, parseErr := strconv.Atoi(numeric)
	if parts == nil || parseErr != nil || value < 100000 || value > 9999999 {
		return result, number, nil, versionProblem("unsupported_database_version", "PostgreSQL版本格式无法识别")
	}
	number[0], _ = strconv.Atoi(parts[1])
	number[1], _ = strconv.Atoi(parts[2])
	if value != number[0]*10000+number[1] {
		return result, number, nil, versionProblem("unsupported_database_version", "PostgreSQL数字版本与显示版本不一致")
	}
	result.VersionLine = strconv.Itoa(number[0])
	if !strings.EqualFold(serverEncoding, "UTF8") || !strings.EqualFold(clientEncoding, "UTF8") || standardStrings != "on" {
		return result, number, nil, versionProblem("unsupported_database_configuration", "PostgreSQL需要UTF8编码和standard_conforming_strings=on")
	}
	return result, number, []string{banner, serverEncoding, clientEncoding, standardStrings, zone}, nil
}
