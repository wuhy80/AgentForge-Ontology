import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'

// Only the public service prefixes are proxied. The browser never receives a
// service token, signing key, database credential or an internal API route.
const proxy = {
  // OntoApp 与工作台共用公开来源，保留真实 Host、Origin 和 Cookie。
  '/ontoapp/': { target: 'http://127.0.0.1:5180', ws: true },
  '/api/ontoapp-ontology': {
    target: 'http://127.0.0.1:4173',
    rewrite: (path: string) => path.replace(/^\/api\/ontoapp-ontology/, '/api'),
  },
  '/api/ontoapp': { target: 'http://127.0.0.1:18083' },
  // OAuth authorization/discovery endpoints are public metadata endpoints;
  // they must not inherit a browser API token or be rewritten through MCP.
  '/oauth': { target: 'http://127.0.0.1:18080' },
  '/.well-known': { target: 'http://127.0.0.1:18080' },
  '/api/metadata': { target: 'http://127.0.0.1:18080', rewrite: (path: string) => path.replace(/^\/api/, '') },
  '/api/data': { target: 'http://127.0.0.1:18081', rewrite: (path: string) => path.replace(/^\/api/, '') },
  '/api/execution': { target: 'http://127.0.0.1:18082', rewrite: (path: string) => path.replace(/^\/api/, '') },
}

export default defineConfig({
  plugins: [react()],
  server: { host: '127.0.0.1', port: 5173, strictPort: true, proxy },
  preview: { host: '127.0.0.1', port: 4173, strictPort: true, proxy },
  test: { environment: 'jsdom', include: ['src/**/*.test.{ts,tsx}'], setupFiles: ['./src/test/setup.ts'], restoreMocks: true },
})
