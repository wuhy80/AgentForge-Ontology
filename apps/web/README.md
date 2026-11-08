# Ontology 中文工作台

React + TypeScript + Vite 前端，对接仓库现有的Metadata、Data和Execution服务。查询交互主要说明见[可视化查询编辑器](../../docs/数据查询/可视化查询编辑器.md)，源码编辑规则见[Function](../../docs/模块/Function.md#独立源码编辑2026-11-07用户确认)，公共权限规则见[权限设计](../../docs/权限/权限设计.md)，本地账号及会话实际协议见[Metadata identity](../../services/metadata/identity/README.md)。完整能力边界见[项目总览](../../docs/项目总览.md#当前实现与验证2026-11-06)，后端启动及真实验证方式见[开发指南](../../docs/开发指南.md#前端工作台)。

## 开发与构建

需要 Node.js 22.12+ 或 24；已实测 24.4.0。在此目录执行：

```powershell
npm.cmd ci
npm.cmd run dev
```

开发地址为`http://127.0.0.1:5173`。Metadata、Data、Execution后端分别运行在`18080`、`18081`、`18082`；同源`/api/metadata`、`/api/data`、`/api/execution`分别移除`/api`前缀转发至公开路由。启动仓库的`scripts/start-dev.ps1`后，以忽略目录`.cache/dev/admin.json`初始化的租户、用户名和密码登录；重复启动不会重置已有密码。正常流程不需要签发或粘贴访问凭证，也不向浏览器提供数据库、服务密钥或可切换租户头。

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
| [对象属性](src/features/model/SchemaPropertiesEditor.tsx) | 紧凑属性列表及每行编辑对话框，集中维护单属性和表列映射；应用到页面草稿、取消回滚，实际字段类型和存在状态在同一弹窗内按需核对 |
| [数据预览](src/features/model/ModelDataPreview.tsx) | 直接读取当前生效对象的小页实例，选择字段和记录，底部按 Link 正反方向预览关联数据；不提供业务写入入口 |
| [Schema 映射](src/features/model/SchemaStorageEditor.tsx) | 两种建模方向、主表稳定身份及逐属性跨源/跨表目录选择；默认平台存储也展示实际生效表列，草稿与生效映射分开标识；反向导入保留已有属性身份 |
| [Link 映射](src/features/model/LinkStorageEditor.tsx) | 区分纯定义、平台关系表、已有外部关联表和外键；可视化表列选择、关系示意、一次性补绑定 |
| [Python源码](src/components/PythonSourceEditor.tsx) | Function及直接源码Action独立使用的CodeMirror编辑器；Python高亮、本地语法与中文参数补全、缩进、括号及行号，运行范围仍仅Python纯计算 |
| [数据源配置](src/features/sources/SourcesPage.tsx) | 表格列出连接及兼容状态，每行测试/编辑/管理；密码只写入，编辑沿用版本、TLS、租户隔离及ETag |
| [数据库管理](src/features/sources/SourceManager.tsx) | 重新读取数据库基础信息、搜索表目录、查看字段及索引；专属来源执行有界单表只读SQL并展示结果，共享来源通过模型查询；实际权限与SQL范围见[数据源](../../docs/数据源.md#列表与数据库管理2026-11-07) |
| [数据探索](src/features/query/QueryPage.tsx) | 默认OntologySQL，顶部指导、左侧模型/右侧字段、双击与键盘插入、CodeMirror懒加载、参数、服务端补全/诊断、结果详情/关联及续页；保留可视化完整子集转换 |
| [实例详情](src/features/query/InstanceDialog.tsx) | 单条创建/读取/修改/确认删除，保持精度、幂等及并发凭证 |
| [用户与权限](src/features/admin/AdminPage.tsx) | 管理员创建/编辑账号、启停与密码重置、直接角色与用户组、权限草稿/生效/试算及最近200条审计 |
| [用户组编辑](src/features/admin/UserGroupDialog.tsx) | 中文用户组、角色与成员搜索、启停、ETag冲突保留及当前会话变更提示；独立于模型目录分组 |
| [API](src/lib/api.ts)、[类型](src/lib/types.ts) | 公共协议的前端镜像、目录分页、错误与身份失效；协议以 contracts 和服务说明为准 |
| [对话框](src/components/Dialog.tsx) | 键盘焦点、Escape、焦点恢复；页面隐藏时不展示 Portal，保留所属页面的编辑状态 |
| [真实浏览器验证](e2e/workspace.spec.ts) | MySQL/OPA 服务上的模型、查询与实例页面流程及窄屏检查 |

工作台右侧各页面共用流式内容区：以扣除侧栏后的可用宽度为基准，左右各留3%，内容占94%，不设固定最大宽度；顶栏、正文及页脚边界保持对齐，窄屏沿用相同比例。规则由 [工作台样式](src/styles.css) 统一控制，表格在自身容器内滚动。

页面隐藏选项与前端校验用于体验，所有权限、类型、依赖和结构能力仍由后端裁决。管理员标记只开放当前租户的管理入口，不自动授予模型或数据权限；实际业务访问使用 Metadata 数据库中的当前角色绑定及已生效策略，经 OPA 求值。策略草稿可试算，显式生效后才改变业务授权；单条读取与集合查询分别使用 `instance.read`、`instance.query`。

查询编写的布局、双击与替换/撤销规则统一见[查询工作台](../../docs/数据查询/可视化查询编辑器.md#页面布局与首次查询)。已有SQL时双击模型，预览并选择[多模型Link路径](../../docs/数据查询/可视化查询编辑器.md#多模型与link路径辅助2026-11-07已实现)，采纳后保留原筛选和参数；双击字段按当前模型角色插入。最多4条INNER Link，关联结果独立只读展示、保留一对多并复用完整计划续页；跨源、LEFT和聚合当前拒绝。

分类和分组的工作台规则统一见[组织与隔离](../../docs/组织与隔离.md#工作台当前分类2026-11-07-已确认)。先建好目录，再由顶栏选定分类进入模型或数据探索；用户、权限及[数据源](../../docs/数据源.md)按租户管理。模型列表点击名称或编辑进入 `#models/{id}`，新建使用 `#models/new?kind=schema`，直接地址仍重新校验分类和定义授权。来源及模式规则见[Schema](../../docs/模块/Schema.md#2-建模方向与预览)。映射实例只读，主表字段可筛选排序，其他来源字段可投影。切换分类清除查询现场并关闭旧分类编辑器，已保存草稿可返回继续；同租户跨分类引用和未确认操作身份继续保留。浏览器只在 sessionStorage 保存分类 ID 偏好，不保存密码、会话凭据或查询结果。

首次登录检查两个服务的能力接口；退出、过期或401清除本会话页面数据。查询及实例访问收到403时清除对应旧结果；草稿目录拒绝或对象被过滤时清除私有候选，恢复未确认发布前重新验证读取权限，不用旧缓存替代当前授权。

对象页的 Tab 规则统一见 [Schema](../../docs/模块/Schema.md#2-建模方向与预览)。关联关系由两端稳定 ID 筛选，关联动作使用 [Action](../../docs/模块/Action.md) 的目标对象 ID；相关列表包含授权可见的当前定义及草稿，不把关联动作当作可执行按钮。

Link 编辑页可配置实际数据库映射，数据探索从每条结果的“关联数据”进入单跳正反查询并分页；平台关系可添加/移除，已有外部关系只读。未绑定关系显示仅定义，不把“已生效”展示成已有事实。操作使用独立 `link.query/create/delete`，现有租户由管理员在权限配置中显式添加并生效；两端对象仍独立授权。协议与当前限制见 [Link](../../docs/模块/Link.md#当前关系数据库实现2026-11-07)，同库能力不代表跨源关系、多跳或图查询已经开放。

Function 编辑页可启用 Python 实现，配置源码、入口和 1–30 秒超时；已生效纯函数可更新代码并重新发布，参数类型与约束保持锁定。Python 字典按声明的中文参数名传入/返回，改名须同步源码；HTTP 继续使用稳定属性 ID，int64/decimal 使用字符串。当前仅支持标准库及无副作用计算，网络、第三方依赖和平台数据访问未开放。运行器及实际协议见 [Execution](../../services/execution/README.md)，源码与清单主要规则见 [Function](../../docs/模块/Function.md)。

已生效的 Action/Function 编辑页提供“运行当前定义”面板，打开时从服务器读取当前定义并检查 Execution 能力；草稿不参与运行。运行采用同步调用、持久记录及幂等键；响应丢失时保持相同参数重试以核对同一次调用，修改参数或已确认完成后再次运行使用新键。运行中记录可显式核对；关闭、离开、身份变化及拒绝时清除旧运行数据，迟到响应不能重新显示。执行服务不可用不阻止现有模型、查询或权限页面。

隔离浏览器用例 `e2e/python-runtime.spec.ts` 在真实 Edge 中拦截全部 API，覆盖 Python 编辑/发布、当前态运行、中文参数与结果、403 清理以及 1440px/390px 无页面溢出；不连接既有租户数据库，不代表真实容器执行验收。若本机 Windows 保留了 5173 端口，可临时启动 `npm.cmd run dev -- --port 6173`，设置 `$env:ONTOLOGY_WEB_URL='http://127.0.0.1:6173'` 后运行 `npm.cmd run test:e2e -- e2e/python-runtime.spec.ts`。这只改变测试地址，真实服务的 Origin 配置保持既有值。

权限配置和模拟支持 `function.invoke`、`action.invoke`，按对应类型的完整资源授权，不接受字段或行条件；Action 调用还须拥有绑定 Function 的运行权限。已有租户不会自动追加权限，由管理员显式配置并生效。受控委托、Action 独享的函数调用路径及数据/外部写入继续待实现。

本轮提供本地账号、平面用户组、角色和受控行列策略表单；用户表分别展示直接角色与组继承来源，组变更后清除旧授权模拟结果。尚未接入外部身份目录/SSO、组嵌套、细分委托管理、完整ABAC及逐行掩码。当前管理审计仅显示最近200条，完整归档、导出和生产运维仍待后续实现。
