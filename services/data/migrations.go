package data

import "github.com/wuhy80/AgentForge-Ontology/libs/storage"

var Migrations = []storage.Migration{
	{ID: "001_data_gates", SQL: `CREATE TABLE af_data_gates (tenant_id varbinary(128) NOT NULL, definition_id varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL, digest char(64) NOT NULL, previous_digest varchar(64) NOT NULL, operation_id varchar(64) NOT NULL, status varchar(20) NOT NULL, definition longtext NOT NULL, storage_signature char(64) NOT NULL, PRIMARY KEY(tenant_id,definition_id), UNIQUE KEY operation_id(operation_id)) ENGINE=InnoDB`},
	{ID: "002_data_idempotency", SQL: `CREATE TABLE af_data_idempotency (tenant_id varbinary(128) NOT NULL, schema_id varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL, subject_id varbinary(255) NOT NULL, request_key varbinary(128) NOT NULL, request_hash char(64) NOT NULL, entity_id char(36) NOT NULL, response longtext NOT NULL, PRIMARY KEY(tenant_id,schema_id,subject_id,request_key)) ENGINE=InnoDB`},
	{ID: "003_data_outbox", SQL: `CREATE TABLE af_data_outbox (id char(36) NOT NULL PRIMARY KEY, tenant_id varbinary(128) NOT NULL, schema_id varchar(64) NOT NULL, entity_id char(36) NOT NULL, event_type varchar(40) NOT NULL, payload longtext NOT NULL, created_at datetime(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)) ENGINE=InnoDB`},
}
