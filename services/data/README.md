# Data 服务

当前实现首个 MySQL 后端闭环：`schema_2_db`、`single`、有类型实体表、结构 SQL 预览、受控发布、单条 CRUD 和单 Schema QuerySpec。服务通过 Metadata HTTP 接口读取权威当前定义，不读取 Metadata 私有表。完整设计仍见[数据读写](../../docs/数据读写.md)、[DataSet](../../docs/数据查询/DataSet.md)和[Schema](../../docs/模块/Schema.md)。

## 运行与迁移

本模块是独立 Go module；在 `GOWORK=off` 下构建。配置环境变量后，先显式执行平台迁移，再启动服务：

```powershell
$env:GOWORK = 'off'
go run ./cmd/server -migrate
go run ./cmd/server
```

所需配置为 `ONTOLOGY_MYSQL_DSN`、`ONTOLOGY_METADATA_URL`、`ONTOLOGY_OPA_URL`、`ONTOLOGY_AUTH_SECRET`、`ONTOLOGY_SERVICE_TOKEN`、`ONTOLOGY_CURSOR_SECRET`；OPA 开启认证时还需 `ONTOLOGY_OPA_TOKEN`。三个本服务密钥须各不相同且至少 32 字节。默认仅监听 `127.0.0.1:18081`，可用 `ONTOLOGY_LISTEN` 配置。服务启动不会自动迁移数据库；也没有绕过 OPA 的允许全部模式。

MySQL 需要支持 `utf8mb4_0900_bin` 排序规则及 16KiB 页的 InnoDB；当前集成验证使用 MySQL 26.7.0。实体表显式使用 `ROW_FORMAT=DYNAMIC`，UUID 列使用 ASCII，租户键使用二进制类型，物理列由稳定 field ID 派生，业务名称不进入 SQL 标识符。建模预检限制最多 100 个属性、32 个索引，保守核算 60000 字节逻辑行及 7000 字节页内预算，不支持的页大小在关闭 gate 前拒绝。表内值保留真实类型，`int64`、`decimal` 在 JSON 中使用字符串，时间使用带时区的 RFC 3339 字符串并精确到微秒；超出范围或精度会拒绝。

## 已实现接口

| 接口 | 行为 |
| --- | --- |
| `GET /healthz`、`GET /readyz` | 进程及数据库连接探针 |
| `GET /data/capabilities` | 返回当前实际开放能力及限制 |
| `POST /data/schemas/{schemaId}/instances` | 显式创建；必须提交幂等键 |
| `GET /data/schemas/{schemaId}/instances/{entityId}` | 按当前行列授权读取 |
| `PATCH`、`DELETE /data/schemas/{schemaId}/instances/{entityId}` | 必须携带最近读取结果的 `If-Match` |
| `POST /data/query` | 中文名称或稳定 ID 绑定、类型化筛选、投影、排序和签名游标 |
| `POST /internal/publications/preview` | 只读结构预览；正文为 `contracts.PrepareRequest` |
| `POST /internal/publications/prepare` | 持久关闭受影响 Schema 入口、等待已入场事务排空、执行并核验物理计划 |
| `POST /internal/publications/activate` | 核验 Metadata 权威当前摘要后恢复访问 |

内部发布接口只接受服务 Bearer token 和 `X-Tenant-ID`，普通接口使用经验证的用户身份；不能用外部请求的租户头替代可信身份。

发布失败后保留暂停状态，由 Metadata 重试同一个操作恢复。初次建表核验列、类型、可空性、排序规则及完整索引；不接管未纳管同名表。当前再次发布只支持保持存储签名不变的展示名称和描述等修改，不提供自动 `ALTER` 或破坏性迁移。预检拒绝不暂停现有映射；持久暂停后的失败不自动恢复流量。

## 正确性与限制

Data 在同一事务内提交实例、创建幂等记录和 outbox，并在更新及删除时检查当前 ETag。幂等重试返回原创建结果，读取仍检查当前权限；同键不同意图拒绝。创建、修改同时检查可写字段和新行条件，修改还检查旧行条件。查询权限在 SQL 中先于排序和分页执行，不通过读取一页后删行实现行过滤。

请求开始与本地事务提交前分别查询当前 OPA 决策，决策变化会回滚。本实现没有完成跨服务原子撤权协议，不能据此承诺撤权与正在提交的写入严格串行。Schema gate 通过本地事务行锁保证发布和 Data 请求的入场顺序；当前每个 Schema 的操作会串行竞争该锁，吞吐尚未测量。

QuerySpec 目前每页为 1 至 100 条、最多一个业务排序字段并以实例 ID 决胜；表达式有深度、节点和集合预算。支持 AND/OR/NOT、等值及比较、显式空值、有限 IN/NOT IN、字面前缀；NULL 保留 SQL 三值逻辑。decimal 条件和游标参数显式转换为对应精度，防止 MySQL 将字符串比较转为 DOUBLE 后丢失大数精度。属性名称按 Metadata 相同的 NFC 和大小写折叠规则绑定，属性名称不能覆盖另一属性 ID。游标绑定主体、租户、定义摘要、完整查询和授权决策，并在一小时后过期。跨页期间业务数据变动按每次新执行可见状态处理，不提供跨请求历史快照。

`db_2_schema`、分表、计算属性、Enum 属性的跨定义协调、Link 实例及多跳、批量事务、OntologySQL、聚合和前端尚未开放。服务对无法表达的请求直接拒绝，`capabilities` 不将五类元数据可维护等同于这些运行能力已实现。outbox 和幂等记录目前不自动清理；保留期限、事件投递、全量恢复和生产容量仍待后续实现及验证。

## 验证

```powershell
go test ./...
```

设置 `MYSQL_TEST_DSN` 后，相同命令还会执行真实 MySQL 集成用例，未设置时明确跳过该用例。集成测试使用随机租户及 Schema，不删除或清空共享平台表；验证预览无写入、发布门禁、条件授权、幂等、唯一冲突、并发凭证、NULL/NOT、事务及 outbox 回滚和改名后的当前映射。单元测试覆盖注入参数、隐藏字段、精度边界、游标篡改及上下文隔离。检查通过不代表已完成三库认证、跨服务原子撤权或性能验收。
