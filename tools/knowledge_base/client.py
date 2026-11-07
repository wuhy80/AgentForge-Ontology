"""Authenticated public-API client; no direct database or service credentials."""
from __future__ import annotations

import http.cookiejar
import json
import urllib.error
import urllib.parse
import urllib.request


class ImportFailure(RuntimeError):
    pass


class ApiFailure(ImportFailure):
    def __init__(self, status: int, code: str, message: str):
        super().__init__(f"HTTP {status} {code}: {message}")
        self.status = status
        self.code = code


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise ImportFailure("API 重定向已拒绝，请直接指定工作台的同源地址")


class Client:
    def __init__(self, base_url: str):
        parsed = urllib.parse.urlsplit(base_url)
        if parsed.scheme not in ("http", "https") or not parsed.netloc or parsed.username or parsed.password or parsed.path not in ("", "/") or parsed.query or parsed.fragment:
            raise ImportFailure("--url 必须是工作台同源地址，例如 http://127.0.0.1:5173")
        if parsed.scheme == "http" and parsed.hostname not in ("127.0.0.1", "localhost", "::1"):
            raise ImportFailure("远程工作台须使用 HTTPS")
        self.base_url = base_url.rstrip("/")
        self.csrf = ""
        self.opener = urllib.request.build_opener(
            urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()), NoRedirect()
        )

    def request(self, method: str, path: str, body=None, etag: str | None = None):
        if not path.startswith("/api/") or path.startswith("//"):
            raise ImportFailure("仅允许调用当前工作台公开 API")
        headers = {"Accept": "application/json", "Origin": self.base_url}
        data = None
        if body is not None:
            headers["Content-Type"] = "application/json; charset=utf-8"
            data = json.dumps(body, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
        if self.csrf:
            headers["X-CSRF-Token"] = self.csrf
        if etag:
            headers["If-Match"] = '"' + etag.strip('"') + '"'
        request = urllib.request.Request(self.base_url + path, data=data, headers=headers, method=method)
        try:
            with self.opener.open(request, timeout=45) as response:
                raw = response.read()
                return json.loads(raw) if raw else None
        except urllib.error.HTTPError as error:
            try:
                detail = json.loads(error.read())
                detail = detail.get("error", detail)
                code = detail.get("code", "api_error")
                message = detail.get("message", "服务拒绝请求")
            except (ValueError, AttributeError):
                code, message = "api_error", "服务返回非 JSON 错误，请检查本地日志"
            raise ApiFailure(error.code, code, message) from None
        except (urllib.error.URLError, TimeoutError, OSError) as error:
            # Do not print request bodies, credentials, cookies, or source text.
            raise ImportFailure(f"无法确认 API 结果（{type(error).__name__}）；保留状态后重新执行可恢复") from None

    def login(self, tenant: str, username: str, password: str):
        session = self.request("POST", "/api/metadata/auth/login", {"tenant": tenant, "username": username, "password": password})
        self.csrf = session["csrfToken"]
        return session

    def logout(self):
        if self.csrf:
            try:
                self.request("POST", "/api/metadata/auth/logout")
            finally:
                self.csrf = ""

    def catalog(self, path: str, **filters):
        rows, seen = [], set()
        params = {**filters, "limit": 200}
        for _ in range(100):
            page = self.request("GET", path + "?" + urllib.parse.urlencode(params))
            rows.extend(page["data"])
            if not page["hasMore"]:
                return rows
            cursor = page.get("nextCursor")
            if not cursor or cursor in seen:
                raise ImportFailure("目录续页缺失或重复，停止导入")
            seen.add(cursor)
            params["after"] = cursor
        raise ImportFailure("目录超过 20,000 项预算，请缩小范围")

    def query(self, schema_id: str, fields: list[str], where=None):
        spec = {"from": {"schemaId": schema_id}, "select": fields, "page": {"limit": 100}}
        if where:
            spec["where"] = where
        result, seen = [], set()
        for _ in range(200):
            page = self.request("POST", "/api/data/query", spec)
            result.extend(page["data"])
            if not page["hasMore"]:
                return result
            cursor = page.get("nextCursor")
            if not cursor or cursor in seen:
                raise ImportFailure("查询续页缺失或重复，停止导入")
            seen.add(cursor)
            spec["page"]["cursor"] = cursor
        raise ImportFailure("实例查询超过 20,000 项预算，请缩小范围")
