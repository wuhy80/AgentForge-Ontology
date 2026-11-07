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
# 模拟接口的真实浏览器交互测试，不读取开发账号或写本地数据库。
npm.cmd run test:e2e -- category-context.spec.ts
# 已启动真实服务与前端时，用本机 Edge 验证完整页面流程。
npm.cmd run test:e2e -- workspace.spec.ts
```

锁文件固定实际依赖版本；测试、构建产物、浏览器截图与依赖目录不提交。常规单元及交互测试无需数据库；浏览器测试通过本地忽略目录读取初始开发账号，在独立验收分类中创建自己的数据，账号配置缺失时明确跳过，不录制 trace 或 video。

## 页面与代码入口

| 入口 | 职责 |
| --- | --- |
| [App](src/App.tsx) | 会话、导航与顶栏当前分类；概览和业务页面共享分类，偏好按租户及账号在当前标签页记忆并经授权目录校验 |
| [分类与分组](src/features/organizations/OrganizationsPage.tsx) | 独立维护分类及当前分类下的分组，列出目录、创建分类和分组；组织移动、重命名与删除尚未开放 |
| [模型管理](src/features/model/ModelsPage.tsx) | 分组紧凑列表及独立模型编辑地址，支持刷新、返回、未保存离开保护；五类定义、草稿、预览与生效恢复 |
| [对象编辑](src/features/model/ModelEditor.tsx) | 基础信息、属性配置、索引信息、Link 信息、动作信息及数据预览；关联定义可跳转，跨页签保留修改 |
| [数据预览](src/features/model/ModelDataPreview.tsx) | 直接读取当前生效对象的小页实例，选择字段和记录，底部按 Link 正反方向预览关联数据；不提供业务写入入口 |
| [Schema 映射](src/features/model/SchemaStorageEditor.tsx) | 两种建模方向、主表稳定身份及逐属性跨源/跨表目录选择；默认平台存储也展示实际生效表列，草稿与生效映射分开标识；反向导入保留已有属性身份 |
| [Link 映射](src/features/model/LinkStorageEditor.tsx) | 区分纯定义、平台关系表、已有外部关联表和外键；可视化表列选择、关系示意、一次性补绑定 |
| [数据源配置](src/features/sources/SourcesPage.tsx) | 管理员维护多个 MySQL/PostgreSQL 连接、TLS和租户隔离，测试连接及查看表列；密码只写入 |
| [数据探索](src/features/query/QueryPage.tsx) | 可视化 QuerySpec 配置、实际执行、查询状态及续页 |
| [实例详情](src/features/query/InstanceDialog.tsx) | 单条创建/读取/修改/确认删除，保持精度、幂等及并发凭证 |
| [用户与权限](src/features/admin/AdminPage.tsx) | 管理员创建/编辑账号、启停与密码重置、直接角色与用户组、权限草稿/生效/试算及最近200条审计 |
| [用户组编辑](src/features/admin/UserGroupDialog.tsx) | 中文用户组、角色与成员搜索、启停、ETag冲突保留及当前会话变更提示；独立于模型目录分组 |
| [API](src/lib/api.ts)、[类型](src/lib/types.ts) | 公共协议的前端镜像、目录分页、错误与身份失效；协议以 contracts 和服务说明为准 |
| [对话框](src/components/Dialog.tsx) | 键盘焦点、Escape、焦点恢复；页面隐藏时不展示 Portal，保留所属页面的编辑状态 |
| [真实浏览器验证](e2e/workspace.spec.ts) | MySQL/OPA 服务上的模型、查询与实例页面流程及窄屏检查 |

页面隐藏选项与前端校验用于体验，所有权限、类型、依赖和结构能力仍由后端裁决。管理员标记只开放当前租户的管理入口，不自动授予模型或数据权限；实际业务访问使用 Metadata 数据库中的当前角色绑定及已生效策略，经 OPA 求值。策略草稿可试算，显式生效后才改变业务授权；单条读取与集合查询分别使用 `instance.read`、`instance.query`。

分类和分组的工作台规则统一见[组织与隔离](../../docs/组织与隔离.md#工作台当前分类2026-11-07-已确认)。先建好目录，再由顶栏选定分类进入模型或数据探索；用户、权限及[数据源](../../docs/数据源.md)按租户管理。模型列表点击名称或编辑进入 `#models/{id}`，新建使用 `#models/new?kind=schema`，直接地址仍重新校验分类和定义授权。来源及模式规则见[Schema](../../docs/模块/Schema.md#2-建模方向与预览)。映射实例只读，主表字段可筛选排序，其他来源字段可投影。切换分类清除查询现场并关闭旧分类编辑器，已保存草稿可返回继续；同租户跨分类引用和未确认操作身份继续保留。浏览器只在 sessionStorage 保存分类 ID 偏好，不保存密码、会话凭据或查询结果。

首次登录检查两个服务的能力接口；退出、过期或401清除本会话页面数据。查询及实例访问收到403时清除对应旧结果；草稿目录拒绝或对象被过滤时清除私有候选，恢复未确认发布前重新验证读取权限，不用旧缓存替代当前授权。

对象页的 Tab 规则统一见 [Schema](../../docs/模块/Schema.md#2-建模方向与预览)。关联关系由两端稳定 ID 筛选，关联动作使用 [Action](../../docs/模块/Action.md) 的目标对象 ID；相关列表包含授权可见的当前定义及草稿，不把关联动作当作可执行按钮。

Link 编辑页可配置实际数据库映射，数据探索从每条结果的“关联数据”进入单跳正反查询并分页；平台关系可添加/移除，已有外部关系只读。未绑定关系显示仅定义，不把“已生效”展示成已有事实。操作使用独立 `link.query/create/delete`，现有租户由管理员在权限配置中显式添加并生效；两端对象仍独立授权。协议与当前限制见 [Link](../../docs/模块/Link.md#当前关系数据库实现2026-11-07)，同库能力不代表跨源关系、多跳或图查询已经开放。

本轮提供本地账号、平面用户组、角色和受控行列策略表单；用户表分别展示直接角色与组继承来源，组变更后清除旧授权模拟结果。尚未接入外部身份目录/SSO、组嵌套、细分委托管理、完整ABAC、逐行掩码及Action/Function运行授权。当前管理审计仅显示最近200条，完整归档、导出和生产运维仍待后续实现。
