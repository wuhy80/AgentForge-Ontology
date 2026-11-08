module github.com/wuhy80/AgentForge-Ontology/services/mcp

go 1.25.0

require (
	github.com/modelcontextprotocol/go-sdk v1.8.0
	github.com/wuhy80/AgentForge-Ontology/contracts v0.0.0
	github.com/wuhy80/AgentForge-Ontology/libs/platform v0.0.0
)

require (
	github.com/google/jsonschema-go v0.4.3 // indirect
	github.com/segmentio/asm v1.1.3 // indirect
	github.com/segmentio/encoding v0.5.4 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	golang.org/x/oauth2 v0.35.0 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/sys v0.44.0 // indirect
	golang.org/x/time v0.15.0 // indirect
)

replace github.com/wuhy80/AgentForge-Ontology/contracts => ../../contracts

replace github.com/wuhy80/AgentForge-Ontology/libs/platform => ../../libs/platform
