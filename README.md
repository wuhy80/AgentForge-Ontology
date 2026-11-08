# AgentForge-Ontology

通用本体（Ontology）管理与运行框架，使用 Go 开发。面向不同领域统一管理业务对象、属性、关系、动作与函数，为业务应用、知识管理和智能体提供语义基础。项目定位与范围见[项目总览](docs/项目总览.md)。

当前已提供首批Go Metadata、Data、Execution服务及React中文工作台，包含公共协议、认证授权、MySQL存储与Python、JavaScript、Go纯计算运行器；Function和Action各自提供源码编辑、高亮、补全与草稿试运行。已实现范围及验证结果见[项目总览](docs/项目总览.md#当前实现与验证2026-11-06)。

Linux 单机的 Git 拉取、HTTPS 入口、systemd、备份及本地数据迁移方式见[部署与运维](docs/部署与运维.md#0-linux-单机部署2026-11-08)；单机验证与生产容量、扩容及恢复验收分别记录。

本地开发需要支持自动工具链的Go（或预装Go 1.26.10）、PowerShell和运行中的Docker Desktop；当前模块基线及公网部署前置条件见[开发指南](docs/开发指南.md)和[部署与运维](docs/部署与运维.md)。在仓库根目录执行：

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\start-dev.ps1
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\check.ps1 -Integration
```

Metadata、Data、Execution 分别监听`127.0.0.1:18080`、`127.0.0.1:18081`、`127.0.0.1:18082`，MCP 为 `http://127.0.0.1:18084/mcp`。启动脚本创建独立的开发数据库，显式迁移并初始化首个账号后启动服务，并准备Python/Node镜像、构建默认Go专用镜像；自定义Go镜像须管理员预先准备，`-DependenciesOnly`不准备运行镜像。密钥与本地初始管理员信息仅保存在忽略的`.cache/dev/`。完整使用方式见[开发指南](docs/开发指南.md)。

前端需要 Node.js 22.12+ 或 24。另开终端运行：

```powershell
Set-Location apps/web
npm.cmd ci
npm.cmd run dev
```

打开 `http://127.0.0.1:5173`，使用租户、用户名和密码登录。首次初始化的本地管理员为 `demo-tenant / admin`，随机初始密码见 `.cache/dev/admin.json`；后续账号在“用户与权限”页面创建，可直接分配角色或通过用户组继承角色，再配置业务策略。会话在服务端可撤销，刷新可恢复，退出、停用、密码重置或组授权变化使旧会话失效；不再使用手工签发凭证登录。工程说明见[前端](apps/web/README.md)。

Schema、Enum、Link、Action、Function 统一使用单份当前生效定义，不设置业务 version；用户通过中文业务名称维护定义和编写查询，内部引用、保存及执行使用稳定 ID，改名不改变已有绑定身份；修改经校验及必要迁移后替换当前定义。

- [文档入口](docs/README.md)：按模块组织，每份说明包含规则、方案、示例与验收。
- [项目总览](docs/项目总览.md)与[总体架构](docs/总体架构.md)：目标、阶段、模块边界及服务划分。
- 模块说明：[Schema](docs/模块/Schema.md)、[Enum](docs/模块/Enum.md)、[Function](docs/模块/Function.md)、[Action](docs/模块/Action.md)、[Link](docs/模块/Link.md)。
- [组织与隔离](docs/组织与隔离.md)：租户、分类、分组、分类内不重名、中文名称解析及稳定 ID 绑定。
- [权限](docs/权限/README.md)：集中维护行列过滤、Action/Function限制、策略配置、引擎选型与验证资料；首批接入OPA受限决策及纯计算调用授权，完整UCAST、受控委托和三库认证待后续实现。
- [MCP 接入](docs/MCP接入.md)：Ontology 提供通用 MCP 服务、个人连接凭据与 OAuth；dsh 通过现有 Streamable HTTP 客户端连接。
- [Schema 建模](docs/模块/Schema.md)：db_2_schema 反向建模、schema_2_db 正向生效、分表与路由、索引与结构/差异/SQL 预览。
- [数据查询](docs/数据查询/README.md)：独立维护 [DataSet](docs/数据查询/DataSet.md) 公共查询语义、[OntologySQL](docs/数据查询/OntologySQL.md) 文本编译与[可视化查询编辑器](docs/数据查询/可视化查询编辑器.md)中文配置方案，各入口共用查询计划的设计仍为候选。
- [数据读写](docs/数据读写.md)：Schema 动态单条/批量 CRUD、对象与关系事务及大规模导入的候选设计。
- 公共机制：[存储引擎与数据库适配](docs/存储引擎.md)、[发布与迁移](docs/发布与迁移.md)、[执行与调度](docs/执行与调度.md)、[部署与运维](docs/部署与运维.md)、[性能与容量](docs/性能与容量.md)。
- 应用场景：[个人知识库](docs/应用场景/个人知识库.md)（当前试点）、[工业应用场景参考](docs/industrial-agent-ontology-plan.md)（历史规划与远期能力参考）。
