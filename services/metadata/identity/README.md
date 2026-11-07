# 本地身份与权限管理

本包拥有租户账号、角色绑定、会话、当前权限策略/草稿及审计。使用 `identity.NewStore(db, opa, origin)`，将 `store.NewHandler()` 接入公开 `/metadata/auth/`、`/metadata/admin/`；所有管理接口自行检查当前 cookie 会话与管理员标记。`identity.Migrations` 须追加到 Metadata 迁移列表并显式执行，普通请求不建表。

`store.Bootstrap(ctx, tenant, username, password)` 只初始化空租户，重复执行不覆盖任何已有账号或密码。初始管理员有独立的管理员标记及显式业务授权角色，管理员标记本身不授予业务数据权限。密码为12–72字节，经 bcrypt 存储；用户管理不提供跨租户入口、自助注册或任意认证身份切换。

登录返回 `contracts.Session`，随机会话写入 HttpOnly、SameSite=Strict、Path=/ 的 `ontology_session` cookie，HTTPS 来源设置 Secure；数据库只保存会话摘要。有效期8小时，每次使用读取当前账号和角色，账号停用、密码/角色/管理员变化吊销旧会话。修改请求须带 `X-CSRF-Token`，浏览器 Origin 须匹配构造器配置；公共服务中间件还要求明确 Origin，CLI 调用同样提交工作台 Origin。自助改密吊销全部旧会话，再返回新会话及 CSRF。

管理路由包括 users、roles、policy、policy/publish、simulate 和 audit；用户/角色列表返回 `{data:[]}`，超过2000条明确失败。审计仅返回最近200条，以 `{data:[],hasMore:true|false}` 明确是否还有更早记录。修改用户/角色及策略保存/生效须带双引号包围的强 If-Match，冲突返回412。策略草稿不改变当前授权，生效、用户及角色变更在同一事务更新授权状态并记录无密码/会话值的审计。策略模拟不执行数据读写。

`store.CheckSession` 可供平台中间件或内部受服务凭据保护的身份接口使用；`store.Authorize` 从当前数据库读取启用账号、角色和策略，交给 `platform.EvaluateRuntimePolicy` 验证及 OPA 求值。拒绝未知操作、角色、跨租户资源、非当前Schema绑定、未知字段和不匹配值类型；不提供 Link/Action/Function 运行授权。允许规则支持静态字段及受控行条件，拒绝规则仅支持无条件整操作；不能安全合并的多角色条件显式拒绝。

`go test ./identity` 运行类型与预算回归。设置 `IDENTITY_TEST_DSN` 启用真实 MySQL 测试；可额外设置 `IDENTITY_TEST_OPA_URL` 连接真实 OPA，否则测试使用受控 HTTP 决策桩。测试显式应用迁移并创建随机租户，不删除已有业务数据。当前登录限流为每进程最多1024组账号/租户/来源计数，5次失败后拒绝15分钟；多副本统一限流、外部身份目录和生产会话运维继续待后续实施。
