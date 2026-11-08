# Ontology MCP 网关

本服务将 Metadata、Data 和 Execution 的受保护 HTTP 能力适配为 MCP Streamable HTTP。它不读取其他服务私有表、不持有管理员业务会话、不执行用户源码，也不自行实现查询授权。

## 当前实现

- `/mcp` 使用官方 `github.com/modelcontextprotocol/go-sdk` v1.8.0，模块要求 Go 1.25+；优先协商 MCP `2026-07-28`，保留 SDK 支持的旧版握手兼容。
- 每次请求只接受 `Authorization: Bearer mcp_<64位小写十六进制>`，拒绝 Cookie 和不允许的跨站请求；调用者自报的用户/租户头不能选择身份。令牌由 Metadata 身份服务签发、撤销并绑定用户、租户及范围，工具结果返回前再次核验。
- `ontology:discover` 提供模型、分类、调用签名发现；`ontology:query` 提供对象及 OntologySQL 查询；`ontology:invoke` 提供当前授权的纯计算 Action/Function 调用。查询和调用令牌仍需包含 `ontology:discover`。
- 结果同时包含结构化 JSON 和文本块，预算为请求/结果各 1 MiB；`int64`、`decimal` 遵循平台字符串协议；不返回源码、凭据、物理映射、原始异常或未授权结果。
- `/.well-known/oauth-protected-resource[/mcp]` 提供资源元数据；OAuth 授权服务器公开端点由 Metadata 提供，网关只做受限代理，不跟随重定向。

## 启动

普通启动不会迁移任何数据库表。配置依赖后运行：

```powershell
$env:GOWORK='off'
$env:GOTOOLCHAIN='auto'
go run ./cmd/server
```

必需变量：

| 变量 | 默认/用途 |
| --- | --- |
| `ONTOLOGY_METADATA_URL` | Metadata 地址，例如 `http://127.0.0.1:18080` |
| `ONTOLOGY_DATA_URL` | Data 地址，例如 `http://127.0.0.1:18081` |
| `ONTOLOGY_EXECUTION_URL` | Execution 地址，默认 `http://127.0.0.1:18082` |
| `ONTOLOGY_SERVICE_TOKEN` | 内部服务凭据，至少32字节；只用于网关到 Metadata 的身份核验 |
| `ONTOLOGY_MCP_LISTEN` | 监听地址，默认 `127.0.0.1:18084` |
| `ONTOLOGY_MCP_RESOURCE` | MCP resource URI，默认 `http://127.0.0.1:18084/mcp` |
| `ONTOLOGY_AUTH_ISSUER` | OAuth issuer，默认 `http://127.0.0.1:18084` |
| `ONTOLOGY_MCP_ALLOWED_ORIGINS` | 可选逗号分隔 Origin；资源自身 Origin 总是允许 |

### DSH 连接示例

DSH 已有 `dsh-mcp-client`，使用 `streamable-http`，连接 URL 为 `http://127.0.0.1:18084/mcp`，Header 使用安全凭据引用：

```yaml
- id: ontology-mcp
  name: '@deepseek-ai/dsh-mcp-client'
  config:
    serverName: ontology
    transport: streamable-http
    url: http://127.0.0.1:18084/mcp
    expandTemplates: true
    headers:
      Authorization: 'Bearer ${credential:ONTOLOGY_MCP_TOKEN}'
```

OAuth 交互客户端应使用受保护资源元数据发现 `ONTOLOGY_AUTH_ISSUER`，通过 PKCE 获取令牌。当前 DSH 官方 MCP manager 文档仍声明没有内置 OAuth flow，因此可先使用 Metadata 工作台签发的可撤销 MCP 连接凭据；不要把平台 Cookie、服务凭据或密码写入连接配置。

## 验证范围

```powershell
$env:GOTOOLCHAIN='auto'; $env:GOWORK='off'
go test ./...
go vet ./...
```

2026-11-08 已通过官方 Go SDK 最新/最早两个配置修订的真实服务调用，覆盖模型发现、对象查询、OntologySQL、精确数值、Python Function、幂等与撤销；真实 MySQL 身份生命周期和 Edge 浏览器 OAuth 流程也已验证，细项见[MCP 接入](../../docs/MCP接入.md)。dsh 实例、多副本、所有语言与权限组合、性能及生产恢复仍未验收。

真实服务冒烟测试需准备一个有当前模型/数据的专用测试账号，设置以下变量再执行；它只读取业务模型/数据，创建临时个人连接并在结束时撤销，无参数的当前 Function 如存在则执行并核验幂等：

```powershell
$env:MCP_TEST_URL='http://127.0.0.1:18084/mcp'
$env:MCP_TEST_METADATA_URL='http://127.0.0.1:18080'
$env:MCP_TEST_EXECUTION_URL='http://127.0.0.1:18082'
$env:MCP_TEST_ORIGIN='http://127.0.0.1:5173'
$env:MCP_TEST_ACCOUNT_FILE='<专用测试账号 JSON 的绝对路径>'
go test -run TestLiveMCPQueryAndRevocation -count=1 -v
```

账号文件仅包含 `Tenant`、`Username`、`Password`，应存放于忽略目录。身份数据库生命周期通过 `scripts/check.ps1 -Integration` 显式启用；浏览器流程在 `apps/web` 使用同一专用账号执行 `npm.cmd run test:e2e -- mcp-connections.spec.ts`。
