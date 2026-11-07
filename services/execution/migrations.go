package execution

import "github.com/wuhy80/AgentForge-Ontology/libs/storage"

// Only an explicit -migrate command or an opted-in integration test applies
// this service's tables. Normal requests and startup never create tables.
var Migrations = []storage.Migration{
	{ID: "001_execution_invocations", SQL: `CREATE TABLE af_execution_invocations (
		tenant_id varbinary(128) NOT NULL,
		subject_id varbinary(255) NOT NULL,
		request_key varbinary(128) NOT NULL,
		request_hash char(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
		id char(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
		status varchar(24) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
		deadline_at datetime(6) NOT NULL,
		record_json longtext NOT NULL,
		PRIMARY KEY (tenant_id,subject_id,request_key),
		UNIQUE KEY invocation_id (tenant_id,id)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_bin`},
}
