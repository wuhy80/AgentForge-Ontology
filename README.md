# AgentForge-Ontology

面向工业智能体（Industrial Agent）的本体管理与运行框架，计划使用 Go 实现。

当前仓库处于需求与架构规划阶段，尚未包含业务服务实现。

Schema、Enum、Link、Action、Function 统一使用单份当前生效定义，不设置业务 version，引用仅保存稳定 ID；修改经校验及必要迁移后替换当前定义。

- [文档入口](docs/README.md)：按模块组织，每份说明包含规则、方案、示例与验收。
- [项目总览](docs/项目总览.md)与[总体架构](docs/总体架构.md)：目标、阶段、模块边界及服务划分。
- 模块说明：[Schema](docs/模块/Schema.md)、[Enum](docs/模块/Enum.md)、[Function](docs/模块/Function.md)、[Action](docs/模块/Action.md)、[Link](docs/模块/Link.md)。
- [组织与隔离](docs/组织与隔离.md)：租户、分类、分组、分类内不重名及展示名与稳定 ID 分离。
- [Schema 建模](docs/模块/Schema.md)：db_2_schema 反向建模、schema_2_db 正向生效、分表与路由、索引与结构/差异/SQL 预览。
- [数据读写](docs/数据读写.md)：Schema 动态单条/批量 CRUD、对象与关系事务及大规模导入的候选设计。
- 公共机制：[存储引擎与数据库适配](docs/存储引擎.md)、[发布与迁移](docs/发布与迁移.md)、[执行与调度](docs/执行与调度.md)、[部署与运维](docs/部署与运维.md)、[性能与容量](docs/性能与容量.md)。
- [工业 Agent 功能规划](docs/industrial-agent-ontology-plan.md)：工业应用场景与远期能力参考。
