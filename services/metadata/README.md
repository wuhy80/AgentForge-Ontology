# Metadata 开发入口

本模块实现组织管理、五类定义草稿与当前态、名称解析、方向发现，以及与 Data 协作的持久发布操作。公共规则继续维护在[总体架构](../../docs/总体架构.md)、[组织与隔离](../../docs/组织与隔离.md)及[发布与迁移](../../docs/发布与迁移.md)。

## 构建与运行

本模块使用独立 `go.mod`，可以在 `GOWORK=off` 下构建。

```powershell
$env:GOWORK='off'
go test ./...
go vet ./...
```

启动入口读取以下环境变量；不要把凭据提交到 Git。

| 变量 | 用途 |
| --- | --- |
| `ONTOLOGY_MYSQL_DSN` | 本服务拥有的 MySQL 数据库连接，连接字符集使用 utf8mb4 |
| `ONTOLOGY_AUTH_SECRET` | 用户身份凭证签名密钥，至少32字节 |
| `ONTOLOGY_SERVICE_TOKEN` | Data/Metadata 内部通信凭据，至少32字节且与身份密钥不同 |
| `ONTOLOGY_OPA_URL` | OPA 服务地址 |
| `ONTOLOGY_OPA_TOKEN` | 可选的 OPA 服务凭据 |
| `ONTOLOGY_DATA_URL` | Data 服务地址 |
| `ONTOLOGY_LISTEN` | 监听地址，默认 `127.0.0.1:18080` |

```powershell
# 显式迁移后退出；普通启动不创建平台表。
go run ./cmd/server -migrate
go run ./cmd/server
```

## HTTP 入口

普通请求需要签名身份凭证，租户来自已验证身份。定义管理使用整份定义的 `definition.read`、`definition.edit`、`definition.publish` 授权；组织管理使用 `organization.read`、`organization.edit`。本增量对带行条件的定义管理授权返回422，不忽略条件继续访问。

| 方法和路径 | 用途 |
| --- | --- |
| `GET /healthz`、`GET /readyz` | 存活与数据库就绪探针 |
| `GET /metadata/capabilities` | 当前开放能力与限制 |
| `POST/GET /metadata/categories` | 创建和分页读取分类 |
| `POST/GET /metadata/groups` | 创建和分页读取分组；读取指定 categoryId |
| `POST /metadata/definitions` | 创建定义及首份草稿，返回服务端生成的定义/字段 ID |
| `GET /metadata/definitions` | 默认分页读取已生效定义；`view=draft` 发现未发布草稿，支持 categoryId、kind |
| `GET /metadata/definitions/{id}` | 读取当前定义；只有草稿的对象返回404 |
| `GET/PUT /metadata/definitions/{id}/draft` | 读取/保存草稿；PUT 必须带强 If-Match |
| `POST /metadata/definitions/{id}/preview` | 预览草稿，缺少草稿时预览当前；不创建发布操作或占用发布状态 |
| `POST /metadata/definitions/{id}/publish` | 按草稿强 If-Match 生效，或恢复同一持久操作 |
| `GET /metadata/publications/{id}` | 核对操作状态及恢复进度 |
| `GET /metadata/resolve?category=分类名&name=定义名&kind=schema` | 中文名称解析；kind 可选 |
| `GET /metadata/schemas/{id}/links` | 发现正反方向，按 Link ID 与 direction 定位 |

`GET current` 和 `GET resolve` 是内部服务凭据唯一可访问的业务路径；必须同时提供 `X-Tenant-ID`。该凭据不能编辑、发布、读取草稿或替代普通用户授权。JSON `etag` 为裸摘要，HTTP `ETag` 和 `If-Match` 使用双引号包围摘要。

组织及定义列表默认100条、最多200条，使用返回的 `nextCursor` 作为下一次 `after`。这与 Data 的查询游标分别处理。

定义目录的 `view` 只接受 `current` 或 `draft`，未提供时保持读取当前态。草稿视图只返回实际有草稿的对象，包括从未生效的定义和生效后重新编辑的草稿；结果 `status` 为 `draft`，内容与 ETag 均来自该草稿。草稿目录同时检查全局及逐对象的 `definition.edit` 授权，当前目录保持 `definition.read` 授权；无权对象不会成为续页标记。目录仅用于当前身份可管理内容的发现，不将拥有当前定义读取权限等同于拥有草稿编辑权限。

## 测试与当前限制

`go test ./...` 覆盖名称归一、定义/类型校验、身份隔离、错误关闭、并发凭证及 Data 操作确认。设置 `METADATA_TEST_DSN` 可运行真实 MySQL 测试，显式应用本模块迁移，并以随机租户验证生命周期、失败恢复、outbox 去重、中文名称多轮往返修改、同操作并发恢复和结构变更拒绝；测试不会删除既有数据。

发布操作身份独立于候选内容摘要：A→B→A 的最后一步创建新操作，只有候选已与当前内容相同时才清理相同草稿并幂等返回已有成功操作。并发重试按持久阶段推进，较早网络请求的迟到失败不能覆盖已提交或成功状态。

当前生命周期为草稿与生效态；停用、删除、移动定义、结构性迁移及运行调用尚未开放。已生效定义只接受保留绑定身份的展示元数据修改，实例存储能力由 Data 再次认证。outbox 当前仅持久记录，没有消费者。发布故障须通过同一接口/凭证显式恢复；跨服务自动恢复及进行中操作的即时撤权尚未验收。
