# AgentForge-Ontology

面向工业智能体（Industrial Agent）的本体管理与运行框架，计划使用 Go 实现。

当前仓库处于需求与架构规划阶段，尚未包含业务服务实现。

Schema、Enum、Link、Action、Function 统一使用单份当前生效定义，不设置业务 version；用户通过中文业务名称维护定义和编写查询，内部引用、保存及执行使用稳定 ID，改名不改变已有绑定身份；修改经校验及必要迁移后替换当前定义。

- [文档入口](docs/README.md)：按模块组织，每份说明包含规则、方案、示例与验收。
- [项目总览](docs/项目总览.md)与[总体架构](docs/总体架构.md)：目标、阶段、模块边界及服务划分。
- 模块说明：[Schema](docs/模块/Schema.md)、[Enum](docs/模块/Enum.md)、[Function](docs/模块/Function.md)、[Action](docs/模块/Action.md)、[Link](docs/模块/Link.md)。
- [组织与隔离](docs/组织与隔离.md)：租户、分类、分组、分类内不重名、中文名称解析及稳定 ID 绑定。
- [Schema 建模](docs/模块/Schema.md)：db_2_schema 反向建模、schema_2_db 正向生效、分表与路由、索引与结构/差异/SQL 预览。
- [数据查询](docs/数据查询/README.md)：独立维护 [DataSet](docs/数据查询/DataSet.md) 的中文名称编写、筛选、关联、多跳、聚合、分页及查询预览候选设计。
- [数据读写](docs/数据读写.md)：Schema 动态单条/批量 CRUD、对象与关系事务及大规模导入的候选设计。
- 公共机制：[存储引擎与数据库适配](docs/存储引擎.md)、[发布与迁移](docs/发布与迁移.md)、[执行与调度](docs/执行与调度.md)、[部署与运维](docs/部署与运维.md)、[性能与容量](docs/性能与容量.md)。
- [工业 Agent 功能规划](docs/industrial-agent-ontology-plan.md)：工业应用场景与远期能力参考。
