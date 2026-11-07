module github.com/wuhy80/AgentForge-Ontology/tests/integration

go 1.22.0

require (
	github.com/wuhy80/AgentForge-Ontology/contracts v0.0.0
	github.com/wuhy80/AgentForge-Ontology/libs/platform v0.0.0
	github.com/wuhy80/AgentForge-Ontology/libs/storage v0.0.0
	github.com/wuhy80/AgentForge-Ontology/services/data v0.0.0
	github.com/wuhy80/AgentForge-Ontology/services/metadata v0.0.0
)

require (
	github.com/go-sql-driver/mysql v1.7.1 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20221227161230-091c0ba34f0a // indirect
	github.com/jackc/pgx/v5 v5.5.5 // indirect
	github.com/jackc/puddle/v2 v2.2.1 // indirect
	github.com/jinzhu/inflection v1.0.0 // indirect
	github.com/jinzhu/now v1.1.5 // indirect
	golang.org/x/crypto v0.33.0 // indirect
	golang.org/x/sync v0.11.0 // indirect
	golang.org/x/text v0.22.0 // indirect
	gorm.io/driver/mysql v1.5.7 // indirect
	gorm.io/gorm v1.25.12 // indirect
)

replace github.com/wuhy80/AgentForge-Ontology/contracts => ../../contracts

replace github.com/wuhy80/AgentForge-Ontology/libs/platform => ../../libs/platform

replace github.com/wuhy80/AgentForge-Ontology/libs/storage => ../../libs/storage

replace github.com/wuhy80/AgentForge-Ontology/services/metadata => ../../services/metadata

replace github.com/wuhy80/AgentForge-Ontology/services/data => ../../services/data
