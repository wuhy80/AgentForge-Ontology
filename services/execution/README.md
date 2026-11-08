# Execution：首批 Python 调用

本服务拥有 Function/Action 调用与执行记录，使用独立 MySQL 数据库。Metadata 继续拥有当前定义、账号、会话和策略；服务通过窄 HTTP 接口读取，不查询其他服务的私有表。公共运行设计见[执行与调度](../../docs/执行与调度.md)，权限规则见[权限设计](../../docs/权限/权限设计.md)，源码与参数协议见 [contracts](../../contracts/execution.go)。

## 当前能力

- 同步执行已生效、`sideEffects: none`、配置`runtime`的Python Function及直接源码Action。Action也可使用`functionId`绑定签名相同的已生效Function；绑定双方的中文参数名、类型、空值、默认值和必填语义须一致，参数ID可以各自独立。Action的内联源码、函数引用和制品引用互斥。
- Python入口由作者声明，工作台初始示例为`main`，接受并返回以声明中文参数名为键的字典。HTTP输入、输出使用稳定参数ID。int64、decimal始终以字符串传递，Python计算可显式使用`int`、`decimal.Decimal`，输出再转为字符串。
- 输入输出支持 string、text、longtext、uuid、int64、decimal、boolean、timestamp；单次输入/输出仍受 256 KiB 总预算。枚举引用、存储映射与复杂参数类型明确拒绝。可选输入缺省时不注入键，有默认值则使用默认值；null 仅在 nullable 时接受。
- 执行记录按租户与发起用户隔离；记录真实调用目标、实际源码、定义上下文、输入、源码SHA-256与实际容器image ID。直接源码Action保存自己的实现快照，绑定Action另外保存Function上下文。HTTP仅返回状态、输出、摘要及安全错误，不返回源码、原始输入、stdout、stderr或traceback。

未开放JavaScript、自定义镜像、容器业务程序、HTTP/gRPC外部实现、第三方依赖安装、网络访问、业务数据读写、模型API调用、事件/周期调度、后台队列、自动重试和调用委托。既无runtime也未绑定可执行Function的定义仍可登记；业务调用会明确拒绝。

## HTTP 入口

| 方法和路径 | 用途 |
| --- | --- |
| `GET /healthz` | 进程存活 |
| `GET /readyz` | 存储表、Docker 与本地 Python 镜像就绪 |
| `GET /execution/capabilities` | 已登录用户读取运行器实际可用状态；`actionInlineSource=true`表示支持Action直接源码 |
| `POST /execution/invocations` | 同步发起计算，必须带 `Idempotency-Key` |
| `GET /execution/invocations/{id}` | 当前发起用户按 ID 核对状态与读取结果 |

能力响应中的`actionRequiresFunctionInvoke=true`是旧兼容字段，仅指绑定Function的Action；直接源码Action的支持由`actionInlineSource`表达。

公共入口沿用HttpOnly Cookie、Origin与CSRF规则。服务凭据不能替代用户调用。Function、直接源码Action、绑定Action的调用授权统一见[权限设计](../../docs/权限/权限设计.md#首批纯计算调用2026-11-07已实现)；本服务在受理、启动及结果交付时执行对应检查，不根据源码编辑权限推导调用许可。未自动为已有租户策略追加授权。

受理、实际启动、结果交付、GET 和幂等重放均复核当前会话、当前权限和当前定义。退出注销即使账号仍启用，也不能交付旧会话的结果。定义上下文变化后，旧结果返回 `409 definition_changed`，保守拒绝整份结果；不按旧文本重新解析同名定义。此检查不等于跨服务原子即时撤权。

POST 请求使用 `contracts.InvocationRequest`，例如：

```json
{"definitionId":"已生效的定义稳定ID","input":{"输入参数稳定ID":"9007199254740993.10"}}
```

首次完成返回 201，同键同请求重放返回 200，相同请求仍运行时返回 202；内容变化返回 `409 idempotency_conflict`。键限定 1～128 个字母、数字、`.`、`_`、`:` 或 `-`，在租户与发起用户范围内唯一。新计算使用新键，网络不确定时保留原键，不能为了重试生成新键。

每个进程最多同时执行 4 个计算，不排队；超限在保存执行记录前返回 429，可稍后使用原键重试。记录先持久保存为 running，之后成为 succeeded、failed、timed_out 或 canceled。浏览器断开仍以独立短事务上下文保存已知终态。写入结果不确定时只能用原键/ID核对，不自动再跑。

进程中断留下的 running 记录在执行时限加90秒后由当前有权读取它的请求标记为 interrupted，终态不可被迟到进程覆盖；这不是自动恢复执行。平台不自动删除历史记录或实现生产备份/保留策略。

## Python 容器

运行器只使用服务管理员配置的本地 Linux Python 镜像，默认 `python:3.12-alpine`；每次执行先解析实际 image ID 并按该 ID 创建容器，使用 `--pull=never`，不会因用户定义下载镜像或安装依赖。Python 用 `-I -S -u` 启动，代码及输入通过 stdin 传入，不挂载宿主目录、不传入服务凭据或宿主环境变量。

容器固定使用非 root UID 65534、只读根目录、禁用网络、移除全部 Linux capabilities、no-new-privileges、64 PID、128 MiB 内存及1 CPU，临时目录为8 MiB tmpfs。代码、入口与超时上限见契约校验；运行时间限制为1～30秒。普通 print 输出被丢弃，直接输出流也有预算；错误不回传用户异常文本。

容器分创建与附加启动两步；请求超时/取消后先停止 Docker 客户端，再以独立上下文确认回收该唯一容器。清理无法确认时返回安全错误并暂停该运行器接收新任务，后续 Ready 检查重试回收。容器带固定模块标记与期限，Ready 也回收本模块超过期限的遗留容器，不处理其他模块或仍在期限内的其他副本任务。

这套受限容器已提供本批隔离机制；容器仍共享宿主内核，不代表经过生产认证的恶意代码沙盒。进程或 daemon 不可用期间无法承诺立即清理，恢复检查必须成功才重新开放计算。

## 构建与启动

```powershell
$env:GOWORK='off'
go test ./...
go vet ./...
go run ./cmd/server -migrate
go run ./cmd/server
```

只有显式 `-migrate` 应用本服务平台表，普通启动及请求不迁移。配置：

| 变量 | 含义 |
| --- | --- |
| `ONTOLOGY_MYSQL_DSN` | 本服务独立 MySQL 数据库 |
| `ONTOLOGY_METADATA_URL` | Metadata 地址 |
| `ONTOLOGY_SERVICE_TOKEN` | 至少32字节内部服务凭据 |
| `ONTOLOGY_LISTEN` | 默认 `127.0.0.1:18082` |
| `ONTOLOGY_PYTHON_IMAGE` | 已准备的可信镜像，建议正式环境固定内容摘要 |
| `ONTOLOGY_DOCKER_BINARY` | 可选的可信 Docker CLI 路径 |

## 验证

模块测试涵盖稳定参数映射、精确数值、类型拒绝、Action 双重授权、启动/结果/幂等重放的当前身份权限检查、源码变更后旧结果拒绝、并发限制、过期记录终态及容器清理失败阻断/恢复。

`EXECUTION_TEST_DSN` 开启真实 MySQL 隔离测试，以随机租户检查唯一并发认领、重开仓库读取、用户/租户隔离及终态不可覆盖，只清理该次随机租户。`EXECUTION_TEST_DOCKER=1` 开启真实容器测试，检查 Python 精确计算、非 root、只读目录、网络限制、无凭据、异常、超时、取消与输出预算；使用相同镜像配置。测试入口不自动下载镜像。

跨服务真实 MySQL、OPA、会话与 Docker 的 HTTP 验证位于 [tests/integration](../../tests/integration)。模块测试或容器测试通过不代表完整调度、委托、生产隔离、三库或性能验收。
