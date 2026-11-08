# 本地身份与权限管理

本包拥有租户账号、身份用户组、角色绑定、会话、当前权限策略/草稿及审计。使用 `identity.NewStore(db, opa, origin)`，将 `store.NewHandler()` 接入公开 `/metadata/auth/`、`/metadata/admin/`；所有管理接口自行检查当前 cookie 会话与管理员标记。`identity.Migrations` 须追加到 Metadata 迁移列表并显式执行，普通请求不建表。013/014 迁移新增身份用户组及成员关联表，原有迁移保持不变。

`store.Bootstrap(ctx, tenant, username, password)` 只初始化空租户，重复执行不覆盖任何已有账号或密码。初始管理员有独立的管理员标记及显式业务授权角色，管理员标记本身不授予业务数据权限。密码为12–72字节，经 bcrypt 存储；用户管理不提供跨租户入口、自助注册或任意认证身份切换。

登录返回 `contracts.Session`，随机会话写入 HttpOnly、SameSite=Strict、Path=/ 的 `ontology_session` cookie，HTTPS 来源设置 Secure；数据库只保存会话摘要。有效期8小时，每次使用读取当前账号和角色，账号停用、密码/角色/管理员变化吊销旧会话。修改请求须带 `X-CSRF-Token`，浏览器 Origin 须匹配构造器配置；公共服务中间件还要求明确 Origin，CLI 调用同样提交工作台 Origin。自助改密吊销全部旧会话，再返回新会话及 CSRF。

管理路由包括 users、user-groups、roles、policy、policy/publish、simulate 和 audit；用户/角色列表返回 `{data:[]}`，超过2000条明确失败。审计仅返回最近200条，以 `{data:[],hasMore:true|false}` 明确是否还有更早记录。修改用户/用户组/角色及策略保存/生效须带双引号包围的强 If-Match，缺失返回428、冲突返回412。策略草稿不改变当前授权，生效、用户及角色变更在同一事务更新授权状态并记录无密码/会话值的审计。策略模拟不执行数据读写。

## 外部连接与 MCP OAuth（2026-11-08 已接入）

显式执行 Metadata 迁移 `015_identity_access_connections`、`016_identity_oauth_clients`、`017_identity_oauth_codes` 后，已登录用户可通过 `GET/POST /metadata/auth/connections` 查看或创建个人外部连接，`DELETE /metadata/auth/connections/{id}` 撤销。创建请求为 `{name,scopes,expiresInSeconds}`，有效期限制为1小时至30天，范围固定为 `ontology:discover`、`ontology:query`、`ontology:invoke`；query/invoke 必须同时包含 discover。成功创建只在响应中返回一次 `mcp_<64位小写十六进制>`，数据库只保存摘要及用户/有效角色快照，列表永不返回密钥。凭据绑定租户和用户，账号停用、用户 ETag/直接角色/启用用户组角色变化、过期或撤销后失效；每次请求仍复核当前服务端授权。外部 Bearer 仅允许 MCP 所需的公开能力路由，不接受 Cookie 与 Bearer 混用，也不能调用管理、编辑、草稿、数据源或源码工具。

OAuth 授权服务器端点由 `ONTOLOGY_AUTH_ISSUER` 配置（缺省为工作台来源），MCP 资源由 `ONTOLOGY_MCP_RESOURCE` 配置（缺省 `http://127.0.0.1:18084/mcp`）。`GET /.well-known/oauth-authorization-server` 发布 issuer、authorize/token/register/revoke、S256 PKCE 及三类范围；`POST /oauth/register` 只接受 HTTPS 或 localhost/127.0.0.1/[::1] 回环 HTTP 回调且不允许用户信息、查询或片段。授权码流程仅支持 `authorization_code`，授权码一次性、5分钟有效，Token endpoint 必须校验客户端、精确回调地址、S256 verifier、资源受众及用户/角色快照；access token 有效1小时，refresh token 明确不支持。浏览器同意页面使用 `POST /metadata/auth/oauth/preview` 和 `POST /metadata/auth/oauth/authorize`，均沿用当前会话、Origin 与 CSRF，服务端返回已校验的 `redirectUri`。MCP 网关应代理 OAuth 公开端点，并在下游按真实用户权限复核，不能以服务凭据代替用户。

2026-11-08，`TestMySQLExternalConnectionsAndOAuth` 已通过真实 MySQL 验证连接 scope、目录桥、跨租户撤销拒绝、资源绑定、PKCE、授权拒绝、授权码过期/重放，以及角色变化和账号停用失效；官方 SDK 与真实浏览器结果统一见[MCP接入](../../../docs/MCP接入.md#6-验证与验收)。

身份用户组支持 `GET/POST /metadata/admin/user-groups`、`GET/PUT /metadata/admin/user-groups/{id}`，列表为 `{data:[]}`，单项使用 `contracts.UserGroup`（id、name、description、enabled、roleIds、userIds、etag），写入使用 `contracts.UserGroupInput`。名称按 NFC 与大小写折叠在当前租户用户组内唯一，名称冲突返回409；用户组、账号、角色各自拥有名称空间。该身份目录与 Metadata 定义组织的 `Group` 独立。当前不提供嵌套、删除或自助加组，未知字段返回400；成员与角色必须属于当前租户，停用账号可登记为成员但不获得授权。组成员关系只在用户组入口维护，用户与会话的 `roleIds` 始终仅表示直接角色。

实际授权与策略模拟共用有效角色计算：直接角色加上全部启用用户组角色，去重后最多64个；每个用户最多加入64组，停用组同样计入。每组最多64角色、256成员；每租户最多2000用户组、20000条成员引用。组写入在租户事务锁内验证完整结果与用户合并预算，直接角色更新也检查合并预算；超限返回422并回滚整个事务，不静默截断。组列表在超过目录预算时同样返回422。组不授予管理员标记，不能安全合并的多组/多角色字段与条件授权仍明确拒绝。

创建用户组，或修改启停状态、角色集合、成员集合，在同一事务更新授权状态、写入 `user_group.create/update` 审计并吊销旧/新成员并集的全部会话。管理员将自己加入组也会吊销自己的旧会话。仅修改名称和说明时保留会话及授权状态，更新 ETag 并记录审计；角色和成员按排序后的集合比较，单纯重排不吊销会话。事务写锁同时约束跨组并发变更，避免分别通过校验后共同突破有效角色或成员组数预算。

`store.CheckSession`可供平台中间件或内部受服务凭据保护的身份接口使用；`store.Authorize`从当前数据库读取启用账号、有效角色和策略，交给`platform.EvaluateRuntimePolicy`验证及OPA求值。拒绝未知操作、角色、跨租户资源、无效定义绑定、未知字段和不匹配值类型；Link支持独立的`link.query/create/delete`静态授权，纯计算调用新增`function.invoke`、`action.invoke`并严格绑定资源类型，不接收参数字段或行条件。调用规则可绑定同租户已有当前态或草稿的对应Function/Action，Link仍须绑定已生效定义；能否执行业务当前态或草稿由Execution分别检查。已有租户不自动补权，Action同时检查绑定Function调用权；完整规则统一见[权限设计](../../../docs/权限/权限设计.md)。实例允许规则支持静态字段及受控行条件，拒绝规则仅支持无条件整操作；不能安全合并的多角色条件显式拒绝。

`go test ./identity` 运行类型与预算回归。设置 `IDENTITY_TEST_DSN` 启用真实 MySQL 测试；可额外设置 `IDENTITY_TEST_OPA_URL` 连接真实 OPA，否则测试使用受控 HTTP 决策桩。测试显式应用迁移并创建随机租户，不删除已有业务数据。用户组回归覆盖授权/模拟一致、显式拒绝与不兼容字段集合、停用账号、跨租户、管理员边界、ETag、成员会话撤销、描述修改保留会话、256成员/64有效角色/64组/2000组/20000成员引用预算，以及同组和跨组并发事务。当前登录限流为每进程最多1024组账号/租户/来源计数，5次失败后拒绝15分钟；多副本统一限流、外部身份目录和生产会话运维继续待后续实施。
