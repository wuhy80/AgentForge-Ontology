# Data 服务

平台存储当前使用 MySQL。无 `storageBinding` 的默认 Schema 保留有类型实体表、受控发布、八种内置写操作和单Schema QuerySpec；显式绑定的数据源增加 `schema_2_db` / `db_2_schema`、MySQL/PostgreSQL 连接目录及按唯一键聚合的跨来源属性投影。PostgreSQL 当前仅作为外部数据源，不表示平台元数据或控制表已适配 PostgreSQL。服务通过 Metadata HTTP 接口核对会话、获取当前授权及读取权威当前定义，不读取 Metadata 私有表。本地账号和会话协议见 [Metadata identity](../metadata/identity/README.md)，公共权限规则见[权限设计](../../docs/权限/权限设计.md)；连接配置见[数据源](../../docs/数据源.md)，建模及映射规则以[Schema](../../docs/模块/Schema.md)为主要说明，查询规则仍见[数据读写](../../docs/数据读写.md)及[DataSet](../../docs/数据查询/DataSet.md)。

## 运行与迁移

本模块是独立 Go module；在 `GOWORK=off` 下构建。配置环境变量后，先显式执行平台迁移，再启动服务：

```powershell
$env:GOWORK = 'off'
go run ./cmd/server -migrate
go run ./cmd/server
```

所需配置为 `ONTOLOGY_MYSQL_DSN`、`ONTOLOGY_METADATA_URL`、`ONTOLOGY_SERVICE_TOKEN`、`ONTOLOGY_CURSOR_SECRET`；服务及游标密钥须各不相同且至少32字节。Data 通过 Metadata 的内部身份及决策接口使用当前权限，不需要用户签名密钥或直接配置 OPA；OPA 地址及可选凭据由 Metadata 持有。默认仅监听 `127.0.0.1:18081`，可用 `ONTOLOGY_LISTEN` 配置。服务启动不会自动迁移数据库；也没有绕过当前授权的允许全部模式。

经HTTPS反向代理部署时可设置 `ONTOLOGY_TRUSTED_PROXY_CIDRS`，默认不信任转发头；实际对端、Host、Origin及CSRF规则见[权限设计](../../docs/权限/权限设计.md#当前实现协议2026-11-07)。

使用显式数据源时另配置独立且至少32字节的 `ONTOLOGY_DATA_SOURCE_KEY`，用于绑定租户及源身份的 AES-GCM 凭据加密。密码只写入，更新省略密码保留原值，显式空字符串清空，响应只返回 `passwordSet`。缺少密钥不影响旧默认存储启动，但源配置及解密明确拒绝。新增平台迁移也必须显式执行，普通请求与服务启动不建平台表；保存数据源或预览不执行目标 DDL。

外部来源连接须由部署方配置 `ONTOLOGY_SOURCE_NETWORK_RULES` 的CIDR、端口及TLS允许范围，未配置默认拒绝全部；新来源默认验证完整证书。DNS及逐连接检查、已有回环知识库兼容配置统一见[数据源安全](../../docs/数据源.md#凭据与连接)，租户输入不能修改平台允许范围。

MySQL 需要支持 `utf8mb4_0900_bin` 排序规则及 16KiB 页的 InnoDB；当前集成验证使用 MySQL 26.7.0。实体表显式使用 `ROW_FORMAT=DYNAMIC`，UUID 列使用 ASCII，租户键使用二进制类型，物理列由稳定 field ID 派生，业务名称不进入 SQL 标识符。建模预检限制最多 100 个属性、32 个索引，保守核算 60000 字节逻辑行及 7000 字节页内预算，不支持的页大小在关闭 gate 前拒绝。表内值保留真实类型，`int64`、`decimal` 在 JSON 中使用字符串，时间使用带时区的 RFC 3339 字符串并精确到微秒；超出范围或精度会拒绝。

当前字段类型为 `string`、`text`、`longtext`、`uuid`、`int64`、`decimal`、`boolean`、`timestamp`。`string` 最多 255 个字符；`text` 保持最多 65535 个 UTF-8 字节；独立的 `longtext` 映射 MySQL `LONGTEXT`，单字段最多 4 MiB（4194304 个解码后的 UTF-8 字节）。两种文本均保留换行、空字符串及显式允许的 `null`，超限拒绝，不截断或自动转换。`text`、`longtext` 不支持索引和排序；可投影及使用类型化比较、集合、空值和字面前缀筛选，不提供全文搜索。已有 `text` 改为 `longtext` 属于结构变更，当前不提供自动扩列迁移。

Data 的 JSON 请求体最多 8 MiB（8388608 字节），包括字段名、JSON 转义及其他字段，超限返回 HTTP 413 / `request_too_large`。单字段预算与整份请求预算同时生效；多字段或大量转义可能先触发请求预算。`GET /data/capabilities` 返回 `dataTypes`、`maxTextBytes`、`maxLongTextBytes` 和 `maxRequestBytes`。Metadata 定义及身份管理请求仍使用各自的 1 MiB 请求预算，不能把长正文作为大默认值或大行条件绕过管理接口预算。

## 已实现接口

| 接口 | 行为 |
| --- | --- |
| `GET /healthz`、`GET /readyz` | 进程及数据库连接探针 |
| `GET /data/capabilities` | 返回当前实际开放能力及限制 |
| `GET/POST /data/sources`、`GET/PUT /data/sources/{id}` | 管理员维护租户数据源；PUT 必须强 If-Match；`view=select` 提供模型作者可选摘要 |
| `GET /data/sources/{id}/overview` | 当前租户管理员读取真实基础信息及带可用性/范围/口径的指标，数值按字符串返回；完成后复核当前会话、管理员及来源配置ETag，实际统计口径见[数据库管理工作台](../../docs/数据源.md#数据库管理工作台2026-11-08已确认) |
| `GET /data/source-versions` | 管理员或模型作者读取服务端版本系列和supported/pending状态 |
| `POST /data/sources/test`、`POST /data/sources/{id}/test` | 管理员测试连接与兼容状态；已有源可提交完整表单及If-Match并复用省略密码，空体/null沿用已保存配置测试 |
| `GET /data/sources/{id}/tables`、`GET /data/sources/{id}/tables/{table}` | 只读表、列及索引目录，可选namespace；表列表批量返回可选字段/主键列计数，列详情可返回默认值和注释，展示描述不改变结构签名 |
| `POST /data/sources/{id}/query` | 当前租户管理员携带来源配置If-Match及`SourceQuery{sql,limit?}`查询专属来源；有界单表SELECT或固定排行模板，返回`SourceQueryResult`。实际语法、限额及共享来源拒绝规则见[数据源配置](../../docs/数据源.md#sql只读控制台2026-11-08增量已确认) |
| `POST /data/sources/{id}/sql/analyze`、`POST /data/sources/{id}/sql/format` | 同一管理员/专属来源/If-Match边界的静态工具，输入SourceQuery，成功返回sql/valid/dialect/message；不连接来源数据库，不执行查询 |
| `GET /data/schemas/{schemaId}/storage-mapping` | 按对象 `definition.edit` 授权读取当前生效的主表及逐属性物理映射，含实际目录的列类型、存在状态；不返回凭据或业务行 |
| `POST /data/schemas/{schemaId}/instances` | 显式创建；必须提交幂等键 |
| `GET /data/schemas/{schemaId}/instances/{entityId}` | 按当前行列授权读取 |
| `PATCH`、`DELETE /data/schemas/{schemaId}/instances/{entityId}` | 必须携带最近读取结果的 `If-Match` |
| `POST /data/schemas/{schemaId}/instances/upsert` | 明确ID或完整唯一键的upsert；幂等键必填，默认check |
| `POST /data/schemas/{schemaId}/instances/bulk-create`、`bulk-update`、`bulk-delete`、`bulk-upsert` | 同Schema原子批量写；逐项clientKey与必要ifMatch，1～100项 |
| `GET /data/schemas/{schemaId}/write-capabilities` | 当前调用者八项能力、可写字段、唯一键、覆盖范围及预算；不返回物理映射 |
| `POST /data/transactions` | 默认平台存储跨Schema混合create/update/delete/upsert，整笔1～100项原子提交 |
| `POST /data/transactions/status` | 使用原完整事务正文核对持久回执，不执行任何业务写入 |
| `POST /data/query` | 中文名称或稳定 ID 绑定、类型化筛选、投影、排序和签名游标 |
| `GET /data/query/context` | 按当前目录及实例查询权限返回最小模型/字段能力，支持分类、模型、搜索及分页 |
| `GET /data/schemas/{schemaId}/form?operation=create` | 默认create，可显式patch；只返回当前可见字段及对应写权限的安全表单描述，具体语义见[权限设计](../../docs/权限/权限设计.md#当前实现协议2026-11-07) |
| `POST /data/query/ontology-sql/analyze`、`complete`、`format` | 单Schema及最多4条INNER Link的OntologySQL检查、参数推断、UTF-16诊断、上下文补全及格式化；不扫描业务数据 |
| `POST /data/query/ontology-sql/paths`、`insert-field` | 只读授权路径候选及多角色字段插入，保留筛选与参数；不读取业务行 |
| `POST /data/query/ontology-sql` | 编译为稳定ID的QuerySpec并进入同一执行器，返回原查询结果及实际绑定的query供续页 |
| `POST /internal/publications/preview` | 只读结构预览；正文为 `contracts.PrepareRequest` |
| `POST /internal/publications/prepare` | 持久关闭受影响 Schema 入口、等待已入场事务排空、执行并核验物理计划 |
| `POST /internal/publications/activate` | 核验 Metadata 权威当前摘要后恢复访问 |
| `POST /internal/authorization/object-value` | 仅服务端授权取值；固定实例或当前用户匹配的资料对象，正文及响应见下文 |

内部发布接口只接受服务 Bearer token 和 `X-Tenant-ID`。普通接口使用用户在工作台账号密码登录后取得的 Cookie 会话，Data 每次通过 Metadata 核对会话、当前账号和租户；所有非安全方法请求，包括 POST 查询，还要求同源 `Origin` 与匹配的 `X-CSRF-Token`。不能用外部请求的租户头或手工 Bearer 凭证替代用户会话。

### 权限对象上下文取值

`POST /internal/authorization/object-value`只接受内部服务凭据及`X-Tenant-ID`；公网网关拒绝`/internal/`，普通Cookie和MCP凭据不授予访问。Metadata只可从管理员保存的权限规则构造请求，不能将客户端提供的对象或属性原样转发。规则对判权属性的最小读取不授普通对象读取、字段投影或编辑资格，公共机制仍由[权限设计](../../docs/权限/权限设计.md)维护。

实际DTO见[permission_context.go](../../contracts/permission_context.go)。正文为`{schemaId,fieldId,instanceId?,matchFieldId?,matchValue?,mode?}`，字段仅接受稳定ID。选择器二选一：固定`instanceId`，或`matchFieldId`等于已由Metadata解析的当前用户属性值。`mode`省略或`scalar`要求恰好一行，字段匹配还要求当前定义存在单字段唯一索引；`set`匹配结果最多100行，查询取至多101行检测超限，按实例ID确定顺序并返回去重后的属性值集合，超过100行整项拒绝，即使去重后不超限也不截断。仅支持平台默认MySQL单表；映射来源、跨源与Link路径不可用于此接口。

响应为`{value,digest}`，单值或集合内的int64/decimal继续使用精确字符串；取值总JSON不超过64KiB，内部请求正文不超过32KiB。缺少对象、业务NULL、无匹配、歧义、无效绑定及超预算统一返回403且不回显原值。只选取请求属性与行身份/ETag作依赖证据，不返回其他业务属性，也不再次调用授权服务造成递归。

取值使用独立READ COMMITTED只读事务，前后核对发布gate与权威当前定义；不锁gate，避免在原Data写事务持有同一gate时回调死锁。此精确内部路径使用独立的1个执行槽、至多4个等待请求和4秒总预算，不占原业务请求的执行槽，也不无限绕过资源限制。digest绑定租户、定义ETag、选择器、所选行ID/ETag和值，后续授权复核据此发现已提交的依赖变化。该读取不观察调用方事务的未提交修改，也不提供跨服务原子撤权；写入侧对上下文源与写目标重叠及未认证的跨源组合继续明确拒绝。

MCP 接入另接受 Metadata 签发的 `mcp_` 外部 Bearer，仅允许窄路由白名单内的能力发现、查询上下文、对象读取、QuerySpec 与 OntologySQL 查询。Metadata 每次核验凭据、scope 及真实用户，Data 继续执行相同行列权限；查询目录经窄内部桥读取，不开放完整定义、物理映射或数据源管理。外部 Bearer 不沿用浏览器 Cookie/CSRF，禁止混用；具体认证边界见[权限设计](../../docs/权限/权限设计.md#mcp-与外部客户端认证2026-11-08已接入)，客户端接入见[MCP服务](../mcp/README.md)。

发布失败后保留暂停状态，由 Metadata 重试同一个操作恢复。初次建表核验列、类型、可空性及完整索引；不接管未纳管同名表。默认无 binding 的已有模型仍只支持保持存储签名不变的展示修改；显式正向映射支持认证范围内追加可空列及索引，经同一计划编译器预览、执行和核验。反向映射只读取并验证已有结构，不对源表执行 DDL。预检拒绝不暂停现有映射；持久暂停后的失败不自动恢复流量。详细约束统一见 [Schema](../../docs/模块/Schema.md#2-建模方向与预览)。

## 八种内置写操作

每个已生效Schema按`(schemaId, operation)`动态获得`create/update/delete/upsert`与对应四个`bulk_*`，共享类型、行列权限及存储事务；不会创建八份Action定义。当前三个单条接口保留请求/响应与create历史幂等兼容，update继续PATCH且不提供PUT。详细规则唯一见[数据读写](../../docs/数据读写.md)，DTO在[write_operations.go](../../contracts/write_operations.go)。

upsert正文为`{match, values, conflictPolicy?, idempotencyKey}`；match二选一为`{id}`或`{indexId, values}`。业务键须是当前真实完整唯一索引，match.values使用稳定字段ID，全部非null；重复写入匹配字段须类型等价且更新时不改键。创建分支可保留match.id，普通create仍由服务端生成UUID。其他唯一键冲突返回安全冲突，不选择另一行更新。

`check`为默认：没有If-Match只创建，已存在428；带完整强If-Match只更新，消失或过期412。`overwrite`必须显式声明并另获`instance.upsert.overwrite`，不能与If-Match同用，有则PATCH、无则创建，仍落实全部相关字段与旧/新行条件。upsert同时要求`instance.upsert/create/patch`；四种bulk沿父操作权限，不新增bulk权限名。新权限不自动写入已有租户，须管理员显式配置并生效。

bulk正文为`{atomic?, items, conflictPolicy?, idempotencyKey}`，仅支持默认true；每项clientKey唯一。create项提供values；update项提供id/ifMatch/values；delete项提供id/ifMatch；upsert项提供match/values和必要ifMatch，整批同一策略。ifMatch使用完整带双引号的强HTTP ETag，例`"不透明凭证"`，不能用一个外层头代表整批。values使用稳定字段ID或当前属性名，禁止同字段双重命名。单条upsert与bulk写入values必须非空，最多100项，重复身份或最终命中拒绝。

单条upsert首次创建201、更新/重放200，返回`WriteResult{schemaId,outcome,id,etag,values?}`及HTTP ETag。bulk成功200，返回`{schemaId,atomic:true,committed:true,replayed?,results}`，results按输入顺序含clientKey；删除项不返回values/etag。失败返回统一安全错误，整批无任何已提交项；当前不提供部分成功或逐项错误结构。

幂等键1～128字节，作用域包含租户、用户、Schema、操作，摘要包含原意图与并发条件；同键不同意图409。平台新增`008_data_write_receipts`，须显式`-migrate`，普通启动不建表。数据/回执/outbox同一本地事务，重放再次校验当前操作及历史/当前行义务，删除回执授权摘要变化即拒绝。当前没有自动过期或清理，保留策略需另行设计。事务竞争最多三次，受15秒整体预算约束；8MiB正文、完整响应及保留证据预算在提交前检查。

能力发现返回八个`name/allowed/reason/writeFields`及安全fields、matchModes、uniqueKeys、overwriteAllowed、overwriteWriteFields、overwriteUniqueKeyIds、maxBulkItems和请求/响应预算。基础check与可选overwrite分别描述字段/唯一键，额外覆盖规则不收窄check。来源维护或未启用、无写权和关系限制均可使操作不可用，执行时仍重新核验，UI提示不授予权限。

## 默认平台存储事务

正文为`WriteTransactionRequest{idempotencyKey,operations}`，每项`{schemaId,operation,clientKey,id?,ifMatch?,match?,values?,conflictPolicy?}`；DTO见[transactions.go](../../contracts/transactions.go)。只接受当前用户租户内默认平台存储的已生效Schema，映射Schema和跨来源事务以`transaction_storage_unsupported`明确拒绝。总项数1～100，clientKey在整笔内唯一，逐项复用八写接口的字段、旧/新行权限、强ETag和upsert规则；同一实际对象不能重复修改，不提供前一步结果引用。`conflictPolicy`仅upsert可填，覆盖仍须额外授权。

所有Schema按稳定ID排序获取发布gate，在同一个`*sql.Tx`内执行类型化SQL、实例写入和outbox；结果按输入顺序返回。事务共用15秒及8MiB请求、响应和保留证据预算，不按项重置总预算。提交前复核当前授权和Schema摘要；内部执行桥另通过guard复核原会话及Action/Function身份，其服务身份不能代替业务权限。该机制不承诺跨服务原子撤权。

新增`009_data_transaction_receipts`须显式迁移。事务幂等键按租户、当前用户和整笔key隔离，摘要包含完整原请求及并发条件；同key不同意图409。数据/outbox/成功回执同事务提交。可确定的业务失败在savepoint回滚全部写入和outbox后，提交最小安全`status=failed`回执，不保留已回滚行；成功为`status=committed`且`committed=true`。可确定的前置字段、Schema、并发凭证和逐项权限拒绝也在同一receipt锁内登记最小failed，不执行任何业务写入；并发在途请求必须先释放该锁，已有committed绝不会被拒绝结果覆盖。这两种终态均HTTP200，`results`仅在成功时含逐项结果，重放加`replayed=true`。解析、身份、请求总预算、guard前置拒绝或存储不可用等可返回HTTP错误，网络、超时或commit错误不能据此断言回滚。

`POST /data/transactions/status`必须提交原完整正文，沿当前会话、逐项授权和相同hash读取回执，不创建回执或重跑业务。`not_found`只表示本次未看到持久回执，不能证明没有在途提交；客户端可再次核对或以同key及相同正文重试原提交。成功回执复用历史及现行行约束校验与当前字段投影，删除回执授权摘要变化即拒绝。确定性前置校验或权限拒绝时，只允许同主体、同hash读取已有最小failed错误，不读取历史业务值；已有committed仍返回当前拒绝，未发现回执则保留校验错误。权限/定义变化或权威服务不可用时，成功回执的恢复查询可能拒绝，不绕过现行授权披露旧结果。回执暂不自动清理，生产保留和故障恢复仍待验收。

## 显式来源映射

数据源由当前权威会话中的租户管理员维护；模型作者读取可选源及表结构使用现有 `definition.edit` 授权，源目录权限不替代实例行列授权。物理表名、列名来自已校验绑定与目录，密码不进入模型正文。数据源的共享租户列与专属数据库规则见[数据源配置](../../docs/数据源.md)。

版本配置实际协议为 `versionPolicy=auto|pinned` 与 `expectedVersionLine`，响应另有服务端 `compatibility`；测试返回 `success/serverVersion/compatibility`，连接或兼容失败为200且success=false，权限/格式/ETag错误仍返回相应HTTP错误。启用状态保存会实际检查，失败不替换原配置；停用状态允许离线保存。检查保存在原config_json中，无新增平台迁移。外部每条物理连接先核验产品、版本和关键配置，结构、查询和Link游标绑定能力签名；完整规则及认证范围唯一见[版本管理](../../docs/数据源.md#数据库版本选择与校验2026-11-07已确认并实现)与[存储引擎](../../docs/存储引擎.md#版本能力登记2026-11-07首批实现)。

当前一个 Schema 最多映射8张来源表。主表提供稳定标识，关联来源按同类型唯一键匹配，每个主对象每来源最多一行，没有关联行的属性返回 `null`。跨来源属性可以投影；筛选、排序及权限行条件仅支持主表属性，涉及其他来源的条件明确拒绝。查询逐源读取，不承诺跨数据库的一致快照或事务。其中经显式认证/初始化的专用MySQL单主表可使用八操作；其他映射保持只读，不能把单库事务保证套到多表或跨源。

2026-11-08已补齐数据库管理工作台后端：管理员`GET /data/sources/{id}/overview`真实观测、批量表字段/主键列计数及列默认值/注释。指标数值使用字符串并携带可用性、统计范围和口径，权限不足时该项不可用而不伪造0；MySQL的Questions差分为实例QPS，PostgreSQL本轮不提供QPS。目录展示描述不改变结构签名；概览交付前复核会话、管理员及来源配置ETag。详细字段及口径唯一见[数据库管理工作台](../../docs/数据源.md#数据库管理工作台2026-11-08已确认)，前后端分项验证见项目总览。表预览继续使用既有专属来源有界单表查询，共享来源不可通过管理页读取物理业务行。

同日已按用户确认实现只读SQL控制台增量：`SourceQuery.limit`、`/sql/analyze`和`/sql/format`工具及固定目录排行模板。请求limit省略或0按100处理，其他值及SQL LIMIT须1～100，执行取两者较小值。工具沿当前管理员、同租户、启用专属来源及配置If-Match边界，仅做词法/封闭语法校验和格式化，不连接来源库或执行SQL，不承诺表存在及数据库读取权限；共享来源拒绝。实际DTO、模板与统计口径唯一见[SQL只读控制台](../../docs/数据源.md#sql只读控制台2026-11-08增量已确认)。CREATE/INSERT/UPDATE及DDL未进入本轮。

能力响应中 `operations` 表示默认存储；`mappedOperations` 为 `read/query`，`sourceDrivers` 为 `mysql/postgresql`，`mappedUpdates` 为 `nullable-fields-and-indexes`。`crossSourceProjection` 为真，`crossSourceConditions` 与 `crossSourceTransactions` 为假；它表示所有映射共有的读取能力；具体八项写能力必须读取每Schema的write-capabilities，不能仅按有无storageBinding判断。全局另返回batch=true、writeOperations八项、perSchemaWriteCapabilities=true及maxBulkWriteItems=100。

默认存储迁入专用来源使用[独立离线工具](../../tools/storage_relocation/README.md)，没有公开重绑接口，也不在启动或普通请求中执行。Data 负责校验来源与目标归属、整批关闭 gate 并排空已准入事务、复用映射 DDL 编译器复制及逐字段核验，最后由协调器在 Metadata 提交并核验后整批激活。源表保留，目标已有不同内容时拒绝覆盖；完整范围、预算与恢复要求统一见[默认存储迁入专用源](../../docs/发布与迁移.md#默认存储迁入专用源2026-11-07)。离线迁移本身不启用来源实例写入；当前[个人知识库](../../docs/应用场景/个人知识库.md#配置复核与专用来源迁移当前)已沿此路径迁入专属数据库，导入器以保存清单做只读核对。

### 专用MySQL来源的显式写初始化

在Data服务目录及正式配置环境中先执行`go run ./cmd/server -migrate`，再单独执行：

```powershell
go run ./cmd/server -initialize-mapped-writes -tenant <租户ID> -schema <SchemaID>
```

命令不能与-migrate合用，必须明确租户和Schema；仍检查平台gate、权威当前定义、来源网络/TLS及真实结构。仅支持专用MySQL、单主表、完整非生成业务列映射、独立UUID系统主键、InnoDB/utf8mb4_0900_bin和真实索引；触发器、级联约束、共享、多表/跨源及PostgreSQL拒绝。外键关系列不允许普通属性直写，当前任何已绑定Link的来源端点均暂不开放delete/bulk_delete。详细认证条件见数据读写第9节。

来源账号另须直接拥有该库/全局SELECT和TRIGGER，以及全局REFERENCES（或全局SELECT）以完整看见跨库入站约束，不能将不可见目录误判为无触发器/外键。全局REFERENCES不授业务读取，不要求全实例业务SELECT；间接角色、table-only或复杂范围尚未认证，返回不可写。程序不会自动授予或修改已有数据库账号权限。

业务列须精确匹配类型、长度、精度及排序规则，不能仅按反向只读的宽松类型兼容认证写入；CHAR裁剪、短VARCHAR、低精度时间与不一致文本排序规则均拒绝。主键为规范小写UUID，文本使用utf8mb4_0900_bin，UUID使用ascii_bin或utf8mb4_0900_bin。部分REVOKE导致无法证明完整目录可见时同样拒绝。

显式命令创建来源内af_source_write_identity/gates/receipts/outbox及source_write迁移记录，绑定租户/来源/表，不修改业务行或来源配置；普通启动与请求不迁移也不自动启用。来源写入、回执和outbox同事务，源gate参与prepare排空与activate恢复。已启用来源的ETag为完整规范化状态HMAC，投影不改变凭证，隐藏字段改变也使旧凭证过期；来源备份须含控制表及持久密钥。未启用映射保持读取协议。本批没有对现有个人知识库执行初始化。

## 关联行查询

`QuerySpec.rows` 与 OntologySQL `JOIN LINK` 共用同一执行器，最多4条INNER Link、5个对象节点；支持managed/junction/foreign_key、反向、分支、自关联别名。必须同数据源及同数据库，只允许各节点主表属性；不支持LEFT、任意ON、跨源、次表投影、关系属性或聚合。每节点租户/行列授权与每条Link的发现和查询授权进入完整SQL，之后排序及LIMIT；游标绑定全部定义/授权/来源与目录摘要。`rowMode=true` 返回独立 `rows/fields`，保留每次节点绑定，不能将row ID用于实例接口。实际协议和共同语义见[DataSet](../../docs/数据查询/DataSet.md#关联行查询增量2026-11-07)。

`GET /data/capabilities` 新增 `rowQueries=true/maxRowJoins=4/queryPathPlanning=true`；`maxLinkHops=1` 表示既有固定实例via接口，并非INNER关联行上限。路径规划仅查授权元数据，使用候选不跳过运行时物理和权限校验。

## 正确性与限制

2026-11-08补充公网资源预算：Data每进程同时最多处理1个操作，最多4个等待者且最多等待250ms；等待发生在会话远程校验和正文读取之前，超出返回429 `data_busy`及`Retry-After: 1`，取消及时出队。同进程多个handler共用预算，健康/就绪探针豁免。该默认值面向160M单服务模板保守设置，不代表吞吐验收。请求context最多15秒，过期/取消返回受控JSON；数据库取消依赖实际驱动的QueryContext支持。完整JSON响应最多8 MiB，先计入编码转义、字段和游标开销，再有界编码与缓冲后提交，超限返回422 `response_budget`，不会先发送部分成功结果。实例、映射、Link关联行及来源SQL在逐行积累期间也检查预算；创建/更新在提交前限制完整实例状态，幂等身份与冲突规则不变。调用方遇到响应超限应减少投影或分页，不能静默截断；目录更严格的字节分页以OntologySQL主说明为准。单个数据库行在Scan前仍可能由驱动分配接收内存，以上不是对任意恶意来源报文的完整内存隔离，来源网络和数据库读取预算仍需受控。

Data在所属单一本地事务内提交实例、操作幂等记录和outbox，并在更新及删除时检查当前 ETag。幂等重试返回原创建结果，读取仍检查当前权限；同键不同意图拒绝。创建、修改同时检查可写字段和新行条件，修改还检查旧行条件。查询权限在 SQL 中先于排序和分页执行，不通过读取一页后删行实现行过滤。

请求开始与本地事务提交前分别通过 Metadata 请求当前授权决策，由其读取当前用户、角色和已生效策略后交给 OPA 求值；不复用旧允许，决策变化会回滚。单条读取使用 `instance.read`，集合查询使用独立的 `instance.query`，创建、修改及删除分别使用对应操作授权。本实现没有完成跨服务原子撤权协议，不能据此承诺撤权与正在提交的写入严格串行。Schema gate 通过本地事务行锁保证发布和 Data 请求的入场顺序；当前每个 Schema 的操作会串行竞争该锁，吞吐尚未测量。

QuerySpec 目前每页为 1 至 100 条、最多一个业务排序字段并以实例 ID 决胜；表达式有深度、节点和集合预算。支持 AND/OR/NOT、等值及比较、显式空值、有限 IN/NOT IN、字面前缀；NULL 保留 SQL 三值逻辑。decimal 条件和游标参数显式转换为对应精度，防止 MySQL 将字符串比较转为 DOUBLE 后丢失大数精度。属性名称按 Metadata 相同的 NFC 和大小写折叠规则绑定，属性名称不能覆盖另一属性 ID。游标绑定主体、租户、定义摘要、完整查询和授权决策，并在一小时后过期。跨页期间业务数据变动按每次新执行可见状态处理，不提供跨请求历史快照。

首批 [OntologySQL 实际协议](../../docs/数据查询/OntologySQL.md#首批实际协议2026-11-07) 支持单Schema只读SELECT、显式投影、类型化参数、布尔/比较/空值/集合/前缀条件、一个排序字段及LIMIT；通过有界lexer/parser和类型绑定进入上述共用执行路径。目录及补全使用调用者的当前定义发现权限，叠加instance.query和字段义务；响应不返回物理映射。文本/参数/游标限制、诊断与错误语义以该主要说明和contracts为准。能力响应的ontologySQL为true不表示任意SQL可用。

分表、计算属性、Enum 属性的跨定义协调、Link 多跳/跨源、跨来源事务、JOIN/聚合及任意联邦条件尚未开放。权限当前支持受控行条件、静态字段集合及可安全合并的角色规则，不能安全合并的组合明确拒绝；不代表完整 ABAC 或逐行掩码已实现。Action/Function的实际执行边界见[执行服务](../execution/README.md)。前端已在 [apps/web](../../apps/web/README.md) 接入 CRUD、单 Schema 查询、OntologySQL及 Link 单跳导航。服务对无法表达的请求直接拒绝，`capabilities` 不将五类元数据可维护等同于这些运行能力已实现。outbox 和幂等记录目前不自动清理；保留期限、事件投递、全量恢复和生产容量仍待后续实现及验证。

## Link 运行接口

关系映射、生命周期与实际协议统一见 [Link](../../docs/模块/Link.md#当前关系数据库实现2026-11-07)，公共查询规则见 [DataSet](../../docs/数据查询/DataSet.md)。`POST /data/query` 接受可选 `via={linkId,direction,instanceId}`，`from` 为返回端；在数据库内通过参数化关系条件完成正向或反向单跳，端点权限过滤先于排序分页。平台默认 MySQL 关系事实可增删，外部同源 MySQL/PostgreSQL 关联表和外键只读，跨源关系明确拒绝。

| HTTP | 用途 |
| --- | --- |
| `GET /data/links/{linkId}/instances?direction=out&instanceId=...&limit=25` | 平台关系记录分页，游标通过 `cursor` 传回，结果只包含可访问的两端 |
| `POST /data/links/{linkId}/instances` | 平台关系新增，正文为 `sourceId/targetId`，返回关系及 ETag；重复边拒绝 |
| `DELETE /data/links/{linkId}/instances/{id}` | 平台关系移除，携带强 If-Match；不删除对象 |

托管关系在显式发布中建表、核验索引/外键并经持久 gate 激活，普通启动及查询不创建表。业务事务串行检查端点与基数，关系/outbox 原子提交；对象有关联时默认限制删除。外部绑定通过实际目录验证，保留来源系统的维护边界。关系授权为独立 `link.query/create/delete`，已有租户须显式授权；两端实例授权继续检查，不通过读取一页后隐藏行实现。这里不承诺跨服务原子撤权或外部源跨表快照。

## 验证

2026-11-08八写操作增量已通过九个Go模块的测试与vet，Data全量启用真实MySQL/PostgreSQL。新增核心用例覆盖八操作、八并发同键upsert、唯一键精确比较、原子回滚、旧create回放兼容、权限收紧及覆盖范围；来源用例使用独立随机平台/来源库，覆盖八操作、回执/读取HMAC一致、隐藏字段变化、源内幂等/outbox与发布排空/恢复。另以临时低权账号验证隐藏触发器不得认证，临时跨库外键验证global REFERENCES提供完整元数据可见性且级联约束拒绝；触发器创建使用独立测试维护账号，未改现有来源权限。真实TLS两租户公开Cookie矩阵及42项Nginx回源检查通过，完整分项结果见项目总览。测试库/账号按runner finally清理，本轮未迁移现网或修改知识库；生产故障恢复、三库写入及性能仍未验收。

同日默认平台跨Schema事务增量已通过Data全模块真实MySQL/PostgreSQL测试与vet；新事务用例实际在MySQL运行，覆盖四操作混合提交、同key八并发只提交一次、跨Schema同ID身份、数据/outbox/savepoint回滚及持久failed、格式/权限前置失败恢复、状态核对不写入、主体隔离、当前字段及新行约束、实际目标去重、guard撤销及未知基础设施错误。另以真实并发锁验证前置拒绝等待已在途事务提交且不能覆盖committed回执。测试在独立随机库/账号执行并清理，未执行现有库迁移、重启或部署；不表示执行沙箱联合验收、生产恢复或跨来源事务已通过。

来源基本实库要求MYSQL_TEST_DSN与不同security_库的MAPPED_WRITE_TEST_DSN；隐藏目录/跨库约束补充分别使用MAPPED_WRITE_LIMITED_TEST_DSN及MAPPED_WRITE_REFERENCING_TEST_DSN。若测试MySQL启用binlog且创建触发器要求额外维护权限，可用MAPPED_WRITE_DDL_TEST_DSN仅创建/清理合成trigger，实际被认证来源连接仍沿其最小认证权限。未提供对应环境的补充用例明确跳过，不能据普通单元通过声明完整来源验收。

2026-11-08只读SQL控制台增量以`scripts/check.ps1 -SourceIntegration`完整通过八个Go模块测试及vet，另通过可信Go启动器vet。真实MySQL/PostgreSQL验证普通查询请求limit生效、四种固定排行SQL执行和系统/`af_`表过滤；PostgreSQL另以临时角色核对SELECT与schema USAGE双重权限及未知reltuples返回NULL。HTTP模块覆盖静态工具离线不连接来源、当前管理员/租户/If-Match/启停/共享来源边界，交付前撤权、注销及ETag变化后拒绝，真实query的请求上限与会话复核。用例仅创建和清理随机测试表、schema及NOLOGIN角色，没有平台迁移，不作为新生产权限、性能或三库共同认证。

2026-11-08数据库管理增量以`scripts/check.ps1 -SourceIntegration`完整通过八个Go模块测试及vet，另通过可信Go启动器vet；最终目录计数改动另通过Data真实MySQL/PostgreSQL模块测试及vet。真实数据库验证概览、目录、默认值SQL NULL/原文、中文注释、展示描述变化后结构签名保持及批量字段/主键列计数。HTTP模块回归使用既有fixture注入会话检查器，覆盖跨租户和交付前撤销管理员、注销、配置ETag更新的拒绝；该项不冒充新增完整真实登录链或生产撤权验收。没有新增平台迁移；前端交互、本地Data就绪及缓存账号登录401导致的真实浏览器验收未完成均见项目总览。

```powershell
go test ./...
```

设置`MYSQL_TEST_DSN`后，相同命令还会执行真实MySQL集成用例；来源八操作另要求`MAPPED_WRITE_TEST_DSN`指向不同且库名以`security_`开头的独立空专用MySQL测试库（测试会显式迁移控制表），未设置时明确跳过该用例。集成测试使用随机租户及 Schema，不删除或清空共享平台表；验证预览无写入、发布门禁、条件授权、幂等、唯一冲突、并发凭证、NULL/NOT、事务及 outbox 回滚和改名后的当前映射。单元测试覆盖注入参数、隐藏字段、精度边界、游标篡改及上下文隔离。检查通过不代表已完成三库认证、跨服务原子撤权或性能验收。

OntologySQL编译器和HTTP测试覆盖残缺文本与fuzz seeds、有界嵌套、精确数值/微秒、命名参数、UTF-16/CRLF、注释/转义、未支持语法拒绝，以及目录、搜索、补全不暴露隐藏模型/字段/物理映射。2026-11-07真实MySQL＋OPA集成在 `tests/integration` 的既有隔离租户内验证文本与JSON共用结果、行列权限、精确数值及游标续页；映射集成同时验证MySQL/PostgreSQL来源初页和续页的字段描述没有物理映射。当前个人知识库另经公开Cookie会话和CSRF完成442对象只读核对，结果见项目总览；没有新增业务测试对象。

`longtext` 单元回归覆盖多字节边界、4 MiB 正文的数据库值编解码、原 `text` 65535 字节边界、空值/空字符串、8 MiB 请求及尾部空白超限、DDL 映射、索引/排序拒绝和能力声明。2026-11-07 的模块测试通过，未启用数据库环境变量；`TestMySQLLongTextRoundTripAtApplicationLimit` 为待显式运行的真实 MySQL 建列、物理核验及 4 MiB 往返测试。长正文响应容量和性能仍未验收，查询应按需投影正文并选用较小分页。

2026-11-07新增数据源配置、AES-GCM身份绑定、授权拒绝及物理标识边界测试。已使用真实MySQL随机租户/随机表验证配置加密、HTTP连接与表列发现、主键和唯一/复合索引、跨租户隔离与专属目标并发冲突、ETag并发返回200/412、引用保护及停用拒绝；没有修改现有知识库定义或实例。`TestSourceCatalogRealDrivers` 与 `TestSourceHTTPConnectionAndCatalogRealDrivers` 可另外通过 `POSTGRES_SOURCE_TEST_DSN` 连接外部PostgreSQL验证，未设置时明确跳过。外部来源映射的结构执行和跨源读取结果仍须按实际运行的独立用例记录，不由连接测试代替。

同日新增 [版本HTTP生命周期](sources_version_integration_test.go)、[物理连接回归](source_versions_test.go)和[来源主表能力认证](source_version_matrix_test.go)。来源/映射/Link测试可用 `MYSQL_SOURCE_TEST_DSN` 指定独立外部MySQL，未设时回退 `MYSQL_TEST_DSN`，平台控制库仍由后者指定；PostgreSQL继续用 `POSTGRES_SOURCE_TEST_DSN`。本批实际版本及模块/真实集成/浏览器结果统一见[项目总览](../../docs/项目总览.md#当前实现与验证2026-11-06)，待适配版本不因测试入口存在而视为已认证。

同日离线迁移的真实 MySQL 定向测试已通过，覆盖完整字段及空字符串/NULL/长正文/精确数值保留、源表不变、整批准备与激活、DDL 中断恢复，以及目标接管、数据漂移、托管 Link 依赖、来源变化、缩减清单和部分准备批次的拒绝。迁移 CLI 另验证同一 MySQL 实例上的 Metadata/Data 维护锁互不自阻塞；这些隔离用例不代表跨实例持续隔离、生产恢复或性能验收。
