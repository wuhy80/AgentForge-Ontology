# Ontology 中文工作台

React + TypeScript + Vite 前端，对接仓库现有的 Metadata/Data 服务。查询交互主要说明见[可视化查询编辑器](../../docs/数据查询/可视化查询编辑器.md)，公共权限规则见[权限设计](../../docs/权限/权限设计.md)，本地账号及会话实际协议见 [Metadata identity](../../services/metadata/identity/README.md)。完整能力边界见[项目总览](../../docs/项目总览.md#当前实现与验证2026-11-06)，后端启动及真实验证方式见[开发指南](../../docs/开发指南.md#前端工作台)。

## 开发与构建

需要 Node.js 22.12+ 或 24；已实测 24.4.0。在此目录执行：

```powershell
npm.cmd ci
npm.cmd run dev
```

开发地址为 `http://127.0.0.1:5173`。后端须分别运行在 `18080`、`18081`；同源 `/api/metadata`、`/api/data` 分别移除 `/api` 前缀转发至公开路由。启动仓库的 `scripts/start-dev.ps1` 后，以忽略目录 `.cache/dev/admin.json` 初始化的租户、用户名和密码登录；重复启动不会重置已有密码。正常流程不需要签发或粘贴访问凭证，也不向浏览器提供数据库、服务密钥或可切换租户头。

会话由 HttpOnly、SameSite=Strict Cookie 承载，最长8小时；页面刷新时重新读取当前会话。浏览器通过同源请求携带 Cookie，并由 API 层为登录以外的非安全方法请求添加内存中的 CSRF token；服务端同时校验 `Origin`。这也适用于 POST 查询和预览。退出调用后端注销会话，账号停用、密码重置和角色绑定变化会撤销旧会话。首次登录后可从工作台修改密码。

Vite 的开发和产物预览代理仅适用于本机验证。`npm.cmd run preview` 使用4173端口，须相应配置 Metadata 的 `ONTOLOGY_PUBLIC_ORIGIN` 才能登录和提交修改；生产部署仍须配置公开 API 网关、HTTPS 与对应来源。

```powershell
npm.cmd test
npm.cmd run build
npm.cmd run preview
# 已启动真实服务与前端时，用本机 Edge 验证完整页面流程。
npm.cmd run test:e2e
```

锁文件固定实际依赖版本；测试、构建产物、浏览器截图与依赖目录不提交。常规单元及交互测试无需数据库；浏览器测试通过本地忽略目录读取初始开发账号，在独立验收分类中创建自己的数据，账号配置缺失时明确跳过，不录制 trace 或 video。

## 页面与代码入口

| 入口 | 职责 |
| --- | --- |
| [App](src/App.tsx) | 会话、中文工作台导航、目录汇总与概览；目录取真实数据，未提供的指标不编造 |
| [模型管理](src/features/model/ModelsPage.tsx) | 分类分组、五类定义表单、草稿、预览与生效恢复 |
| [数据探索](src/features/query/QueryPage.tsx) | 可视化 QuerySpec 配置、实际执行、查询状态及续页 |
| [实例详情](src/features/query/InstanceDialog.tsx) | 单条创建/读取/修改/确认删除，保持精度、幂等及并发凭证 |
| [用户与权限](src/features/admin/AdminPage.tsx) | 管理员创建/编辑账号、启停与密码重置、直接角色与用户组、权限草稿/生效/试算及最近200条审计 |
| [用户组编辑](src/features/admin/UserGroupDialog.tsx) | 中文用户组、角色与成员搜索、启停、ETag冲突保留及当前会话变更提示；独立于模型目录分组 |
| [API](src/lib/api.ts)、[类型](src/lib/types.ts) | 公共协议的前端镜像、目录分页、错误与身份失效；协议以 contracts 和服务说明为准 |
| [对话框](src/components/Dialog.tsx) | 键盘焦点、Escape、焦点恢复；页面隐藏时不展示 Portal，保留所属页面的编辑状态 |
| [真实浏览器验证](e2e/workspace.spec.ts) | MySQL/OPA 服务上的模型、查询与实例页面流程及窄屏检查 |

页面隐藏选项与前端校验用于体验，所有权限、类型、依赖和结构能力仍由后端裁决。管理员标记只开放当前租户的管理入口，不自动授予模型或数据权限；实际业务访问使用 Metadata 数据库中的当前角色绑定及已生效策略，经 OPA 求值。策略草稿可试算，显式生效后才改变业务授权；单条读取与集合查询分别使用 `instance.read`、`instance.query`。

首次登录检查两个服务的能力接口；退出、过期或401清除本会话页面数据。查询及实例访问收到403时清除对应旧结果；草稿目录拒绝或对象被过滤时清除私有候选，恢复未确认发布前重新验证读取权限，不用旧缓存替代当前授权。

本轮提供本地账号、平面用户组、角色和受控行列策略表单；用户表分别展示直接角色与组继承来源，组变更后清除旧授权模拟结果。尚未接入外部身份目录/SSO、组嵌套、细分委托管理、完整ABAC、逐行掩码及Action/Function运行授权。当前管理审计仅显示最近200条，完整归档、导出和生产运维仍待后续实现。
