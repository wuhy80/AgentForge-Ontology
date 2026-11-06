# AgentForge-Ontology

面向工业智能体（Industrial Agent）的本体管理与运行框架，使用 Go 开发。

当前已提供首批 Go Metadata、Data 服务及 React 中文工作台，包含公共协议、认证授权和 MySQL 存储模块。已实现范围及验证结果见[项目总览](docs/项目总览.md#当前实现与验证2026-11-06)。

本地开发需要 Go 1.22+、PowerShell 和运行中的 Docker Desktop。在仓库根目录执行：

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\start-dev.ps1
$token = powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\dev-token.ps1
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\check.ps1 -Integration
```

服务分别监听 `127.0.0.1:18080`、`127.0.0.1:18081`。启动脚本创建独立的开发数据库，显式迁移后启动服务；密钥仅保存在忽略的 `.cache/dev/`。本地示例主体为 `demo-tenant / developer`。完整使用方式见[开发指南](docs/开发指南.md)。

前端需要 Node.js 22.12+ 或 24。另开终端运行：

```powershell
Set-Location apps/web
npm.cmd ci
npm.cmd run dev
```

打开 `http://127.0.0.1:5173`，粘贴上方 `$token` 的值进入工作空间。前端接入真实服务，提供分类/分组、五类定义、草稿、预览和生效、可视化单模型查询及实例维护；凭证只存在内存，刷新后需要重新连接。工程说明见[前端](apps/web/README.md)。

Schema、Enum、Link、Action、Function 统一使用单份当前生效定义，不设置业务 version；用户通过中文业务名称维护定义和编写查询，内部引用、保存及执行使用稳定 ID，改名不改变已有绑定身份；修改经校验及必要迁移后替换当前定义。

- [文档入口](docs/README.md)：按模块组织，每份说明包含规则、方案、示例与验收。
- [项目总览](docs/项目总览.md)与[总体架构](docs/总体架构.md)：目标、阶段、模块边界及服务划分。
- 模块说明：[Schema](docs/模块/Schema.md)、[Enum](docs/模块/Enum.md)、[Function](docs/模块/Function.md)、[Action](docs/模块/Action.md)、[Link](docs/模块/Link.md)。
- [组织与隔离](docs/组织与隔离.md)：租户、分类、分组、分类内不重名、中文名称解析及稳定 ID 绑定。
- [权限](docs/权限/README.md)：集中维护行列过滤、Action/Function 限制、策略配置、引擎选型与验证资料；首批已接入 OPA 的受限结构化授权决策，完整 UCAST、调用授权和三库认证待后续实现。
- [Schema 建模](docs/模块/Schema.md)：db_2_schema 反向建模、schema_2_db 正向生效、分表与路由、索引与结构/差异/SQL 预览。
- [数据查询](docs/数据查询/README.md)：独立维护 [DataSet](docs/数据查询/DataSet.md) 公共查询语义、[OntologySQL](docs/数据查询/OntologySQL.md) 文本编译与[可视化查询编辑器](docs/数据查询/可视化查询编辑器.md)中文配置方案，各入口共用查询计划的设计仍为候选。
- [数据读写](docs/数据读写.md)：Schema 动态单条/批量 CRUD、对象与关系事务及大规模导入的候选设计。
- 公共机制：[存储引擎与数据库适配](docs/存储引擎.md)、[发布与迁移](docs/发布与迁移.md)、[执行与调度](docs/执行与调度.md)、[部署与运维](docs/部署与运维.md)、[性能与容量](docs/性能与容量.md)。
- [工业 Agent 功能规划](docs/industrial-agent-ontology-plan.md)：工业应用场景与远期能力参考。
