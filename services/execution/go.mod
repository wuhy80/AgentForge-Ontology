module github.com/wuhy80/AgentForge-Ontology/services/execution

go 1.26.0

toolchain go1.26.10

require (
	github.com/go-sql-driver/mysql v1.7.1
	github.com/wuhy80/AgentForge-Ontology/contracts v0.0.0
	github.com/wuhy80/AgentForge-Ontology/libs/platform v0.0.0
	github.com/wuhy80/AgentForge-Ontology/libs/storage v0.0.0
)

require (
	github.com/jinzhu/inflection v1.0.0 // indirect
	github.com/jinzhu/now v1.1.5 // indirect
	golang.org/x/text v0.43.0 // indirect
	gorm.io/driver/mysql v1.5.7 // indirect
	gorm.io/gorm v1.25.12 // indirect
)

replace github.com/wuhy80/AgentForge-Ontology/contracts => ../../contracts

replace github.com/wuhy80/AgentForge-Ontology/libs/platform => ../../libs/platform

replace github.com/wuhy80/AgentForge-Ontology/libs/storage => ../../libs/storage
