import { describe, expect, it } from 'vitest'
import { oauthResultURL, parseOAuthAuthorization, validateOAuthPreview } from './external-auth'

const codeChallenge = 'A'.repeat(43)
const query = (extra = '') => `oauth/authorize?client_id=client-a&redirect_uri=${encodeURIComponent('https://client.example/callback')}&response_type=code&code_challenge_method=S256&code_challenge=${codeChallenge}&scope=ontology%3Adiscover%20ontology%3Aquery&resource=${encodeURIComponent('http://127.0.0.1:18084/mcp')}${extra}`

describe('MCP OAuth client safety checks', () => {
  it('accepts only the supported authorization-code PKCE request', () => {
    const input = parseOAuthAuthorization(query('&state=opaque-state'))
    expect(input.client_id).toBe('client-a')
    expect(input.scope).toBe('ontology:discover ontology:query')
  })

  it.each([
    ['implicit flow', query().replace('response_type=code', 'response_type=token')],
    ['plain challenge', query().replace('code_challenge_method=S256', 'code_challenge_method=plain')],
    ['unknown parameter', query('&evil=https%3A%2F%2Fattacker.example')],
    ['invalid redirect scheme', query().replace(encodeURIComponent('https://client.example/callback'), encodeURIComponent('javascript:alert(1)'))],
  ])('rejects %s', (_name, route) => {
    expect(() => parseOAuthAuthorization(route)).toThrow()
  })

  it('requires the server preview to match the original request', () => {
    const input = parseOAuthAuthorization(query())
    const preview = { client: { clientId: 'client-a', name: '测试应用', redirectUris: ['https://client.example/callback'] }, scopes: ['ontology:discover', 'ontology:query'] as const, resource: 'http://127.0.0.1:18084/mcp' }
    expect(validateOAuthPreview(input, preview)).toEqual(preview)
    expect(() => validateOAuthPreview(input, { ...preview, resource: 'https://other.example/mcp' })).toThrow()
  })

  it('accepts a server-built code or error redirect only at the approved destination', () => {
    const input = parseOAuthAuthorization(query('&state=opaque-state'))
    expect(oauthResultURL(input, 'https://client.example/callback?code=one&state=opaque-state')).toContain('code=one')
    expect(oauthResultURL(input, 'https://client.example/callback?error=access_denied&state=opaque-state')).toContain('access_denied')
    expect(() => oauthResultURL(input, 'https://attacker.example/callback?code=one&state=opaque-state')).toThrow()
    expect(() => oauthResultURL(input, 'https://client.example/callback?code=one&state=other')).toThrow()
  })
})
