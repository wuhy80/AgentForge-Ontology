# Data 服务

平台存储当前使用 MySQL。无 `storageBinding` 的默认 Schema 保留有类型实体表、受控发布、单条 CRUD 和单 Schema QuerySpec；显式绑定的数据源增加 `schema_2_db` / `db_2_schema`、MySQL/PostgreSQL 连接目录及按唯一键聚合的跨来源属性投影。PostgreSQL 当前仅作为外部数据源，不表示平台元数据或控制表已适配 PostgreSQL。服务通过 Metadata HTTP 接口核对会话、获取当前授权及读取权威当前定义，不读取 Metadata 私有表。本地账号和会话协议见 [Metadata identity](../metadata/identity/README.md)，公共权限规则见[权限设计](../../docs/权限/权限设计.md)；连接配置见[数据源](../../docs/数据源.md)，建模及映射规则以[Schema](../../docs/模块/Schema.md)为主要说明，查询规则仍见[数据读写](../../docs/数据读写.md)及[DataSet](../../docs/数据查询/DataSet.md)。

## 运行与迁移

本模块是独立 Go module；在 `GOWORK=off` 下构建。配置环境变量后，先显式执行平台迁移，再启动服务：

```powershell
$env:GOWORK = 'off'
go run ./cmd/server -migrate
go run ./cmd/server
```

所需配置为 `ONTOLOGY_MYSQL_DSN`、`ONTOLOGY_METADATA_URL`、`ONTOLOGY_SERVICE_TOKEN`、`ONTOLOGY_CURSOR_SECRET`；服务及游标密钥须各不相同且至少32字节。Data 通过 Metadata 的内部身份及决策接口使用当前权限，不需要用户签名密钥或直接配置 OPA；OPA 地址及可选凭据由 Metadata 持有。默认仅监听 `127.0.0.1:18081`，可用 `ONTOLOGY_LISTEN` 配置。服务启动不会自动迁移数据库；也没有绕过当前授权的允许全部模式。

使用显式数据源时另配置独立且至少32字节的 `ONTOLOGY_DATA_SOURCE_KEY`，用于绑定租户及源身份的 AES-GCM 凭据加密。密码只写入，更新省略密码保留原值，显式空字符串清空，响应只返回 `passwordSet`。缺少密钥不影响旧默认存储启动，但源配置及解密明确拒绝。新增平台迁移也必须显式执行，普通请求与服务启动不建平台表；保存数据源或预览不执行目标 DDL。

MySQL 需要支持 `utf8mb4_0900_bin` 排序规则及 16KiB 页的 InnoDB；当前集成验证使用 MySQL 26.7.0。实体表显式使用 `ROW_FORMAT=DYNAMIC`，UUID 列使用 ASCII，租户键使用二进制类型，物理列由稳定 field ID 派生，业务名称不进入 SQL 标识符。建模预检限制最多 100 个属性、32 个索引，保守核算 60000 字节逻辑行及 7000 字节页内预算，不支持的页大小在关闭 gate 前拒绝。表内值保留真实类型，`int64`、`decimal` 在 JSON 中使用字符串，时间使用带时区的 RFC 3339 字符串并精确到微秒；超出范围或精度会拒绝。

当前字段类型为 `string`、`text`、`longtext`、`uuid`、`int64`、`decimal`、`boolean`、`timestamp`。`string` 最多 255 个字符；`text` 保持最多 65535 个 UTF-8 字节；独立的 `longtext` 映射 MySQL `LONGTEXT`，单字段最多 4 MiB（4194304 个解码后的 UTF-8 字节）。两种文本均保留换行、空字符串及显式允许的 `null`，超限拒绝，不截断或自动转换。`text`、`longtext` 不支持索引和排序；可投影及使用类型化比较、集合、空值和字面前缀筛选，不提供全文搜索。已有 `text` 改为 `longtext` 属于结构变更，当前不提供自动扩列迁移。

Data 的 JSON 请求体最多 8 MiB（8388608 字节），包括字段名、JSON 转义及其他字段，超限返回 HTTP 413 / `request_too_large`。单字段预算与整份请求预算同时生效；多字段或大量转义可能先触发请求预算。`GET /data/capabilities` 返回 `dataTypes`、`maxTextBytes`、`maxLongTextBytes` 和 `maxRequestBytes`。Metadata 定义及身份管理请求仍使用各自的 1 MiB 请求预算，不能把长正文作为大默认值或大行条件绕过管理接口预算。

## 已实现接口

| 接口 | 行为 |
| --- | --- |
| `GET /healthz`、`GET /readyz` | 进程及数据库连接探针 |
| `GET /data/capabilities` | 返回当前实际开放能力及限制 |
| `GET/POST /data/sources`、`GET/PUT /data/sources/{id}` | 管理员维护租户数据源；PUT 必须强 If-Match；`view=select` 提供模型作者可选摘要 |
| `POST /data/sources/test`、`POST /data/sources/{id}/test` | 管理员测试未保存参数或已保存的加密凭据 |
| `GET /data/sources/{id}/tables`、`GET /data/sources/{id}/tables/{table}` | 只读表、列及索引目录；可选 namespace 参数 |
| `POST /data/schemas/{schemaId}/instances` | 显式创建；必须提交幂等键 |
| `GET /data/schemas/{schemaId}/instances/{entityId}` | 按当前行列授权读取 |
| `PATCH`、`DELETE /data/schemas/{schemaId}/instances/{entityId}` | 必须携带最近读取结果的 `If-Match` |
| `POST /data/query` | 中文名称或稳定 ID 绑定、类型化筛选、投影、排序和签名游标 |
| `POST /internal/publications/preview` | 只读结构预览；正文为 `contracts.PrepareRequest` |
| `POST /internal/publications/prepare` | 持久关闭受影响 Schema 入口、等待已入场事务排空、执行并核验物理计划 |
| `POST /internal/publications/activate` | 核验 Metadata 权威当前摘要后恢复访问 |

内部发布接口只接受服务 Bearer token 和 `X-Tenant-ID`。普通接口使用用户在工作台账号密码登录后取得的 Cookie 会话，Data 每次通过 Metadata 核对会话、当前账号和租户；所有非安全方法请求，包括 POST 查询，还要求同源 `Origin` 与匹配的 `X-CSRF-Token`。不能用外部请求的租户头或手工 Bearer 凭证替代用户会话。

发布失败后保留暂停状态，由 Metadata 重试同一个操作恢复。初次建表核验列、类型、可空性及完整索引；不接管未纳管同名表。默认无 binding 的已有模型仍只支持保持存储签名不变的展示修改；显式正向映射支持认证范围内追加可空列及索引，经同一计划编译器预览、执行和核验。反向映射只读取并验证已有结构，不对源表执行 DDL。预检拒绝不暂停现有映射；持久暂停后的失败不自动恢复流量。详细约束统一见 [Schema](../../docs/模块/Schema.md#2-建模方向与预览)。

## 显式来源映射

数据源由当前权威会话中的租户管理员维护；模型作者读取可选源及表结构使用现有 `definition.edit` 授权，源目录权限不替代实例行列授权。物理表名、列名来自已校验绑定与目录，密码不进入模型正文。数据源的共享租户列与专属数据库规则见[数据源配置](../../docs/数据源.md)。

当前一个 Schema 最多映射8张来源表。主表提供稳定标识，关联来源按同类型唯一键匹配，每个主对象每来源最多一行，没有关联行的属性返回 `null`。跨来源属性可以投影；筛选、排序及权限行条件仅支持主表属性，涉及其他来源的条件明确拒绝。查询逐源读取，不承诺跨数据库的一致快照或事务。显式来源的实例创建、修改和删除当前拒绝，避免把默认存储的幂等/outbox事务保证套用到多个数据库。

能力响应中 `operations` 表示默认存储；`mappedOperations` 为 `read/query`，`sourceDrivers` 为 `mysql/postgresql`，`mappedUpdates` 为 `nullable-fields-and-indexes`。`crossSourceProjection` 为真，`crossSourceConditions` 与 `crossSourceTransactions` 为假；客户端应结合 Schema 是否具有 `storageBinding` 决定可用操作。

## 正确性与限制

Data 在同一事务内提交实例、创建幂等记录和 outbox，并在更新及删除时检查当前 ETag。幂等重试返回原创建结果，读取仍检查当前权限；同键不同意图拒绝。创建、修改同时检查可写字段和新行条件，修改还检查旧行条件。查询权限在 SQL 中先于排序和分页执行，不通过读取一页后删行实现行过滤。

请求开始与本地事务提交前分别通过 Metadata 请求当前授权决策，由其读取当前用户、角色和已生效策略后交给 OPA 求值；不复用旧允许，决策变化会回滚。单条读取使用 `instance.read`，集合查询使用独立的 `instance.query`，创建、修改及删除分别使用对应操作授权。本实现没有完成跨服务原子撤权协议，不能据此承诺撤权与正在提交的写入严格串行。Schema gate 通过本地事务行锁保证发布和 Data 请求的入场顺序；当前每个 Schema 的操作会串行竞争该锁，吞吐尚未测量。

QuerySpec 目前每页为 1 至 100 条、最多一个业务排序字段并以实例 ID 决胜；表达式有深度、节点和集合预算。支持 AND/OR/NOT、等值及比较、显式空值、有限 IN/NOT IN、字面前缀；NULL 保留 SQL 三值逻辑。decimal 条件和游标参数显式转换为对应精度，防止 MySQL 将字符串比较转为 DOUBLE 后丢失大数精度。属性名称按 Metadata 相同的 NFC 和大小写折叠规则绑定，属性名称不能覆盖另一属性 ID。游标绑定主体、租户、定义摘要、完整查询和授权决策，并在一小时后过期。跨页期间业务数据变动按每次新执行可见状态处理，不提供跨请求历史快照。

分表、计算属性、Enum 属性的跨定义协调、Link 实例及多跳、批量事务、OntologySQL、聚合及任意联邦条件尚未开放。权限当前支持受控行条件、静态字段集合及可安全合并的角色规则，不能安全合并的组合明确拒绝；不代表完整 ABAC、逐行掩码或 Action/Function 运行授权已经实现。前端已在 [apps/web](../../apps/web/README.md) 接入现有 CRUD 与单 Schema 查询，未增加上述运行能力。服务对无法表达的请求直接拒绝，`capabilities` 不将五类元数据可维护等同于这些运行能力已实现。outbox 和幂等记录目前不自动清理；保留期限、事件投递、全量恢复和生产容量仍待后续实现及验证。

## 验证

```powershell
go test ./...
```

设置 `MYSQL_TEST_DSN` 后，相同命令还会执行真实 MySQL 集成用例，未设置时明确跳过该用例。集成测试使用随机租户及 Schema，不删除或清空共享平台表；验证预览无写入、发布门禁、条件授权、幂等、唯一冲突、并发凭证、NULL/NOT、事务及 outbox 回滚和改名后的当前映射。单元测试覆盖注入参数、隐藏字段、精度边界、游标篡改及上下文隔离。检查通过不代表已完成三库认证、跨服务原子撤权或性能验收。

`longtext` 单元回归覆盖多字节边界、4 MiB 正文的数据库值编解码、原 `text` 65535 字节边界、空值/空字符串、8 MiB 请求及尾部空白超限、DDL 映射、索引/排序拒绝和能力声明。2026-11-07 的模块测试通过，未启用数据库环境变量；`TestMySQLLongTextRoundTripAtApplicationLimit` 为待显式运行的真实 MySQL 建列、物理核验及 4 MiB 往返测试。长正文响应容量和性能仍未验收，查询应按需投影正文并选用较小分页。

2026-11-07新增数据源配置、AES-GCM身份绑定、授权拒绝及物理标识边界测试。已使用真实MySQL随机租户/随机表验证配置加密、HTTP连接与表列发现、主键和唯一/复合索引、跨租户隔离与专属目标并发冲突、ETag并发返回200/412、引用保护及停用拒绝；没有修改现有知识库定义或实例。`TestSourceCatalogRealDrivers` 与 `TestSourceHTTPConnectionAndCatalogRealDrivers` 可另外通过 `POSTGRES_SOURCE_TEST_DSN` 连接外部PostgreSQL验证，未设置时明确跳过。外部来源映射的结构执行和跨源读取结果仍须按实际运行的独立用例记录，不由连接测试代替。
