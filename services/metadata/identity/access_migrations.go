package identity

import "github.com/wuhy80/AgentForge-Ontology/libs/storage"

var externalMigrations = []storage.Migration{
	{ID: "015_identity_access_connections", SQL: `CREATE TABLE af_identity_access_connections (
 id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
 tenant_id VARBINARY(128) NOT NULL,
 user_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 name VARCHAR(128) NOT NULL,
 token_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 scopes TEXT NOT NULL,
 resource VARCHAR(1024) NOT NULL,
 kind VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 client_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 user_etag CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 role_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 created_at BIGINT NOT NULL,
 expires_at BIGINT NOT NULL,
 revoked_at BIGINT NOT NULL DEFAULT 0,
 UNIQUE KEY uq_identity_access_hash (token_hash),
 KEY ix_identity_access_owner (tenant_id,user_id,created_at)
) ENGINE=InnoDB`},
	{ID: "016_identity_oauth_clients", SQL: `CREATE TABLE af_identity_oauth_clients (
 id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
 name VARCHAR(128) NOT NULL,
 redirect_uris TEXT NOT NULL,
 created_at BIGINT NOT NULL,
 expires_at BIGINT NOT NULL,
 KEY ix_identity_oauth_clients_expiry (expires_at)
) ENGINE=InnoDB`},
	{ID: "017_identity_oauth_codes", SQL: `CREATE TABLE af_identity_oauth_codes (
 code_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
 tenant_id VARBINARY(128) NOT NULL,
 user_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 user_etag CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 role_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 client_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 redirect_uri VARCHAR(2048) NOT NULL,
 code_challenge CHAR(43) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 scopes TEXT NOT NULL,
 resource VARCHAR(1024) NOT NULL,
 expires_at BIGINT NOT NULL,
 consumed_at BIGINT NOT NULL DEFAULT 0,
 KEY ix_identity_oauth_codes_owner (tenant_id,user_id,expires_at)
) ENGINE=InnoDB`},
	{ID: "018_identity_oauth_registration_guard", SQL: `CREATE TABLE af_identity_oauth_registration_guard (id TINYINT NOT NULL PRIMARY KEY) ENGINE=InnoDB`},
	{ID: "019_identity_oauth_registration_guard_seed", SQL: `INSERT INTO af_identity_oauth_registration_guard (id) VALUES (1)`},
	{ID: "020_identity_oauth_codes_expiry", SQL: `ALTER TABLE af_identity_oauth_codes ADD INDEX ix_identity_oauth_codes_expiry (expires_at)`},
}
