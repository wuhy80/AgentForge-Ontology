# Metadata 开发入口

本模块实现组织管理、五类定义草稿与当前态、名称解析、方向发现、与 Data 协作的持久发布操作，以及本地账号、会话和权限管理。身份及管理接口协议见 [identity](identity/README.md)，公共权限规则以[权限设计](../../docs/权限/权限设计.md)为主要说明；架构、组织及定义生效分别见[总体架构](../../docs/总体架构.md)、[组织与隔离](../../docs/组织与隔离.md)及[发布与迁移](../../docs/发布与迁移.md)。

## 构建与运行

本模块使用独立 `go.mod`，可以在 `GOWORK=off` 下构建。

```powershell
$env:GOWORK='off'
go test ./...
go vet ./...
```

启动入口读取以下环境变量；不要把凭据提交到 Git。

| 变量 | 用途 |
| --- | --- |
| `ONTOLOGY_MYSQL_DSN` | 本服务拥有的 MySQL 数据库连接，连接字符集使用 utf8mb4 |
| `ONTOLOGY_SERVICE_TOKEN` | Data/Metadata 内部通信凭据，至少32字节 |
| `ONTOLOGY_OPA_URL` | OPA 服务地址 |
| `ONTOLOGY_OPA_TOKEN` | 可选的 OPA 服务凭据 |
| `ONTOLOGY_DATA_URL` | Data 服务地址 |
| `ONTOLOGY_PUBLIC_ORIGIN` | 工作台允许的完整来源，默认 `http://127.0.0.1:5173`；须与浏览器实际地址一致 |
| `ONTOLOGY_LISTEN` | 监听地址，默认 `127.0.0.1:18080` |
| `ONTOLOGY_BOOTSTRAP_TENANT`、`ONTOLOGY_BOOTSTRAP_USERNAME`、`ONTOLOGY_BOOTSTRAP_PASSWORD` | 仅显式执行 `-bootstrap-admin` 时使用的初始租户及管理员账号 |

```powershell
# 显式迁移后退出；普通启动不创建平台表。
go run ./cmd/server -migrate
# 首次使用时配置上述三个 BOOTSTRAP 变量，再显式初始化空租户。
go run ./cmd/server -bootstrap-admin
go run ./cmd/server
```

仓库的 `scripts/start-dev.ps1` 会显式迁移并初始化本地管理员，首次生成的账号保存在忽略目录 `.cache/dev/admin.json`。重复启动及重复初始化不会覆盖已有账号或密码；首次登录后可在工作台修改密码。正常使用不需要手工签发或粘贴 Bearer 凭证。

## HTTP 入口

用户通过租户、用户名和密码登录，浏览器使用可撤销的 HttpOnly Cookie 会话；每次请求核对当前账号、有效期及角色。登录后的非安全方法请求，包括 POST 预览，必须发送匹配的 `Origin` 与 `X-CSRF-Token`，CLI 也须显式携带这两项；登录请求校验来源，不要求已有会话的 CSRF。会话与密码规则、吊销行为及管理接口详见 [identity](identity/README.md)。

定义管理使用整份定义的 `definition.read`、`definition.edit`、`definition.publish` 授权；组织管理使用 `organization.read`、`organization.edit`。Metadata 从数据库读取当前用户、角色绑定及已生效策略，向 OPA 提供受控运行时授权；不以示例静态授权替代当前账号策略。带行条件的定义管理授权返回422，不忽略条件继续访问。

| 方法和路径 | 用途 |
| --- | --- |
| `GET /healthz`、`GET /readyz` | 存活与数据库就绪探针 |
| `GET /metadata/capabilities` | 当前开放能力与限制；Schema双向模式及 `display-and-mapped-additions` 更新边界 |
| `POST /metadata/auth/login`、`GET /metadata/auth/session`、`POST /metadata/auth/logout` | 账号登录、恢复当前会话及服务端注销 |
| `PUT /metadata/auth/password` | 核对当前密码、修改密码并旋转会话 |
| `/metadata/admin/users`、`/metadata/admin/roles` | 当前租户的用户、启停、密码重置及角色管理 |
| `/metadata/admin/user-groups`、`/metadata/admin/user-groups/{id}` | 租户用户组、组角色及成员；列表/创建使用GET/POST，详情/更新使用GET/PUT，更新须带强If-Match |
| `/metadata/admin/policy`、`POST /metadata/admin/policy/publish` | 权限策略草稿及显式生效 |
| `POST /metadata/admin/simulate`、`GET /metadata/admin/audit` | 策略试算及最近200条审计；审计响应声明 `hasMore` |
| `POST/GET /metadata/categories` | 创建和分页读取分类 |
| `POST/GET /metadata/groups` | 创建和分页读取分组；读取指定 categoryId |
| `POST /metadata/definitions` | 创建定义及首份草稿，返回服务端生成的定义/字段 ID |
| `GET /metadata/definitions` | 默认分页读取已生效定义；`view=draft` 发现未发布草稿，支持 categoryId、kind |
| `GET /metadata/definitions/{id}` | 读取当前定义；只有草稿的对象返回404 |
| `GET/PUT /metadata/definitions/{id}/draft` | 读取/保存草稿；PUT 必须带强 If-Match |
| `POST /metadata/definitions/{id}/preview` | 预览草稿，缺少草稿时预览当前；显式来源会读取实时目录、验证映射并编译对应计划，不创建发布操作、修改源库或占用发布状态 |
| `POST /metadata/definitions/{id}/publish` | 按草稿强 If-Match 生效，或恢复同一持久操作 |
| `GET /metadata/publications/{id}` | 核对操作状态及恢复进度 |
| `GET /metadata/resolve?category=分类名&name=定义名&kind=schema` | 中文名称解析；kind 可选 |
| `GET /metadata/schemas/{id}/links` | 发现正反方向，按 Link ID 与 direction 定位 |

`GET current` 和 `GET resolve` 是内部服务凭据唯一可访问的业务路径；必须同时提供 `X-Tenant-ID`。内部身份核对及权限决策分别通过受服务凭据保护的 `/internal/identity/session`、`/internal/authorization/decision` 提供给 Data。服务凭据不能编辑、发布、读取草稿或替代普通用户授权。JSON `etag` 为裸并发凭证，HTTP `ETag` 和 `If-Match` 使用双引号包围凭证。

组织及定义列表默认100条、最多200条，使用返回的 `nextCursor` 作为下一次 `after`。这与 Data 的查询游标分别处理。

Link 增加可选 `storageBinding`：支持平台托管关联表、同外部数据源关联表和外键映射，字段及边界统一见 [Link](../../docs/模块/Link.md#当前关系数据库实现2026-11-07)。保存及发布均核对两端为同租户已生效 Schema、映射形状及同源约束，Data 继续核验真实表列和索引。已生效无绑定 Link 允许一次性追加绑定并通过同一 prepare/activate 流程生效；之后绑定、端点和基数不可修改。无绑定仍仅为定义，发布不会自动生成关系事实。方向发现接口返回定义与方向，实际邻接数据由 Data 的 `QuerySpec.via` 执行。

属性协议支持独立的 `longtext` 类型，单字段上限为 4 MiB 解码后 UTF-8 字节，`text` 仍为 65535 字节；Metadata 校验两类文本默认值并拒绝其索引，Data 的实际存储和请求预算见 [Data 服务](../data/README.md)。Metadata 定义与身份管理 JSON 请求体仍为 1 MiB，因此定义中默认值和权限行条件还受管理请求总预算约束；正文应通过 Data 实例接口读写。既有属性从 `text` 改为 `longtext` 仍触发结构变更拒绝，不能通过草稿保存或展示修改绕过迁移边界。

Schema 的 `storageBinding` 声明主数据源、命名空间、表、稳定标识列及可选租户列；每个属性的 `mapping` 声明来源和物理列，跨表/跨源另外声明唯一关联键。Metadata 归一继承的绑定信息、保持稳定字段ID，并拒绝同来源列重复、关联键不一致及跨来源索引；Data 在预览、发布和执行时重新校验实际目录、类型、租户范围及唯一键。连接配置与密码仅由Data持有，定义中不保存凭据，来源配置协议见[数据源](../../docs/数据源.md)，模型语义统一见[Schema](../../docs/模块/Schema.md#2-建模方向与预览)。

Action 正文支持可选 `targetSchemaIds` 数组，为动作详情和 Schema 详情的关联发现保存最多32个同租户已生效 Schema 稳定 ID；空数组或省略表示未关联，不表示对所有 Schema 生效。保存草稿及发布均校验目标类型与状态，发布将目标写入当前依赖；已有 Action 可经草稿/发布修改这一关联，签名、实现和副作用仍保持原有变更限制。Function 不接受该字段。关联仅是定义元数据，不授予执行或实例权限，详细规则以 [Action](../../docs/模块/Action.md#当前对象关联协议2026-11-07) 为主要说明。

定义目录的 `view` 只接受 `current` 或 `draft`，未提供时保持读取当前态。草稿视图只返回实际有草稿的对象，包括从未生效的定义和生效后重新编辑的草稿；结果 `status` 为 `draft`，内容与 ETag 均来自该草稿。草稿目录同时检查全局及逐对象的 `definition.edit` 授权，当前目录保持 `definition.read` 授权；无权对象不会成为续页标记。目录仅用于当前身份可管理内容的发现，不将拥有当前定义读取权限等同于拥有草稿编辑权限。

## 测试与当前限制

默认存储迁入专用来源使用[独立离线工具](../../tools/storage_relocation/README.md)，不通过普通 HTTP 发布重绑。Metadata 提供固定清单的只读校验、整批当前定义及发布证据事务提交和提交后核验；Data 独立负责物理复制及 gate，协调器在两侧验证完成后才激活。完整范围、停机要求、拒绝条件与恢复规则统一见[默认存储迁入专用源](../../docs/发布与迁移.md#默认存储迁入专用源2026-11-07)，普通草稿与发布的绑定限制不因此放宽。

`go test ./...` 覆盖名称归一、定义/类型校验、身份隔离、错误关闭、并发凭证及 Data 操作确认。设置 `METADATA_TEST_DSN` 可运行真实 MySQL 测试，显式应用本模块迁移，并以随机租户验证生命周期、失败恢复、outbox 去重、中文名称多轮往返修改、同操作并发恢复和结构变更拒绝；测试不会删除既有数据。

对象关联回归 `TestMySQLActionTargetsLifecycleAndIsolation` 使用真实 MySQL Metadata 存储及隔离 Data 确认接口，覆盖 Action 关联创建、生效、ETag 冲突、跨租户/未生效/错误类型目标拒绝、发布前再次校验、目标变更及解除关联；测试只清理本次生成的随机租户。该用例不代表 Action 业务执行或 Data 实例写入已验收。

2026-11-07 的 `TestMySQLDefaultStorageRelocationAtomicityAndEvidence` 已通过真实 MySQL 定向验证，覆盖固定旧定义、整批原子提交及幂等证据，以及草稿、未恢复发布、绑定依赖、来源摘要变化、混合新旧态和证据篡改的拒绝；后续合法 Link 绑定不使已完成迁移证据失效。个人知识库实际迁移及只读核对见[场景记录](../../docs/应用场景/个人知识库.md#配置复核与专用来源迁移当前)，不据此宣称自动恢复或生产迁移验收通过。

设置 `IDENTITY_TEST_DSN` 运行身份管理的真实MySQL生命周期测试，额外设置 `IDENTITY_TEST_OPA_URL` 可连接真实OPA。用户组、会话吊销、策略草稿/生效、审计、登录限流及并发管理员保护的验证入口和结果见[identity](identity/README.md)与项目总览。管理员标记只开放租户内管理，不自动授予业务数据权限；初始管理员通过显式角色获得业务授权。当前提供本地账号、平面用户组和受限角色策略；外部身份目录、组嵌套、委托管理、逐行掩码和完整调用授权仍未交付。

发布操作身份独立于候选内容摘要：A→B→A 的最后一步创建新操作，只有候选已与当前内容相同时才清理相同草稿并幂等返回已有成功操作。并发重试按持久阶段推进，较早网络请求的迟到失败不能覆盖已提交或成功状态。

当前定义生命周期为草稿与生效态；定义停用、删除、移动、破坏性结构迁移及运行调用尚未开放。除上述 Action 对象关联与无绑定 Link 首次追加绑定外，无显式来源绑定的已生效定义仍只接受保留身份的展示修改；显式来源模型额外允许保留已有绑定、类型与约束的可空属性及索引追加，由Data继续校验实时来源。`schema_2_db`以Schema生成安全追加计划，`db_2_schema`以数据库目录为准验证已有列，不反向更改源结构。模式、主绑定、已有属性物理映射、类型与约束不能借追加接口改写。外部源当前支持读取及跨来源投影，实例组合写入明确拒绝；PostgreSQL数据源不表示Metadata平台存储已支持PostgreSQL。outbox 当前仅持久记录，没有消费者。发布故障须通过同一接口和已保存的并发凭证显式恢复；跨服务自动恢复及进行中操作的即时撤权尚未验收。
