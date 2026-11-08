# Execution：Python、JavaScript 与 Go 调用

本服务拥有 Function/Action 调用与执行记录，使用独立 MySQL 数据库。Metadata 继续拥有当前定义、账号、会话和策略；服务通过窄 HTTP 接口读取，不查询其他服务的私有表。公共运行设计见[执行与调度](../../docs/执行与调度.md)，权限规则见[权限设计](../../docs/权限/权限设计.md)，源码与参数协议见 [contracts](../../contracts/execution.go)。

## 当前能力

- 同步执行已生效、`sideEffects: none`、配置`runtime`的Python、JavaScript、Go Function及直接源码Action。Action也可使用`functionId`绑定签名相同的已生效Function；绑定双方的中文参数名、类型、空值、默认值和必填语义须一致，参数ID可以各自独立。Action的内联源码、函数引用和制品引用互斥。
- 入口由作者声明：Python默认`main(inputs)`，JavaScript默认同步`main(inputs)`，Go默认`Main(inputs map[string]any) (map[string]any, error)`；Go源码为含标准库导入的完整`package main`文件。三种语言均接受并返回以声明中文参数名为键的对象，HTTP输入、输出使用稳定参数ID。int64、decimal始终以字符串传递，精确计算及输出转换由作者显式完成。
- 输入输出支持 string、text、longtext、uuid、int64、decimal、boolean、timestamp；单次输入/输出仍受 256 KiB 总预算。枚举引用、存储映射与复杂参数类型明确拒绝。可选输入缺省时不注入键，有默认值则使用默认值；null 仅在 nullable 时接受。
- 执行记录按租户与发起用户隔离；记录真实调用目标、实际源码、定义上下文、输入、源码SHA-256与实际容器image ID。直接源码Action保存自己的实现快照，绑定Action另外保存Function上下文。HTTP仅返回状态、输出、摘要及安全错误，不返回源码、原始输入、stdout、stderr或traceback。
- 已保存的Function/Action草稿可独立试运行；请求显式携带草稿`draftEtag`，不替换当前态，也不能运行任意客户端定义。未发布的纯计算草稿同样可用。草稿与当前态共用隔离容器、并发、参数校验和持久化协议，记录以`mode: draft/current`区分。
- `Callable.testCases`随定义草稿保存，发布后随当前定义可见；最多10项，总JSON最多64KiB，名称最多80个Unicode字符且不重复。样例输入使用参数稳定ID，可部分填写，已填写值须满足标量传输声明；运行时仍检查必填与默认值，不将样例当作执行授权。

未开放用户自定义镜像、容器业务程序、HTTP/gRPC外部实现、第三方依赖安装、网络访问、业务数据读写、模型API调用、事件/周期调度、后台队列、自动重试和调用委托。既无runtime也未绑定可执行Function的定义仍可登记；业务调用会明确拒绝。

## HTTP 入口

| 方法和路径 | 用途 |
| --- | --- |
| `GET /healthz` | 进程存活 |
| `GET /readyz` | 存储表、Docker 与至少一种本地运行镜像就绪 |
| `GET /execution/capabilities` | 已登录用户读取运行器实际可用状态；`actionInlineSource=true`表示支持Action直接源码 |
| `POST /execution/invocations` | 同步发起计算，必须带 `Idempotency-Key` |
| `GET /execution/invocations/{id}` | 当前发起用户按 ID 核对状态与读取结果 |
| `POST /execution/source-tools` | 对已保存且可编辑的Function/Action临时Go源码解析或格式化，不执行、不保存 |

能力响应中的`actionRequiresFunctionInvoke=true`是旧兼容字段，仅指绑定Function的Action；直接源码Action的支持由`actionInlineSource`表达。

`draftInvocation`表示已保存草稿试运行实际可用；`sourceDiagnostics`与`testCases`说明源码坐标诊断、样例协议已接入。

`supportedRuntimes`固定列出`python/javascript/go`；`runtimes`仅列实际镜像及存储可用的语言，缺少单语言镜像不阻止其他语言调用，所选语言不可用时返回503而不会降级。`sourceTools`仅在Go运行器可用时列出`go`。

Go工具请求为`{"definitionId":"已保存定义ID","operation":"check或format","runtime":{"language":"go","source":"完整源码","entrypoint":"Main","timeoutSeconds":30}}`，响应含`source`与`diagnostics`。服务读取真实当前定义或草稿确认同租户Function/Action及`definition.edit`权限，无需invoke；交付前复查会话及编辑权限。未保存定义先保存草稿。工具通过容器内`go/parser`和`go/format`处理，不编译、不检查类型、不调用入口、不写草稿；固定10秒，与计算共用并发名额。JS静态工具由工作台本地实现，不调用此接口。

公共入口沿用HttpOnly Cookie、Origin与CSRF规则。服务凭据不能替代用户调用。Function、直接源码Action、绑定Action的调用授权统一见[权限设计](../../docs/权限/权限设计.md#首批纯计算调用2026-11-07已实现)；本服务在受理、启动及结果交付时执行对应检查，不根据源码编辑权限推导调用许可。未自动为已有租户策略追加授权。

MCP 外部 Bearer 另允许读取执行能力、发起当前态纯计算及读取本人结果，由 Metadata 核验 `ontology:invoke` 范围和真实用户；执行仍复用上述业务授权、幂等及交付前复核。外部凭据不允许草稿试运行或源码工具，禁止与 Cookie 混用，协议与配置见[MCP接入](../../docs/MCP接入.md)及[身份服务](../metadata/identity/README.md#外部连接与-mcp-oauth2026-11-08-已接入)。

受理、实际启动、结果交付、GET 和幂等重放均复核当前会话、当前权限和当前定义。退出注销即使账号仍启用，也不能交付旧会话的结果。定义上下文变化后，旧结果返回 `409 definition_changed`，保守拒绝整份结果；不按旧文本重新解析同名定义。此检查不等于跨服务原子即时撤权。

POST 请求使用 `contracts.InvocationRequest`，例如：

```json
{"definitionId":"已生效的定义稳定ID","input":{"输入参数稳定ID":"9007199254740993.10"}}
```

草稿请求在同一结构增加`"draftEtag":"已保存草稿的64位小写SHA-256凭证"`。Execution通过只读服务身份从Metadata精确GET草稿，同时检查真实调用者的`definition.edit`与根定义调用权限；绑定Action还检查其当前实现Function的调用权限。元数据服务凭据只允许读定义，不能写草稿或发布。ETag不一致返回`409 draft_changed`，读取结果和幂等重放同样重查；草稿发布后已不存在，旧草稿结果返回404。试运行不绕过发布时结构兼容与依赖校验。

失败响应可含`diagnostics`数组，返回受控`code/message`与源码内`line/column/endLine/endColumn`。行列从1开始，列按Unicode码点计数，非UTF-8字节；Python语法错误可给出范围，执行异常只给最深的用户源码行。坐标越界被丢弃，原异常文本、堆栈和源码片段不会返回；超时等没有可靠源码位置的错误不伪造行号。

首次完成返回 201，同键同请求重放返回 200，相同请求仍运行时返回 202；内容变化返回 `409 idempotency_conflict`。键限定 1～128 个字母、数字、`.`、`_`、`:` 或 `-`，在租户与发起用户范围内唯一。新计算使用新键，网络不确定时保留原键，不能为了重试生成新键。

每个进程最多同时执行 4 个计算，不排队；超限在保存执行记录前返回 429，可稍后使用原键重试。记录先持久保存为 running，之后成为 succeeded、failed、timed_out 或 canceled。浏览器断开仍以独立短事务上下文保存已知终态。写入结果不确定时只能用原键/ID核对，不自动再跑。

进程中断留下的 running 记录在执行时限加90秒后由当前有权读取它的请求标记为 interrupted，终态不可被迟到进程覆盖；这不是自动恢复执行。平台不自动删除历史记录或实现生产备份/保留策略。

## 语言容器

运行器只使用管理员准备的本地Linux镜像：Python默认`python:3.12-alpine`，JavaScript默认`node:22-alpine`，Go默认`agentforge-go:1.25`。每次执行先解析实际image ID并按该ID创建容器，使用`--pull=never`，不会因用户定义下载镜像或安装依赖。代码及输入通过stdin传入，不挂载宿主目录、不传入服务凭据或宿主环境变量。

所有语言固定使用非root UID65534、只读根目录、禁用网络、移除Linux capabilities、no-new-privileges及1 CPU。Python/JS为64 PID、128 MiB内存、8 MiB不可执行tmpfs；Go编译与执行为128 PID、768 MiB内存、384 MiB可执行tmpfs。代码、入口与超时上限见契约校验；源码最多64KiB，运行时间1～30秒，Go预算包含编译。普通日志被丢弃，结果256KiB、诊断16KiB；错误不回传用户异常、编译器原文或路径。

Python使用`-I -S -u`。JS入口使用ECMAScript内建与静默console，不开放require/process、Node模块、ES模块或npm；只接受普通对象及标量JSON值，拒绝Promise、undefined、BigInt、NaN/Infinity、Date、数组、函数、Symbol键及getter，避免序列化静默丢失。

Go镜像的可信启动器在容器内校验`package main`、标准库导入与入口，拒绝main/init、平台保留标识、Go/line指令、cgo及非标准库。容器内复制镜像预热缓存到本次私有tmpfs后编译执行，子进程日志从包初始化起即丢弃；输出通过单独描述符传递。Go输入普通JSON数字为`json.Number`，int64/decimal仍为字符串。完整源码协议及示例统一见[Function](../../docs/模块/Function.md)。

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
| `ONTOLOGY_JAVASCRIPT_IMAGE` | 已准备的Node22镜像，默认`node:22-alpine` |
| `ONTOLOGY_GO_IMAGE` | 含可信启动器的Go镜像，默认`agentforge-go:1.25` |
| `ONTOLOGY_DOCKER_BINARY` | 可选的可信 Docker CLI 路径 |

在仓库根目录显式准备默认镜像：`docker pull python:3.12-alpine`、`docker pull node:22-alpine`、`docker build -t agentforge-go:1.25 -f deploy/execution/go/Dockerfile deploy/execution/go`。`scripts/start-dev.ps1`包含这些准备步骤；`-DependenciesOnly`只启动数据库/OPA。管理员自选Go镜像须预先准备，普通服务启动和请求不构建镜像。

## 验证

模块测试涵盖稳定参数映射、精确数值、类型拒绝、Action 双重授权、启动/结果/幂等重放的当前身份权限检查、源码变更后旧结果拒绝、并发限制、过期记录终态及容器清理失败阻断/恢复。

`TestExternalConnectionRejectsDraftExecutionReplayAndResults` 验证同一用户的外部凭据不能执行草稿、重放工作台草稿请求或读取其结果，当前态计算及结果仍可用；MCP 真实服务测试另验证直接 Execution 请求的草稿拒绝。

草稿专项覆盖未发布Function、Action绑定当前Function、edit与invoke联合授权、ETag变更/发布后的拒绝、当前态不受草稿影响、诊断坐标和受控消息；Metadata/identity测试覆盖样例保存发布与对应类型草稿资源授权。

`EXECUTION_TEST_DSN` 开启真实 MySQL 隔离测试，以随机租户检查唯一并发认领、重开仓库读取、用户/租户隔离及终态不可覆盖，只清理该次随机租户。`EXECUTION_TEST_DOCKER=1` 开启真实三语言容器测试，检查精确值、隔离、日志丢弃、严格标量输出、安全诊断、超时、取消、Go源码限制和解析格式化；使用相同镜像配置。测试入口不自动下载镜像。

跨服务真实 MySQL、OPA、会话与 Docker 的 HTTP 验证位于 [tests/integration](../../tests/integration)。模块测试或容器测试通过不代表完整调度、委托、生产隔离、三库或性能验收。
