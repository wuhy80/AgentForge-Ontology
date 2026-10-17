# Action：业务操作与效果

状态：模块说明草案，尚未实现。本文统一维护业务操作定义、参数、效果、权限、实现方式及验收；具体接口和事务边界待确认。

## 1. 用途与边界

Action 表达对目标对象执行的业务操作，例如创建维护工单、更新业务状态或触发外部处理。定义应描述参数、目标、预期效果、权限及执行要求，实际运行状态保存在 Execution。

建议 Action 可调用 [Function](Function.md)，也可通过共同运行器协议绑定脚本、容器或外部服务。Action 与 Function 均支持多语言、同步和异步，区别主要在业务语义；不能仅靠名称判断读写、副作用或重试安全性。

Metadata 的 action 模块维护定义及引用，Data 管理实例/关系，Execution 管理任务和尝试，Worker 执行代码。Action 不能绕过其他服务的接口直接修改其私有表。

## 2. 建议定义内容

| 内容 | 用途 |
| --- | --- |
| id、version、labels、description | 稳定标识及业务用途 |
| target | 允许操作的 Schema、对象或业务范围 |
| input、output | 参数、结果及单位/Enum 语义，复用 Schema 描述 |
| preconditions、effects | 业务前置条件、预期数据变化和外部副作用 |
| permissions | 调用者和执行身份需要具备的权限 |
| implementation | 固定版本的 Function 引用，或运行器清单 |
| idempotency、retry、compensation | 去重、可重试条件、部分成功时的处理规则 |

下面用一个外部工单操作演示结构，所有字段为候选协议，具体规则表达式与执行方式尚待设计。

```json
{
  "id": "create-maintenance-order",
  "version": "1.0.0",
  "description": "为指定设备创建维护工单",
  "target": {"schemaId": "motor", "schemaVersion": "1.0.0"},
  "input": {"reason": {"dataType": "string", "required": true}},
  "output": {"workOrderId": {"dataType": "string"}},
  "effects": [{"kind": "externalWrite", "system": "eam", "operation": "createWorkOrder"}],
  "permissions": ["maintenance-order:create"],
  "implementation": {"kind": "function", "id": "eam-create-work-order", "version": "1.0.0"},
  "idempotency": {"required": true},
  "retry": {"requiresIdempotentReceiver": true}
}
```

引用的 Function 必须实际声明并获准执行外部写入，不能将其伪装成无副作用计算。目标对象标识与幂等键属于一次调用的输入/上下文，具体载体由调用协议统一定义。

## 3. 执行流程

建议流程：解析目标与版本 → 校验身份、参数及业务前置条件 → 创建持久执行记录 → 按固定实现执行 → 验证效果或保存待核对状态 → 返回结果。

前置检查和实际写入之间可能有并发变化。需要强一致的条件应由实际数据所有者在事务或条件写入时再次验证，不能把入口检查当成跨服务原子保证。

同步/异步调用、任务 ID、状态、通知、超时、取消、租约及尝试标识统一见 [执行与调度](../执行与调度.md)。事件和周期可触发指定版本的 Action；规则配置不改变 Action 自身的权限及幂等要求。

## 4. 副作用、重试与兼容性

持久任务按至少一次处理，外部写入需要接收方幂等键或结果核对。工作线程完成外部写入但未提交结果时，不能直接假定操作失败并再次创建工单。

超时不代表业务操作未发生，取消不保证撤销既有结果。跨系统默认没有原子事务；部分成功需明确补偿或向前修复，外部效果不明时保留待核对记录。补偿能力应按操作实际能力声明，不承诺通用回滚。

定义版本需要固定目标 Schema、输入输出、权限要求和实现版本。修改业务效果、必填参数、单位或重试条件时评估旧调用方与未完成任务；发布过程见 [发布与迁移](../发布与迁移.md)。

## 5. 实现与验收

模块提供草稿维护、指定版本读取、目标/参数校验、引用检查与兼容性分析。运行器沿用 Function 的清单与扩展方式；任务处理由 Execution 负责，避免 Action 模块重复实现队列、调度与运行状态机。

定义契约沿用 [Function 的 CORE-06](Function.md)；执行与副作用行为沿用 [执行与调度的 RUN-01～05](../执行与调度.md)。本模块重点验证：

- 未授权或不满足目标条件的操作不能执行。
- 相同幂等键重试可定位同一个逻辑操作，业务效果按接收方协议核对。
- 外部成功但本地结果提交失败、部分成功、超时及取消都可追踪。
- 引用版本失效、输入单位不符及副作用声明不一致时明确拒绝。
- 已受理任务在发布后仍执行固定版本，补偿行为符合声明。

待定：Action 与 Function 的最终边界、目标与前置条件协议、事务范围、幂等键作用域/保留期、接收方核对接口、补偿及权限模型。工业设备控制和额外审批能力仍属于场景范围，未自动加入基础版本。
