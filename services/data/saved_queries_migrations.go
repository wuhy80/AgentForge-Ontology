package data

import "github.com/wuhy80/AgentForge-Ontology/libs/storage"

// Registered in Migrations and applied only by the explicit migrate command.
// The owner guard serializes creation/counting so concurrent requests cannot
// exceed the private-query quota or observe another owner's templates.
var SavedQueryMigrations = []storage.Migration{
	{ID: "011_saved_query_owners", SQL: `CREATE TABLE af_data_saved_query_owners (
 tenant_id VARBINARY(128) NOT NULL,
 user_id VARBINARY(255) NOT NULL,
 PRIMARY KEY(tenant_id,user_id)
) ENGINE=InnoDB`},
	{ID: "012_saved_queries", SQL: `CREATE TABLE af_data_saved_queries (
 tenant_id VARBINARY(128) NOT NULL,
 user_id VARBINARY(255) NOT NULL,
 id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 name VARCHAR(128) NOT NULL,
 name_key VARBINARY(512) NOT NULL,
 template_json MEDIUMTEXT NOT NULL,
 etag CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 created_at DATETIME(6) NOT NULL,
 updated_at DATETIME(6) NOT NULL,
 PRIMARY KEY(tenant_id,user_id,id),
 UNIQUE KEY saved_query_name(tenant_id,user_id,name_key)
) ENGINE=InnoDB`},
}
