package identity

import "github.com/wuhy80/AgentForge-Ontology/libs/storage"

// Migrations must be appended to the Metadata migration set and run explicitly.
var Migrations = []storage.Migration{
	{ID: "008_identity_tenants", SQL: `CREATE TABLE af_identity_tenants (
 tenant_id VARBINARY(128) NOT NULL PRIMARY KEY,
 authorization_state CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 current_policy LONGTEXT NOT NULL,
 draft_policy LONGTEXT NULL,
 policy_etag CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL
) ENGINE=InnoDB`},
	{ID: "009_identity_users", SQL: `CREATE TABLE af_identity_users (
 id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
 tenant_id VARBINARY(128) NOT NULL,
 username VARCHAR(128) NOT NULL,
 username_key VARBINARY(512) NOT NULL,
 display_name VARCHAR(128) NOT NULL,
 password_hash VARCHAR(100) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 enabled BOOLEAN NOT NULL,
 administrator BOOLEAN NOT NULL,
 role_ids LONGTEXT NOT NULL,
 etag CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 UNIQUE KEY uq_identity_username (tenant_id,username_key),
 KEY ix_identity_users_tenant (tenant_id,id)
) ENGINE=InnoDB`},
	{ID: "010_identity_roles", SQL: `CREATE TABLE af_identity_roles (
 id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
 tenant_id VARBINARY(128) NOT NULL,
 name VARCHAR(128) NOT NULL,
 name_key VARBINARY(512) NOT NULL,
 description TEXT NOT NULL,
 etag CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 UNIQUE KEY uq_identity_role_name (tenant_id,name_key),
 KEY ix_identity_roles_tenant (tenant_id,id)
) ENGINE=InnoDB`},
	{ID: "011_identity_sessions", SQL: `CREATE TABLE af_identity_sessions (
 token_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
 tenant_id VARBINARY(128) NOT NULL,
 user_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 csrf_token CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 expires_at BIGINT NOT NULL,
 KEY ix_identity_sessions_user (tenant_id,user_id),
 KEY ix_identity_sessions_expiry (expires_at)
) ENGINE=InnoDB`},
	{ID: "012_identity_audit", SQL: `CREATE TABLE af_identity_audit (
 id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
 tenant_id VARBINARY(128) NOT NULL,
 actor_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 operation VARCHAR(64) NOT NULL,
 target_id VARCHAR(128) NOT NULL,
 created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 KEY ix_identity_audit_tenant (tenant_id,created_at,id)
) ENGINE=InnoDB`},
}
