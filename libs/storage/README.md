# 共享存储模块

当前实现面向 Go 1.22 和 MySQL 8 及更新版本，使用 `database/sql`、GORM v2 和同一个连接池。PostgreSQL、达梦尚未实现，未知驱动明确拒绝。模块负责连接、事务及平台迁移设施，不包含五类定义、实例或任务的业务仓储。

```go
engine, err := storage.Open(ctx, storage.Config{
    Driver: "mysql", DSN: os.Getenv("MYSQL_DSN"),
    MaxOpen: 10, MaxIdle: 5,
})
if err != nil { return err }
defer engine.Close()
```

DSN 必须指向已有数据库，禁止 `multiStatements` 和 `interpolateParams`。连接超时默认 5 秒，时间解码采用 UTC；凭据不写入日志。`Open` 不建表、不执行迁移。池的默认上限为 10，默认空闲连接不超过 5，最大连接寿命为 5 分钟。注册表必须在首次打开前完成配置，此后只读。

`Engine.WithTx(ctx, func(*sql.Tx) error)` 创建 READ COMMITTED 本地事务，回调失败或 panic 时回滚。`Engine.WithSession(ctx, func(*Session) error)` 在同一个事务中混用参数化 SQL 和固定记录的 ORM `Create`，不会额外打开一个事务外连接。提交失败可能表示提交结果未知，应由业务用例按操作 ID 核对，不自动重做。

## 显式迁移

服务的迁移命令调用 `Migrate(ctx, engine.DB(), service, migrations)`；每个 `Migration{ID, SQL}` 仅包含一条由服务维护的可信语句。迁移按当前数据库及服务名取得 MySQL `GET_LOCK`，专用连接持续持锁并执行 DDL、写入 `af_storage_migrations`，最后在同一连接释放锁。锁等待上限为 5 秒。记录成功的迁移重跑不执行 SQL；修改已登记语句会因校验和冲突而拒绝。

MySQL DDL 不可由外层事务回滚。语句执行前先持久化 `running`，成功后才确认 `succeeded`；执行失败登记 `needs_repair`。再次遇到 `running` 或 `needs_repair` 会拒绝自动重跑，包括“DDL 已完成但成功记录丢失”的窗口。

恢复应先核对目标实际结构，必要时由受控运维流程向前修复，再调用 `CompleteMigration` 并提供检查完整目标结构的 verifier。该接口在同一迁移锁内检查、确认记录，不重新执行原 DDL。模块没有“忽略失败继续执行”或盲目标记成功的入口；应用启动不应自动调用恢复。

## 验证

```powershell
$env:GOWORK = 'off'
go test ./...
go vet ./...
```

离线测试使用专用 SQL driver fixture，验证 ORM/SQL 的真实连接绑定、共同提交/回滚、panic 回滚、迁移中断拒绝重放、校验和变化及结构核对失败。它不模拟或认证 MySQL 方言。

设置 `MYSQL_TEST_DSN` 后额外执行真实 MySQL 验证：ORM 与 SQL 同事务、两个迁移执行者争抢，以及待修复记录经结构检查后恢复。必须使用可丢弃的测试数据库；测试只创建随机命名的表，结束后删除这些表及相应测试迁移记录。未设置时两项测试明确跳过。以上测试不代表三库兼容性或性能验收。
