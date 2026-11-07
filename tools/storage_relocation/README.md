# 默认存储迁入专用数据源

这是显式离线维护命令，协调 Metadata 与 Data 各自拥有的校验、事务和迁移步骤，不接受任意 SQL，也不通过普通发布放宽已生效模型的重绑限制。

当前仅支持同租户、同分类的1至16个默认 MySQL `schema_2_db / single` 模型迁入同一显式专用 MySQL 数据源。属性全部映射到各自一张新主表，保留模型、属性、索引及实例 ID，不修改业务类型或约束；拒绝现有草稿、未恢复发布、已绑定的相关 Link、目标表接管或来源配置漂移。每表最多10,000行、64MiB，整批最多128MiB。完整迁移规则唯一维护在[定义生效与迁移](../../docs/发布与迁移.md#默认存储迁入专用源2026-11-07)。

先通过数据源公开 API 配置独立业务库和账号，再从权威当前定义生成候选清单；凭据通过以下环境变量传入，不写入清单、报告或提交文件：

- `ONTOLOGY_RELOCATION_METADATA_DSN`：现有 Metadata 控制库。
- `ONTOLOGY_RELOCATION_DATA_DSN`：现有 Data 控制库。
- `ONTOLOGY_DATA_SOURCE_KEY`：与运行服务相同的数据源加密密钥。

清单采用 `{"format":1,"items":[...]}`，每项结构见 [StorageRelocation](../../contracts/relocation.go)。`previous` 必须包含原生效定义、ETag及active状态；每个模型使用不同的新 `operationId`，`sourceETag` 固定来源配置摘要。候选定义新增主表绑定和逐属性映射，其他内容保持不变。清单中不能夹带密码或连接参数。先定稿摘要，再生成只读计划：

```powershell
# 在本目录执行；输入与输出均放在工作区的忽略目录。
go run . -manifest ../../.cache/relocation-candidate.json -seal ../../.cache/relocation-fixed.json
go run . -manifest ../../.cache/relocation-fixed.json -report ../../.cache/relocation-report.json
```

执行前备份准确的当前定义、控制行及实例原始内容，并停止本地 Metadata/Data 服务。命令检查本机监听端口并持有两控制库的租户维护锁；这不替代部署端对其他副本、外部写入方和网络入口的停机隔离。仅在本地单实例维护范围使用：

```powershell
go run . -manifest ../../.cache/relocation-fixed.json -report ../../.cache/relocation-report.json -apply -offline
```

Data 在同一事务关闭整批 gate，排空已准入事务，再复用现有 DDL 编译器创建目标表和索引，复制实例并校验全部字段。目标步骤绑定完整固定清单，不能以子集恢复。Metadata 在本地事务中提交整批当前态、成功发布记录和迁移证据；最后再次核对后整批激活 gate。报告原子记录 `planned → data_prepared → metadata_committed → complete`。

失败后保留停机，使用同一份固定清单及报告重试；不删除或重写原表，不覆盖内容不一致的目标表。命令不提供自动回滚或生产恢复保证。报告完成后，在维护范围内启动服务并进行公开 API/数据核对；核对通过后结束维护。

迁移保持显式源现有只读边界，不增加实例写入或跨源事务能力。知识库导入器会在业务写入前明确拒绝显式映射；全字段只读核对方式见[知识库工具](../knowledge_base/README.md)。Link须在对象迁移完成后另外通过现有公开API配置并发布。
