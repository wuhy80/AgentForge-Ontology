# Industrial Ontology Runtime 开发规划

## 1. 产品定位

AgentForge 是**面向 AI Agent 的工业语义运行时（Industrial Ontology Runtime）**：把工业对象、实时数据、关系、无副作用函数和有副作用动作统一抽象为稳定、可发现、可查询、可执行的语义接口。

它不是 Ontology 绘图工具，也不是把 `mysql_query`、`redis_get` 等底层能力直接交给 Agent 的数据库 MCP Server。核心原则是：

> Agent 操作工业语义对象；Runtime 负责定位数据库、表、API、Topic 和协议。

例如 Agent 只表达“过去一小时温度超过 80°C 的冷却泵及其驱动电机”，Runtime 再将对象基本信息、时序温度和跨库关系分别下推到对应数据源，最后返回统一语义结果。

## 2. 四层架构

```text
┌─────────────────────────────────────────────┐
│ AI Agent / Workflow                         │
│ ChatGPT · Claude · 自研 Agent               │
└───────────────────┬─────────────────────────┘
                    │ MCP / REST API / SDK
┌───────────────────▼─────────────────────────┐
│ Ontology Runtime                            │
│ Schema · Search · Query · Link · Function   │
│ Action · Auth · Audit · File                │
└───────────────────┬─────────────────────────┘
                    │ immutable snapshot
┌───────────────────▼─────────────────────────┐
│ Ontology Model                              │
│ Workspace → Vendor → Domain → ObjectType    │
│ Object → Property · Link · Action · Binding │
│ Vendor → EnumType · Function                │
└───────────────────┬─────────────────────────┘
                    │ driver/provider ports
┌───────────────────▼─────────────────────────┐
│ MySQL · PostgreSQL · Redis · Influx · HTTP  │
└─────────────────────────────────────────────┘
```

### Schema 与数据分离

Ontology 只保存 `Pump` 的定义、属性含义、关联、数据绑定和动作能力。`PUMP-001` 的实时值可以继续存在 MySQL、Redis、InfluxDB、MES 或 PLC 数据平台中。

- **Virtual Object**：Runtime 实时读取源系统；MVP 只实现该模式。
- **Materialized Object**：通过 CDC/Sync 建立搜索和跨源查询索引；后续按真实需求实现。
- 文件和图片仅保存元数据引用，内容进入 S3/MinIO，不使用数据库 BLOB。

## 3. 核心模型

```text
Workspace
└── Vendor
    ├── EnumType
    ├── Function
    └── Domain
        └── ObjectType
            ├── Property
            ├── Link
            ├── Action
            ├── Tag (多对多标签，不是目录层级)
            └── DataBinding → DataSource
```

`Workspace` 用于项目、租户和权限隔离；`Vendor` 只表达 Ontology 分类，二者不能混用。Runtime 使用完整键 `vendor.domain.Object` 消除对象名称歧义。

### 3.1 ObjectType

Object 是系统中心，至少具有 `name`、`displayName`、`description`、`aliases`、`tags`、`properties`、`links`、`actions`、`dataBinding` 和 `storageMode`。其中 `description`、`aliases`、`examples` 是 Agent 理解和搜索 Schema 的重要信息，不是装饰字段。

### 3.2 Property

首批类型覆盖：

```text
string text int int64 float double decimal bool
 date datetime timestamp json enum image file object array
```

工业属性还支持单位、精度、最小/最大值、默认值、空值、只读、主键、描述和示例。`link` 不作为普通 Property 类型；关系由独立 Link 模型表达。

### 3.3 Link

Link 是一等公民，包含正向名称、反向名称、源/目标 Object、`1:1`、`1:N`、`N:1`、`N:M` 基数以及 Resolver。Resolver 负责跨数据源解析：

- `field`：源字段值匹配目标字段；MVP 已支持；
- `sql`：关系表或自定义 SQL；
- `function`：通过受控 Function 解析；
- 后续 Resolver 必须提供批量接口，禁止产生 N+1 查询。

### 3.4 DataBinding 与 DataSource Driver

DataBinding 将逻辑属性映射到具体资源字段，Runtime 把过滤、投影、排序和限制下推到 Driver，禁止先 `SELECT *` 再由 Go 过滤。

Driver 是插件端口，负责连接测试、能力声明、Schema 探测和查询。数据库小版本由一个 Driver 探测版本并协商能力，不为 MySQL 5.7/8.0/8.4 分别实现 Driver。InfluxDB 1.x 与 2.x 因查询模型差异较大，分别提供 Driver，但在 Runtime 上层统一为 `TimeSeriesQuery`。

建议扩展顺序：MySQL、PostgreSQL、Redis、InfluxDB 1.x、InfluxDB 2.x、HTTP；随后由社区扩展 Oracle、SQL Server、ClickHouse、Doris、StarRocks、TDengine 等。

### 3.5 Function 与 Action

- **Function**：读取、计算或转换信息，不产生业务副作用。
- **Action**：改变真实系统状态，具有业务副作用。

两者都通过 Provider 绑定实现，不直接绑定 Go 代码。Function Provider 可包含 Expression、SQL、HTTP、Plugin；Action Provider 可包含 HTTP、SQL、MQTT、Plugin，后续增加 OPC UA、Modbus、Kafka、NATS、IEC 104。

Action 从第一版模型开始保留权限、参数校验、超时和幂等信息；执行能力必须等审计与授权完成后才开放，并逐步加入审批、dry-run、联锁和紧急停止。

## 4. Draft、发布与 Runtime Cache

产品界面只暴露 `Draft` 与 `Published` 两种状态，后台仍保存 `publication_id`、单调递增 `revision` 和内容 `hash`：

```text
Draft → Validate → Compile → Immutable Published Snapshot → Runtime Cache
```

运行请求从开始到结束固定使用同一个 Snapshot，避免查询过程中 Schema 被管理员修改。发布先检查标识、重复对象、Enum 引用、Link 目标、属性约束、DataSource、字段映射和主键，再编译成适合运行时直接索引的 Map。API 节点在本地内存读取 Snapshot，后续通过 PostgreSQL 持久化、Redis 通知各节点原子换代。

## 5. Agent API

Agent 只需要少量高层工具：

```text
search_ontology
describe_object
query_objects
get_object
get_related
call_function
execute_action
```

REST 与 MCP 必须复用同一 Application Service，不能形成两套业务逻辑。首个 Go 切片已提供前五项对应的 REST 能力；MCP、Function 和 Action 执行按安全依赖顺序补充。

权限至少覆盖 Workspace、Vendor、Domain、Object、Property 和 Action，并严格区分 Object 读取权与 Action 执行权。每次 Action 必须审计操作者、Agent、对象类型、实例、动作、参数、结果、耗时和关联 Snapshot。

## 6. Go 模块划分

```text
cmd/
├── server
└── cli
internal/
├── workspace
├── ontology             # Object/Property/Enum/Link/Function/Action
├── datasource           # Driver registry 与各数据源驱动
├── binding              # Schema 到数据源的映射
├── publish              # validate/compile/snapshot
├── runtime              # schema cache 与对象执行入口
├── query                # 语义查询编译、pushdown
├── search               # 本体搜索
├── executor             # Function/Action Provider
├── file                 # S3-compatible 文件元数据
├── auth                 # RBAC/ABAC
├── audit
└── agent
    ├── rest
    └── mcp
```

MVP 保持模块化单体和无状态 API；PostgreSQL 保存 Ontology 元数据、用户、权限、发布与审计，Redis 用于缓存/发布通知，S3/MinIO 保存文件。只有在独立扩缩容指标出现后才拆微服务。

## 7. 迭代计划与退出标准

### 阶段 1：Ontology Core

- Workspace、Vendor、Domain、Tag、Object、Property、Enum 和 Link；
- Draft、校验、编译、不可变发布快照；
- JSON/YAML Import/Export，使 Ontology 可由 Git 和 CI 管理。

**退出标准**：示例 Ontology 可重复导入、拒绝非法引用，并生成确定性 Snapshot hash。

### 阶段 2：DataSource

- Driver Registry、连接测试、能力协商与 Schema Introspection；
- MySQL、PostgreSQL、Redis、InfluxDB 1.x/2.x；
- Virtual DataBinding 和凭据安全管理。

**退出标准**：同一个 Object 可以映射不同 Driver，Agent 请求不出现物理表字段或凭据。

### 阶段 3：Runtime

- `get_object`、`query_objects`、`get_related`、`describe_object` 和 Ontology Search；
- Query Pushdown、Batch Link Resolver、限制/超时和本地 Snapshot Cache；
- REST API、稳定错误码、OpenAPI 和集成测试。

**退出标准**：完成“高温冷却泵 → 驱动电机”的跨对象语义查询且无 N+1。

### 阶段 4：Agent

- MCP Server 与稳定 Tool Schema；
- Full Text Search，后续按评测结果增加 Embedding/pgvector；
- Workspace 隔离、Property 级读取权限、调用限额与完整追踪。

**退出标准**：未知对象名称的 Agent 能先搜索再查询，且无法越权读取属性。

### 阶段 5：Function / Action

- Function 的 Expression/SQL/HTTP/Plugin Provider；
- Action 的 HTTP/SQL/MQTT/Plugin Provider；
- 参数约束、权限、审计、超时、幂等，随后增加审批和 dry-run。

**退出标准**：任何副作用调用都可授权、可拒绝、可追溯、可安全重试。

## 8. 明确延期的能力

第一版不实现 CDC、知识推理、规则引擎、事件系统、复杂工作流、全量实例向量索引、图数据库、数字孪生和 AI 自动生成 Ontology。这些功能只有在 Object、Link、DataBinding、Runtime Query 和 Agent API 的核心闭环通过真实工业数据验证后再排期。

## 9. 当前实现状态

本次首个纵向切片已落地：核心 Go Schema、发布校验与 Snapshot 编译、Driver Registry、Virtual Object 查询、字段映射、过滤下推、Field Link Resolver、Ontology Search、REST API 和可执行造船厂示例。

下一步应优先完成 PostgreSQL 元数据仓库、Draft CRUD/YAML、MySQL/PostgreSQL Driver、稳定 API 错误模型和 OpenAPI；之后才开发 MCP 与可产生副作用的 Action Executor。
