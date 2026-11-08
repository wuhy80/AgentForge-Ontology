# Ontology 中文工作台

React + TypeScript + Vite 前端，对接仓库现有的Metadata、Data和Execution服务。查询交互主要说明见[可视化查询编辑器](../../docs/数据查询/可视化查询编辑器.md)，源码编辑规则见[Function](../../docs/模块/Function.md#独立源码编辑2026-11-07用户确认)，公共权限规则见[权限设计](../../docs/权限/权限设计.md)，本地账号及会话实际协议见[Metadata identity](../../services/metadata/identity/README.md)。完整能力边界见[项目总览](../../docs/项目总览.md#当前实现与验证2026-11-06)，后端启动及真实验证方式见[开发指南](../../docs/开发指南.md#前端工作台)。

## 开发与构建

正式工作台支持「深空蓝」和「精密白」，可从顶栏或登录页右上角切换，默认深空蓝；同源浏览器记住选择并同步标签页，存储不可用时仍可临时切换。源码与SQL编辑器、关系图、表单和弹窗同步适配，切换保留未保存编辑及撤销记录。完整规则与覆盖边界见[工作台视觉升级](../../docs/界面视觉升级.md)。

需要 Node.js 22.12+ 或 24；已实测 24.4.0。在此目录执行：

```powershell
npm.cmd ci
npm.cmd run dev
```

开发地址为`http://127.0.0.1:5173`。Metadata、Data、Execution后端分别运行在`18080`、`18081`、`18082`；同源`/api/metadata`、`/api/data`、`/api/execution`分别移除`/api`前缀转发至公开路由。启动仓库的`scripts/start-dev.ps1`后，以忽略目录`.cache/dev/admin.json`初始化的租户、用户名和密码登录；重复启动不会重置已有密码。正常流程不需要签发或粘贴访问凭证，也不向浏览器提供数据库、服务密钥或可切换租户头。

按用户确认，开发代理新增独立应用 OntoApp 的可选共同入口：`/ontoapp/` 转发到其前端 `127.0.0.1:5180`，`/api/ontoapp` 转发到其独立配置后端 `127.0.0.1:18083`。另行启动 OntoApp，并在其本地配置设置 `ONTOAPP_BASE_PATH=/ontoapp/`、`ONTOAPP_PUBLIC_ORIGIN=http://127.0.0.1:5173` 后，访问 `http://127.0.0.1:5173/ontoapp/`。代理保留真实 Host、Origin 和 Cookie，沿用工作台已有的公开认证接口；Ontology 不读写 OntoApp 的独立配置数据库。该共同入口不修改 Ontology 后端来源配置，后续接入与验证由 OntoApp 仓库的接入说明维护。

用户随后指定 OntoApp 使用 `http://127.0.0.1:4173/` 网关数据源，已增加专用 `/api/ontoapp-ontology/*` 路由到此预览网关的 `/api/*`，不改变工作台自身的 Metadata/Data 路由。该规则须在 `/api/ontoapp` 之前匹配，保留实际 5173 Host/Origin，不以更改来源绕过授权；4173 须单独保持运行。该配置的源名称、地址、模拟与真实 HTTP 验证结果维护在 OntoApp 的接入说明和验证记录中。

会话由 HttpOnly、SameSite=Strict Cookie 承载，最长8小时；页面刷新时重新读取当前会话。浏览器通过同源请求携带 Cookie，并由 API 层为登录以外的非安全方法请求添加内存中的 CSRF token；服务端同时校验 `Origin`。这也适用于 POST 查询和预览。退出调用后端注销会话，账号停用、密码重置和角色绑定变化会撤销旧会话。首次登录后可从工作台修改密码。

Vite 的开发和产物预览代理仅适用于本机验证。`npm.cmd run preview` 使用4173端口；仓库开发启动脚本已为Metadata显式配置5173主来源与4173附加来源，两个入口可同时登录、表预览和提交修改。手工启动Metadata时设置`ONTOLOGY_ADDITIONAL_PUBLIC_ORIGINS=http://127.0.0.1:4173`，配置及边界见[身份模块](../../services/metadata/identity/README.md)。生产部署仍须配置公开API网关、HTTPS与对应来源。

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
| [本体建模](src/features/model/ModelsPage.tsx) | 侧栏分别进入对象模型、枚举、关系、动作、函数，与管理员的数据源配置同组；按类型展示分组列表，独立编辑地址支持刷新、返回对应类型与未保存离开保护；草稿、预览与生效恢复 |
| [对象编辑](src/features/model/ModelEditor.tsx) | 基础信息、属性配置、索引信息、Link 信息、动作信息及数据预览；关联定义可跳转，跨页签保留修改 |
| [对象属性](src/features/model/SchemaPropertiesEditor.tsx) | 紧凑属性列表及每行编辑对话框，集中维护单属性和表列映射；应用到页面草稿、取消回滚，实际字段类型和存在状态在同一弹窗内按需核对 |
| [数据预览](src/features/model/ModelDataPreview.tsx) | 直接读取当前生效对象的小页实例，选择字段和记录，底部按 Link 正反方向预览关联数据；不提供业务写入入口 |
| [Schema 映射](src/features/model/SchemaStorageEditor.tsx) | 两种建模方向、主表稳定身份及逐属性跨源/跨表目录选择；默认平台存储也展示实际生效表列，草稿与生效映射分开标识；反向导入保留已有属性身份 |
| [Link 映射](src/features/model/LinkStorageEditor.tsx) | 区分纯定义、平台关系表、已有外部关联表和外键；可视化表列选择、关系示意、一次性补绑定 |
| [关系可视化](src/features/model/RelationOverview.tsx)、[关系详情](src/features/model/LinkVisualEditor.tsx) | `#models?view=graph`查看当前分类关系，选线侧栏编辑；详情及对象Link页共用画布，支持拖线创建、个人布局、草稿/预览/发布与当前态双向数据预览；[主要规则](../../docs/模块/Link.md#关系可视化管理2026-11-08首批已实现) |
| [本体全景](src/features/ontology/OntologyPage.tsx) | `#ontology`只读浏览当前分类的五类生效定义，提供对象关系图、定义依赖图、详情联动、搜索筛选、稳定个人布局及既有定义/数据跳转；实现边界与验证见[本体全景](../../docs/本体全景.md) |
| [源码编辑器](src/components/PythonSourceEditor.tsx) | Function及直接源码Action独立使用的CodeMirror编辑器；Python、JavaScript、Go高亮、中文参数补全、语法辅助、格式化及参数改名引用提醒；共同边界见[Function](../../docs/模块/Function.md#javascript与go扩展2026-11-08已确认) |
| [数据源配置](src/features/sources/SourcesPage.tsx) | 表格列出连接及兼容状态，每行测试/编辑/管理；密码只写入，编辑沿用版本、TLS、租户隔离及ETag |
| [数据库管理](src/features/sources/SourceManager.tsx) | 基础信息与真实指标、每页20条目录及字段/主键列计数、默认值/注释/索引；进入专属预览Tab自动刷新，缩窄目录与随工作台主题切换的SQL编辑器/只读工具，默认字段页不查行，共享来源禁用物理业务行读取；[主要规则](../../docs/数据源.md#数据库管理工作台2026-11-08已确认) |
| [数据探索](src/features/query/QueryPage.tsx) | 默认OntologySQL，顶部指导、左侧模型/右侧字段、双击与键盘插入、CodeMirror懒加载、参数、服务端补全/诊断、结果详情/关联及续页；支持单模型及最多4条INNER Link的可视化配置和SQL双向转换，具体边界见查询主要说明 |
| [可视化关联](src/features/query/VisualRelations.tsx) | 授权模型搜索/分页、路径预览及确认、正反方向与角色改名；[绑定辅助](src/features/query/visualRows.ts)保持Schema/Link/字段稳定ID，执行直接走同一QuerySpec |
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

2026-11-08已完善数据库管理的基础信息网格、真实指标、每页20条表目录和字段/预览/SQL Tab。字段/主键列计数、默认值、注释与索引来自真实目录；本轮按用户新要求将专属来源预览改为每次进入Tab自动刷新并删除预览按钮，替代上批显式加载交互，管理初次打开仍默认字段详情不查行。切表、切Tab、刷新或离开取消旧请求并清理业务结果。指标不可用显示原因，MySQL实例QPS和存储估算按实际口径展示，PostgreSQL不以TPS替代QPS。协议见[数据源](../../docs/数据源.md#数据库管理工作台2026-11-08已确认)。

上批数据库管理以全量40个文件/360项单元与交互测试通过；补充指标缺失及并行403竞态后，SourceManager定向16项通过，TypeScript/Vite构建通过。[管理布局](e2e/source-manager-layout.spec.ts)3条及[数据源映射](e2e/sources-mapping.spec.ts)3条隔离Edge用例通过，并人工核对1440px/390px截图；这些结果保留为历史记录，不代替本轮自动预览和SQL工具验证。浏览器使用模拟API；当批真实浏览器登录因本地缓存账号返回401未取得会话，未重置账号或密码。真实数据库和HTTP边界结果分别见项目总览。

本轮已完成只读控制台增量：目录宽度缩至上批约3/5，暖杏主题CodeMirror编辑器提供行号/高亮，配套执行、格式化、校验语法、三类查询模板填入及清空，limit默认100且限制1～100。工具只校验封闭语法或格式化，查询模板填入不执行；实际执行仅SELECT，共享来源继续拒绝物理业务行。协议及统计口径见[SQL只读控制台](../../docs/数据源.md#sql只读控制台2026-11-08增量已确认)，CREATE/INSERT/UPDATE等后续独立实现。

本轮全量43个文件/388项测试通过；后续追加503重试/busy状态回归后，SQL控制台16项、编辑器5项及模板生成3项共24项定向通过，未推算为全量重跑结果。最终TypeScript/Vite生产构建通过；[管理布局](e2e/source-manager-layout.spec.ts)4条与[数据源映射](e2e/sources-mapping.spec.ts)3条共7条隔离Edge用例同次通过，1440px/390px截图人工核对暖杏主题、菜单与目录比例。浏览器使用fixture API；真实两库及HTTP边界结果见项目总览。本轮未重复缓存账号真实登录尝试，前批401限制继续保留，未修改账号密码。

Link 编辑页可配置实际数据库映射，数据探索从每条结果的“关联数据”进入单跳正反查询并分页；平台关系可添加/移除，已有外部关系只读。未绑定关系显示仅定义，不把“已生效”展示成已有事实。操作使用独立 `link.query/create/delete`，现有租户由管理员在权限配置中显式添加并生效；两端对象仍独立授权。实例导航协议与限制见 [Link](../../docs/模块/Link.md#当前关系数据库实现2026-11-07)，有界多Link关联行及可视化配置另见[查询主要说明](../../docs/数据查询/可视化查询编辑器.md#可视化关联与模式互通2026-11-08已实现)；跨源关系及无界图查询继续后续。

Function和直接源码Action各自选择Python、JavaScript或Go实现，配置源码、入口和1～30秒超时；已生效纯计算定义可更新代码并重新发布，参数类型与约束保持锁定。JavaScript使用同步普通函数，Go使用单文件`package main`及约定的`Main`入口，具体示例见[Function](../../docs/模块/Function.md#javascript与go扩展2026-11-08已确认)。切换语言时，自编源码需要确认是否替换为对应模板。三语言均按声明的中文参数名传入/返回对象，改名须同步源码；HTTP继续使用稳定属性ID，int64/decimal使用字符串。当前仅支持无副作用计算，网络、第三方依赖和平台数据访问未开放。运行器及实际协议见[Execution](../../services/execution/README.md)，本轮开发及分项实测状态见[项目总览](../../docs/项目总览.md#当前实现与验证2026-11-06)。

Python使用本地Ruff WASM工具，JavaScript使用本地Prettier Worker解析与格式化；Go的显式语法检查与格式化调用容器内Go标准库解析器和格式化器，须先保存定义以取得稳定ID并具有编辑资格。工具不运行用户函数，也不验证类型、入口签名或业务输出；这些内容通过明确的草稿试运行核对。三语言的高亮、补全、诊断坐标和参数改名提醒范围统一维护在Function，不把基础补全当作完整语言服务。

已生效的Action/Function编辑页提供“运行当前定义”，打开时读取服务器当前定义并检查Execution能力；独立的“试运行草稿”使用服务端已保存草稿与对应ETag，未保存修改须先保存。草稿试运行不改变普通调用的当前代码，已保存测试样例随定义草稿/发布共享，不写入浏览器localStorage。源码调试、样例类型与预算统一见[Function](../../docs/模块/Function.md#python调试与草稿试运行2026-11-08已确认)，记录来源、幂等及过期结果规则见[执行与调度](../../docs/执行与调度.md#草稿试运行2026-11-08)。

两种运行都采用同步调用、持久记录及幂等键；响应丢失时保持相同参数与草稿ETag重试以核对同一次调用，修改参数、草稿或已确认完成后再次运行使用新键。运行中记录可显式核对；关闭、离开、身份变化及拒绝时清除旧运行数据，迟到响应不能重新显示。执行服务不可用不阻止现有模型、查询或权限页面。诊断坐标只定位对应源码快照，编辑代码后不继续显示旧结果的错误位置。

2026-11-08此前Python调试批次以`npm.cmd test -- --maxWorkers=4`完整通过38个文件/319项单元与交互测试，TypeScript/Vite构建通过。隔离浏览器用例`e2e/python-runtime.spec.ts`共4条在真实Edge中通过，拦截全部API，覆盖Python编辑/发布、当前态运行、中文参数与结果、403清理，以及新增真实Ruff WASM语法检查、保留注释的格式化、参数改名提醒、样例保存、草稿与当前态分离、错误定位及1440px/390px布局；不连接既有租户数据库，不代表本轮三语言容器执行已验证，后端真实集成记录见[项目总览](../../docs/项目总览.md#当前实现与验证2026-11-06)。WASM随前端独立构建并惰性加载，资源体积与工具范围见[Function](../../docs/模块/Function.md#python调试与草稿试运行2026-11-08已确认)。若本机Windows保留了5173端口，可临时启动`npm.cmd run dev -- --port 6173`，设置`$env:ONTOLOGY_WEB_URL='http://127.0.0.1:6173'`后运行`npm.cmd run test:e2e -- e2e/python-runtime.spec.ts`。这只改变测试地址，真实服务的Origin配置保持既有值。

2026-11-08本轮三语言扩展完整通过40个文件/350项单元与交互测试，TypeScript/Vite生产构建通过；最终构建后的[多语言](e2e/multilang-runtime.spec.ts)与[Python](e2e/python-runtime.spec.ts)共10条隔离Edge用例全部通过，覆盖Function/Action、语言切换、缺镜像、拒绝清理及窄屏。浏览器拦截全部API，JavaScript使用真实本地Worker，Go源码工具响应由用例模拟；真实容器和HTTP链结果单独见项目总览。

权限配置和模拟支持 `function.invoke`、`action.invoke`，按对应类型的完整资源授权，不接受字段或行条件；绑定Function的Action调用还须拥有该Function的运行权限，直接源码Action仅检查自身调用资格。已有租户不会自动追加权限，由管理员显式配置并生效。受控委托、Action 独享的函数调用路径及数据/外部写入继续待实现。

本轮提供本地账号、平面用户组、角色和受控行列策略表单；用户表分别展示直接角色与组继承来源，组变更后清除旧授权模拟结果。尚未接入外部身份目录/SSO、组嵌套、细分委托管理、完整ABAC及逐行掩码。当前管理审计仅显示最近200条，完整归档、导出和生产运维仍待后续实现。
