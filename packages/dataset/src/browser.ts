import { DataSetError } from './errors.js';
import type { QueryBinding, QueryResult, QuerySpec, QueryTransport } from './types.js';

const maxBytes = 8 << 20;

export interface BrowserTransportOptions {
  /** Same-origin public gateway path, without a URL origin or query string. */
  basePath?: string;
  /** Read from the current session in memory; the SDK never persists tokens. */
  csrfToken: () => string;
  onUnauthorized?: () => void;
  fetch?: typeof globalThis.fetch;
  timeoutMs?: number;
}

export interface BrowserQueryTransport extends QueryTransport {
  /** Call on logout/session replacement to discard pending responses too. */
  invalidate(): void;
}

const record = (value: unknown): value is Record<string, unknown> => value !== null && typeof value === 'object' && !Array.isArray(value);

async function readJSON(response: Response, signal: AbortSignal): Promise<unknown> {
  const declared = response.headers.get('Content-Length');
  if (declared !== null && /^\d+$/.test(declared) && Number(declared) > maxBytes) {
    void response.body?.cancel().catch(() => {});
    throw new DataSetError('查询响应超过8MiB预算', 'response_budget', 422);
  }
  let text = '';
  if (response.body) {
    const reader = response.body.getReader();
    const decoder = new TextDecoder('utf-8', { fatal: true });
    const cancelRead = () => { void reader.cancel(signal.reason).catch(() => {}); };
    signal.addEventListener('abort', cancelRead, { once: true });
    if (signal.aborted) cancelRead();
    let size = 0;
    try {
      while (true) {
        signal.throwIfAborted();
        const chunk = await reader.read();
        signal.throwIfAborted();
        if (chunk.done) break;
        size += chunk.value.byteLength;
        if (size > maxBytes) {
          void reader.cancel().catch(() => {});
          throw new DataSetError('查询响应超过8MiB预算', 'response_budget', 422);
        }
        text += decoder.decode(chunk.value, { stream: true });
      }
      text += decoder.decode();
    } finally { signal.removeEventListener('abort', cancelRead); reader.releaseLock(); }
  }
  if (!text) throw new DataSetError('查询服务没有返回JSON', 'invalid_response', 502);
  try { return JSON.parse(text); }
  catch { throw new DataSetError('查询服务返回了无效JSON', 'invalid_response', 502); }
}

/** Cookie/CSRF transport for an existing browser session. It performs no login. */
export function createBrowserTransport(options: BrowserTransportOptions): BrowserQueryTransport {
  const basePath = options.basePath ?? '/api/data';
  if (!/^\/(?!\/)(?:[a-zA-Z0-9_-]+\/)*[a-zA-Z0-9_-]+\/?$/.test(basePath)) {
    throw new DataSetError('basePath必须为同源公开网关路径', 'invalid_base_path');
  }
  if (typeof options.csrfToken !== 'function') throw new DataSetError('需要当前会话的CSRF读取函数', 'csrf_required');
  const timeoutMs = options.timeoutMs ?? 20_000;
  if (!Number.isInteger(timeoutMs) || timeoutMs < 1 || timeoutMs > 60_000) throw new DataSetError('请求超时须为1..60000毫秒', 'invalid_timeout');
  const fetcher = options.fetch ?? globalThis.fetch;
  if (typeof fetcher !== 'function') throw new DataSetError('当前环境不支持fetch', 'fetch_unavailable');
  const prefix = basePath.replace(/\/$/, '');
  const pending = new Set<AbortController>();
  let expired = false;

  function invalidate() {
    if (expired) return;
    expired = true;
    for (const controller of pending) controller.abort(new DataSetError('当前会话已失效', 'unauthenticated', 401));
  }

  async function request<T>(suffix: string, query: QuerySpec, signal?: AbortSignal): Promise<T> {
    if (expired) throw new DataSetError('当前会话已失效', 'unauthenticated', 401);
    signal?.throwIfAborted();
    const csrf = options.csrfToken();
    if (typeof csrf !== 'string' || !csrf || /[\x00-\x20\x7f]/.test(csrf)) throw new DataSetError('缺少当前会话CSRF，请重新登录', 'csrf_required');
    let body: string;
    try { body = JSON.stringify(query); }
    catch { throw new DataSetError('查询必须为可编码的JSON', 'invalid_query'); }
    if (typeof body !== 'string') throw new DataSetError('查询必须为JSON对象', 'invalid_query');
    if (new TextEncoder().encode(body).byteLength > maxBytes) throw new DataSetError('查询请求超过8MiB预算', 'request_budget', 422);
    const controller = new AbortController();
    const abort = () => controller.abort(signal?.reason);
    signal?.addEventListener('abort', abort, { once: true });
    if (signal?.aborted) abort();
    pending.add(controller);
    const timer = setTimeout(() => controller.abort(new DOMException('查询请求超时', 'TimeoutError')), timeoutMs);
    try {
      if (expired) throw new DataSetError('当前会话已失效', 'unauthenticated', 401);
      controller.signal.throwIfAborted();
      const response = await fetcher(`${prefix}${suffix}`, {
        method: 'POST',
        headers: { Accept: 'application/json', 'Content-Type': 'application/json; charset=utf-8', 'X-CSRF-Token': csrf },
        body, signal: controller.signal, credentials: 'same-origin', mode: 'same-origin', redirect: 'error', cache: 'no-store',
      });
      if (response.status === 401 && !expired) {
        invalidate();
        try { options.onUnauthorized?.(); } catch { /* A callback cannot restore an expired transport. */ }
      }
      if (expired) throw new DataSetError('当前会话已失效', 'unauthenticated', 401);
      controller.signal.throwIfAborted();
      let payload: unknown;
      try { payload = await readJSON(response, controller.signal); }
      catch (cause) {
        if (response.ok || cause instanceof DataSetError && cause.code === 'response_budget' || controller.signal.aborted) throw cause;
        throw new DataSetError(`查询请求失败（HTTP ${response.status}）`, `http_${response.status}`, response.status);
      }
      if (expired) throw new DataSetError('当前会话已失效', 'unauthenticated', 401);
      if (!response.ok) {
        const error = record(payload) ? (record(payload.error) ? payload.error : payload) : {};
        throw new DataSetError(typeof error.message === 'string' ? error.message : `查询请求失败（HTTP ${response.status}）`, typeof error.code === 'string' ? error.code : `http_${response.status}`, response.status);
      }
      return payload as T;
    } catch (cause) {
      if (expired) throw new DataSetError('当前会话已失效', 'unauthenticated', 401);
      if (cause instanceof DataSetError) throw cause;
      if (controller.signal.aborted) throw controller.signal.reason;
      throw new DataSetError('无法连接查询服务或读取响应', 'network_error');
    } finally {
      clearTimeout(timer);
      signal?.removeEventListener('abort', abort);
      pending.delete(controller);
    }
  }

  return {
    bind: (query, signal) => request<QueryBinding>('/query/bind', query, signal),
    query: (query, signal) => request<QueryResult>('/query', query, signal),
    invalidate,
  };
}
