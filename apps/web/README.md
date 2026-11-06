# Ontology 中文工作台

React + TypeScript + Vite 前端，对接仓库现有的 Metadata/Data 服务。设计及查询交互的主要说明见[可视化查询编辑器](../../docs/数据查询/可视化查询编辑器.md)，完整能力边界见[项目总览](../../docs/项目总览.md#当前实现与验证2026-11-06)，本地身份、后端启动及真实验证方式见[开发指南](../../docs/开发指南.md#前端工作台)。

## 开发与构建

需要 Node.js 22.12+ 或 24；已实测 24.4.0。在此目录执行：

```powershell
npm.cmd ci
npm.cmd run dev
```

开发地址为 `http://127.0.0.1:5173`。后端须分别运行在 `18080`、`18081`；同源 `/api/metadata`、`/api/data` 分别移除 `/api` 前缀转发至公开路由。浏览器内不配置数据库、服务密钥或可切换租户头，身份来自已验证签名凭证。Vite 的开发和产物预览代理仅适用于本机验证，生产部署须另配静态资源服务和公开 API 网关。

```powershell
npm.cmd test
npm.cmd run build
npm.cmd run preview
# 已启动真实服务与前端时，用本机 Edge 验证完整页面流程。
npm.cmd run test:e2e
```

锁文件固定实际依赖版本；测试、构建产物、浏览器截图与依赖目录不提交。常规单元及交互测试无需数据库；浏览器测试通过本地忽略目录读取开发凭据，在独立验收分类中创建自己的数据，凭据缺失时明确跳过，不录制 trace 或 video。

## 页面与代码入口

| 入口 | 职责 |
| --- | --- |
| [App](src/App.tsx) | 会话、中文工作台导航、目录汇总与概览；目录取真实数据，未提供的指标不编造 |
| [模型管理](src/features/model/ModelsPage.tsx) | 分类分组、五类定义表单、草稿、预览与生效恢复 |
| [数据探索](src/features/query/QueryPage.tsx) | 可视化 QuerySpec 配置、实际执行、查询状态及续页 |
| [实例详情](src/features/query/InstanceDialog.tsx) | 单条创建/读取/修改/确认删除，保持精度、幂等及并发凭证 |
| [API](src/lib/api.ts)、[类型](src/lib/types.ts) | 公共协议的前端镜像、目录分页、错误与身份失效；协议以 contracts 和服务说明为准 |
| [对话框](src/components/Dialog.tsx) | 键盘焦点、Escape、焦点恢复；页面隐藏时不展示 Portal，保留所属页面的编辑状态 |
| [真实浏览器验证](e2e/workspace.spec.ts) | MySQL/OPA 服务上的模型、查询与实例页面流程及窄屏检查 |

页面隐藏选项与前端校验用于体验，所有权限、类型、依赖和结构能力仍由后端裁决。首次连接检查两个服务的能力接口；凭证只存内存，退出、过期或 401 清除本会话页面数据。查询及实例访问收到 403 时清除对应旧结果，不用缓存结果替代授权失败。
