# AgentForge Ontology Runtime

> 为 AI Agent 提供统一工业对象、关系、数据与动作模型的开源 Industrial Ontology Runtime。

AgentForge 不要求 Agent 理解数据库、表、HTTP API、MQTT Topic 或 PLC 协议。它通过稳定的语义对象接口，将异构工业数据源映射成可发现、可查询、可关联、可执行的 `Object`。

## 当前可运行能力

本仓库已经包含首个 Go Runtime 纵向切片：

- `Workspace → Vendor → Domain → ObjectType` 核心模型；
- Property、Enum、Link、Function、Action 和 DataBinding 定义；
- Draft 校验、编译、内容哈希及不可变 Published Snapshot；
- DataSource Driver 注册机制，以及可运行示例使用的 Memory Driver；
- Virtual Object 查询、逻辑字段到物理字段映射和过滤下推；
- Field Link Resolver 和批量目标查询，避免逐条 N+1 查询；
- 基于名称、别名、标签和描述的 Ontology Search；
- 面向 Agent 的精简 REST API。

当前版本是 MVP 内存实现，发布记录、权限、审计和 Runtime Cache 尚未持久化。Function/Action 已进入 Schema，但执行 Provider 尚未开放，防止在权限与审计能力完成前产生真实工业副作用。

## 架构

```text
AI Agent / Workflow
        │  REST（后续增加 MCP / SDK）
        ▼
Ontology Runtime
  Schema · Search · Query · Link
        │
Published Snapshot（不可变）
        │
DataBinding + Driver Registry
        │
MySQL · PostgreSQL · Redis · InfluxDB · HTTP · ...
```

Ontology Schema 与业务实例数据严格分离。Schema 负责描述对象是什么、属性含义、数据位置、关联和可执行能力；Runtime 在查询时通过 DataBinding 访问实际数据。MVP 默认采用 `virtual` 模式，不复制业务实例。

## 快速开始

要求 Go 1.23 或更高版本。

```bash
go run ./cmd/server
```

另开终端发布演示 Ontology：

```bash
curl -sS -X POST http://localhost:8080/v1/ontologies/publish \
  -H 'Content-Type: application/json' \
  --data-binary @examples/shipyard.json
```

搜索 Agent 不熟悉的工业概念：

```bash
curl -sS -X POST http://localhost:8080/v1/ontology/search \
  -H 'Content-Type: application/json' \
  -d '{"workspace":"shipyard-a","query":"主冷却水泵"}'
```

查询温度超过 80°C 的泵。Runtime 会把语义属性 `temperature` 转换成数据源字段并把过滤条件下推给 Driver：

```bash
curl -sS -X POST http://localhost:8080/v1/objects/query \
  -H 'Content-Type: application/json' \
  -d '{
    "workspace":"shipyard-a",
    "object":"jiangnan.equipment.Pump",
    "fields":["id","name","temperature"],
    "filters":[{"property":"temperature","operator":"gt","value":80}],
    "limit":100
  }'
```

通过 `drivenBy` 语义 Link 查询泵的驱动电机：

```bash
curl -sS -X POST http://localhost:8080/v1/objects/related \
  -H 'Content-Type: application/json' \
  -d '{
    "workspace":"shipyard-a",
    "object":"jiangnan.equipment.Pump",
    "id":"P001",
    "link":"drivenBy"
  }'
```

## REST Agent API

| API | 用途 |
| --- | --- |
| `POST /v1/ontologies/publish` | 校验 Draft，编译并原子激活 Published Snapshot |
| `POST /v1/ontology/search` | 按自然语言名称、别名、描述或标签发现 Schema |
| `POST /v1/ontology/describe` | 查看 Object 的 Property、Link 与 Action |
| `POST /v1/objects/get` | 按 Object 主键获取实例 |
| `POST /v1/objects/query` | 使用语义属性过滤、投影和限制实例 |
| `POST /v1/objects/related` | 通过 Link Resolver 获取关联对象 |
| `GET /healthz` | 服务存活检查 |

详细产品边界、模型决策和后续路线见[开发规划](docs/industrial-agent-ontology-plan.md)。
