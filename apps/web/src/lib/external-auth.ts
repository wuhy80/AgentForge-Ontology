export const connectionScopes = [
  { value: 'ontology:discover', name: '发现本体', description: '查看当前可访问的模型、字段和可调用签名。' },
  { value: 'ontology:query', name: '查询数据', description: '按当前业务权限读取对象、关系及运行本体查询。' },
  { value: 'ontology:invoke', name: '运行计算', description: '调用已生效的纯计算操作和函数，读取自己的执行结果。' },
] as const

export type ConnectionScope = (typeof connectionScopes)[number]['value']
export interface ExternalConnection {
  id: string
  name: string
  scopes: ConnectionScope[]
  createdAt: number
  expiresAt: number
  revokedAt?: number
  kind: 'personal' | 'oauth'
  clientId?: string
}
export interface ConnectionDirectory { data: ExternalConnection[]; resource: string; issuer: string }
export interface CreateConnection { name: string; scopes: ConnectionScope[]; expiresInSeconds: number }
export interface CreatedConnection { connection: ExternalConnection; accessToken: string }
export interface OAuthAuthorization {
  client_id: string
  redirect_uri: string
  response_type: string
  code_challenge_method: string
  code_challenge: string
  scope: string
  state?: string
  resource: string
}
export interface OAuthPreview {
  client: { clientId: string; name: string; redirectUris: string[] }
  scopes: readonly ConnectionScope[]
  resource: string
}

const authorizationFields = new Set(['client_id', 'redirect_uri', 'response_type', 'code_challenge_method', 'code_challenge', 'scope', 'state', 'resource'])
export function parseOAuthAuthorization(route: string): OAuthAuthorization {
  if (!route.startsWith('oauth/authorize?') || route.length > 8192) throw new Error('授权链接不完整或已失效，请从外部应用重新连接。')
  const params = new URLSearchParams(route.slice('oauth/authorize?'.length))
  for (const key of params.keys()) if (!authorizationFields.has(key) || params.getAll(key).length !== 1) throw new Error('授权链接参数无效，请从外部应用重新连接。')
  const input = Object.fromEntries(params) as unknown as OAuthAuthorization
  if (['client_id', 'redirect_uri', 'response_type', 'code_challenge_method', 'code_challenge', 'scope', 'resource'].some((key) => !params.get(key))) throw new Error('授权链接不完整，请从外部应用重新连接。')
  if (input.response_type !== 'code' || input.code_challenge_method !== 'S256' || !/^[A-Za-z0-9_-]{43}$/.test(input.code_challenge)) throw new Error('此应用的授权方式暂不受支持，请重新发起连接。')
  const scopes = input.scope.split(' ')
  if (scopes.some((scope) => !connectionScopes.some((item) => item.value === scope)) || new Set(scopes).size !== scopes.length) throw new Error('应用请求了不支持的访问范围。')
  oauthURL(input.redirect_uri)
  oauthURL(input.resource)
  return input
}

function oauthURL(raw: string): URL {
  let value: URL
  try { value = new URL(raw) } catch { throw new Error('授权地址无效，请从外部应用重新连接。') }
  const loopback = value.hostname === 'localhost' || value.hostname === '127.0.0.1' || value.hostname === '[::1]'
  if ((value.protocol !== 'https:' && !(value.protocol === 'http:' && loopback)) || value.username || value.password || value.hash) throw new Error('授权地址无效，请从外部应用重新连接。')
  return value
}

export function validateOAuthPreview(input: OAuthAuthorization, preview: OAuthPreview): OAuthPreview {
  const requested = input.scope.split(' ')
  if (preview.client?.clientId !== input.client_id || !preview.client.redirectUris?.includes(input.redirect_uri)
    || preview.resource !== input.resource || !Array.isArray(preview.scopes) || preview.scopes.length !== requested.length
    || preview.scopes.some((scope) => !requested.includes(scope)) || new Set(preview.scopes).size !== requested.length) throw new Error('授权摘要与请求不一致，请从外部应用重新连接。')
  return preview
}

// The server validates registered redirects. This second check makes a malformed
// response fail closed instead of navigating to an unrelated destination.
export function oauthResultURL(input: OAuthAuthorization, redirectUri: string): string {
  const expected = oauthURL(input.redirect_uri), actual = oauthURL(redirectUri)
  if (actual.origin !== expected.origin || actual.pathname !== expected.pathname) throw new Error('授权返回地址不匹配，请重新发起连接。')
  for (const key of expected.searchParams.keys()) if (JSON.stringify(actual.searchParams.getAll(key)) !== JSON.stringify(expected.searchParams.getAll(key))) throw new Error('授权返回地址不匹配，请重新发起连接。')
  if ((input.state !== undefined && actual.searchParams.get('state') !== input.state)
    || actual.searchParams.has('access_token') || actual.searchParams.has('id_token')
    || (actual.searchParams.has('code') === actual.searchParams.has('error'))) throw new Error('授权响应无效，请重新发起连接。')
  return actual.href
}

export function scopeName(scope: string): string { return connectionScopes.find((item) => item.value === scope)?.name ?? '未知访问范围' }
