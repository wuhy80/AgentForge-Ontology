package metadata

import "github.com/wuhy80/AgentForge-Ontology/libs/storage"

// Migrations are only run by the explicit migrate command.
var Migrations = []storage.Migration{
	{ID: "001_organization", SQL: `CREATE TABLE af_meta_organizations (
 id VARCHAR(36) NOT NULL PRIMARY KEY,
 tenant_id VARBINARY(128) NOT NULL,
 kind VARCHAR(16) NOT NULL,
 parent_id VARCHAR(36) NOT NULL DEFAULT '',
 name VARCHAR(128) NOT NULL,
 name_key VARBINARY(512) NOT NULL,
 UNIQUE KEY uq_organization_name (tenant_id,kind,parent_id,name_key),
 KEY ix_organization_tenant (tenant_id,kind,parent_id)
) ENGINE=InnoDB`},
	{ID: "002_definitions", SQL: `CREATE TABLE af_meta_definitions (
 id VARCHAR(36) NOT NULL PRIMARY KEY,
 tenant_id VARBINARY(128) NOT NULL,
 category_id VARCHAR(36) NOT NULL,
 group_id VARCHAR(36) NOT NULL,
 kind VARCHAR(16) NOT NULL,
 name VARCHAR(128) NOT NULL,
 name_key VARBINARY(512) NOT NULL,
 current_json LONGTEXT NULL,
 current_digest CHAR(64) NOT NULL DEFAULT '',
 draft_json LONGTEXT NULL,
 draft_digest CHAR(64) NOT NULL DEFAULT '',
 status VARCHAR(16) NOT NULL DEFAULT 'draft',
 UNIQUE KEY uq_definition_name (tenant_id,category_id,name_key),
 KEY ix_definition_tenant (tenant_id,category_id,kind)
) ENGINE=InnoDB`},
	{ID: "003_publications", SQL: `CREATE TABLE af_meta_publications (
 id VARCHAR(36) NOT NULL PRIMARY KEY,
 tenant_id VARBINARY(128) NOT NULL,
 definition_id VARCHAR(36) NOT NULL,
 candidate_json LONGTEXT NOT NULL,
 candidate_digest CHAR(64) NOT NULL,
 previous_digest CHAR(64) NOT NULL,
 status VARCHAR(32) NOT NULL,
 error_text TEXT NOT NULL,
 UNIQUE KEY uq_publication_intent (tenant_id,definition_id,candidate_digest),
 KEY ix_publication_tenant (tenant_id,status)
) ENGINE=InnoDB`},
	{ID: "004_tenant_guard", SQL: `CREATE TABLE af_meta_tenant_guards (
 tenant_id VARBINARY(128) NOT NULL PRIMARY KEY,
 publication_id VARCHAR(36) NOT NULL DEFAULT ''
) ENGINE=InnoDB`},
	{ID: "005_dependencies", SQL: `CREATE TABLE af_meta_dependencies (
 tenant_id VARBINARY(128) NOT NULL,
 source_id VARCHAR(36) NOT NULL,
 target_id VARCHAR(36) NOT NULL,
 PRIMARY KEY (tenant_id,source_id,target_id),
 KEY ix_dependency_target (tenant_id,target_id)
) ENGINE=InnoDB`},
	{ID: "006_outbox", SQL: `CREATE TABLE af_meta_outbox (
 id VARCHAR(36) NOT NULL PRIMARY KEY,
 tenant_id VARBINARY(128) NOT NULL,
 event_type VARCHAR(64) NOT NULL,
 object_id VARCHAR(36) NOT NULL,
 payload LONGTEXT NOT NULL,
 created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 KEY ix_outbox_tenant (tenant_id,created_at)
) ENGINE=InnoDB`},
	{ID: "007_publication_intent_reuse", SQL: `ALTER TABLE af_meta_publications
 DROP INDEX uq_publication_intent,
 ADD INDEX ix_publication_intent (tenant_id,definition_id,candidate_digest)`},
}
