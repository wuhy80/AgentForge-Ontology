"""Exercise the actual public Nginx config against synthetic local upstreams.

Requires Docker, nginx:stable-alpine, Go 1.26.10 and OpenSSL. No production endpoint,
credential, trusted certificate store or database is used.
"""
import argparse
import http.client
import json
import os
from pathlib import Path
import shutil
import ssl
import subprocess
import tempfile
import time
import uuid


def run(*args, env=None):
    return subprocess.check_output(args, text=True, stderr=subprocess.STDOUT, env=env).strip()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--openssl', default=shutil.which('openssl') or r'C:\Program Files\Git\usr\bin\openssl.exe')
    args = parser.parse_args()
    root = Path(__file__).resolve().parents[1]
    review = root / '.cache/security-review'
    review.mkdir(parents=True, exist_ok=True)
    name = 'ontology-gateway-test-' + uuid.uuid4().hex[:10]
    started = False
    with tempfile.TemporaryDirectory(prefix='gateway-', dir=review) as temp:
        temp = Path(temp).resolve()
        assert temp.is_relative_to(review.resolve())
        run(args.openssl, 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '1',
            '-subj', '/CN=localhost', '-keyout', str(temp / 'server.key'), '-out', str(temp / 'server.crt'))
        config = (root / 'deploy/single-host/nginx.conf').read_text(encoding='utf-8')
        (temp / 'nginx.conf').write_text(config, encoding='utf-8')
        # A real HTTP upstream must return 200 so Nginx's handling of
        # conditional writes is exercised without a synthetic 201 shortcut.
        (temp / 'main.go').write_text(r'''package main

import (
    "encoding/json"
    "log"
    "net"
    "net/http"
)

func main() {
    handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "application/json")
        w.Header().Set("ETag", "\"new-etag\"")
        w.Header().Set("X-Review-Match", r.Header.Get("If-Match"))
        w.WriteHeader(http.StatusOK)
        json.NewEncoder(w).Encode(map[string]string{
            "path": r.URL.RequestURI(), "method": r.Method, "host": r.Host,
            "client": r.Header.Get("X-Real-IP"), "tenant": r.Header.Get("X-Tenant-ID"),
            "user": r.Header.Get("X-User-ID"), "roles": r.Header.Get("X-Roles"),
            "authorization": r.Header.Get("Authorization"), "cookie": r.Header.Get("Cookie"),
            "csrf": r.Header.Get("X-CSRF-Token"), "origin": r.Header.Get("Origin"),
            "idempotency": r.Header.Get("Idempotency-Key"),
            "mcpVersion": r.Header.Get("MCP-Protocol-Version"),
            "requestId": r.Header.Get("X-Request-ID"),
        })
    })
    var listeners []net.Listener
    for _, address := range []string{"127.0.0.1:18080", "127.0.0.1:18081", "127.0.0.1:18082", "127.0.0.1:18083", "127.0.0.1:18084"} {
        listener, err := net.Listen("tcp", address)
        if err != nil {
            log.Fatal(err)
        }
        listeners = append(listeners, listener)
    }
    for _, listener := range listeners {
        go func() { log.Fatal(http.Serve(listener, handler)) }()
    }
    select {}
}
''', encoding='utf-8')
        build_env = dict(os.environ, GOTOOLCHAIN='go1.26.10', GOWORK='off', CGO_ENABLED='0',
                         GOOS='linux', GOARCH='amd64', GOCACHE=str(root / '.cache/go-build'))
        run('go', 'build', '-trimpath', '-o', str(temp / 'upstream'), str(temp / 'main.go'), env=build_env)
        mounts = [
            (temp / 'nginx.conf', '/etc/nginx/conf.d/default.conf'),
            (temp / 'upstream', '/review/upstream'),
            (temp, '/etc/agentforge-ontology/tls'),
            (root / 'deploy/single-host/security-headers.conf', '/etc/agentforge-ontology/nginx-security-headers.conf'),
            (root / 'deploy/single-host/proxy-headers.conf', '/etc/agentforge-ontology/nginx-proxy-headers.conf'),
        ]
        command = ['docker', 'run', '-d', '--name', name, '-p', '127.0.0.1::30011', '--entrypoint', 'sh']
        for source, destination in mounts:
            assert source.exists(), source
            command += ['--mount', f'type=bind,src={source},dst={destination},readonly']
        command += ['nginx:stable-alpine', '-ec', '/review/upstream & exec nginx -g "daemon off;"']
        try:
            run(*command)
            started = True
            for _ in range(30):
                info = json.loads(run('docker', 'inspect', name))[0]
                if info['NetworkSettings']['Ports'].get('30011/tcp'):
                    break
                if not info['State']['Running']:
                    raise RuntimeError(run('docker', 'logs', name))
                time.sleep(0.1)
            else:
                raise RuntimeError('local gateway did not start')
            run('docker', 'exec', name, 'nginx', '-t')
            port = int(info['NetworkSettings']['Ports']['30011/tcp'][0]['HostPort'])
            context = ssl._create_unverified_context()  # synthetic, loopback-only test certificate
            checks = 0

            def request(method, path, headers=None, body=None):
                connection = http.client.HTTPSConnection('127.0.0.1', port, timeout=8, context=context)
                all_headers = {'Host': '47.100.85.94:30011', **(headers or {})}
                connection.request(method, path, body=body, headers=all_headers)
                response = connection.getresponse()
                raw = response.read()
                status, result_headers = response.status, dict(response.getheaders())
                connection.close()
                try:
                    value = json.loads(raw) if raw else None
                except ValueError as error:
                    raise AssertionError((method, path, status, raw[:200])) from error
                return status, {k.lower(): v for k, v in result_headers.items()}, value

            synthetic = 'SYNTHETIC_MARKER_ONLY'
            headers = {'Authorization': 'Bearer '+synthetic, 'X-Tenant-ID': synthetic,
                       'X-User-ID': synthetic, 'X-Roles': synthetic, 'X-Forwarded-For': '198.51.100.5',
                       'X-Real-IP': '198.51.100.5', 'Cookie': 'ontology_session='+synthetic,
                       'X-CSRF-Token': synthetic, 'Origin': 'https://47.100.85.94:30011',
                       'If-Match': '"'+synthetic+'"', 'Idempotency-Key': synthetic}
            for method, path, expected_path in [
                ('GET', '/api/metadata/definitions/test?review_marker='+synthetic, '/metadata/definitions/test?review_marker='+synthetic),
                ('POST', '/api/metadata/auth/login', '/metadata/auth/login'),
                ('POST', '/api/ontoapp-ontology/metadata/auth/login', '/metadata/auth/login'),
                ('GET', '/api/ontoapp-ontology/metadata/auth/session', '/metadata/auth/session'),
                ('GET', '/api/ontoapp-ontology/metadata/definitions/test?operation=patch', '/data/schemas/test/form?operation=patch'),
                ('GET', '/api/ontoapp-ontology/metadata/definitions/a%2Db', '/data/schemas/a-b/form'),
                ('GET', '/api/ontoapp-ontology/data/query/context?limit=50', '/data/query/context?limit=50'),
                ('POST', '/api/data/query', '/data/query'),
                ('POST', '/api/data/transactions', '/data/transactions'),
                ('POST', '/api/data/transactions/status', '/data/transactions/status'),
                ('POST', '/api/ontoapp-ontology/data/transactions', '/data/transactions'),
                ('POST', '/api/ontoapp-ontology/data/transactions/status', '/data/transactions/status'),
                ('GET', '/api/data/saved-queries?limit=20', '/data/saved-queries?limit=20'),
                ('GET', '/api/data/saved-queries/query-id', '/data/saved-queries/query-id'),
                ('POST', '/api/data/saved-queries', '/data/saved-queries'),
                ('POST', '/api/data/saved-queries/compile', '/data/saved-queries/compile'),
                ('POST', '/api/data/saved-queries/query-id/execute', '/data/saved-queries/query-id/execute'),
                ('PUT', '/api/data/saved-queries/query-id', '/data/saved-queries/query-id'),
                ('DELETE', '/api/data/saved-queries/query-id', '/data/saved-queries/query-id'),
                ('GET', '/api/ontoapp-ontology/data/saved-queries', '/data/saved-queries'),
                ('POST', '/api/ontoapp-ontology/data/saved-queries/compile', '/data/saved-queries/compile'),
                ('POST', '/api/ontoapp-ontology/data/saved-queries/query-id/execute', '/data/saved-queries/query-id/execute'),
                ('PUT', '/api/ontoapp-ontology/data/saved-queries/query-id', '/data/saved-queries/query-id'),
                ('DELETE', '/api/ontoapp-ontology/data/saved-queries/query-id', '/data/saved-queries/query-id'),
                ('PATCH', '/api/ontoapp-ontology/data/schemas/model/instances/record', '/data/schemas/model/instances/record'),
                ('POST', '/api/execution/invocations', '/execution/invocations'),
                ('GET', '/api/data/sources/source/tables/a%20b', '/data/sources/source/tables/a%20b'),
                ('GET', '/api/ontoapp/workspace', '/api/ontoapp/workspace'),
            ]:
                forwarded_headers = dict(headers)
                status, response_headers, value = request(method, path, forwarded_headers)
                assert status == 200, (method, path, status)
                assert value['path'] == expected_path and value['method'] == method, (path, value['path'])
                assert all(value[key] == '' for key in ['authorization', 'tenant', 'user', 'roles']), path
                assert value['host'] == '47.100.85.94:30011' and value['client'] != '198.51.100.5', path
                assert all(value[key] == headers[header] for key, header in [('cookie','Cookie'),('csrf','X-CSRF-Token'),('origin','Origin'),('idempotency','Idempotency-Key')]), path
                assert response_headers.get('x-review-match', '') == forwarded_headers.get('If-Match', ''), path
                assert value['requestId'] and response_headers.get('cache-control') == 'no-store', path
                checks += 1
            for method, path in [
                ('PATCH', '/api/data/schemas/model/instances/record'),
                ('PUT', '/api/metadata/definitions/model/draft'),
                ('POST', '/api/metadata/definitions/model/publish'),
            ]:
                status, response_headers, value = request(method, path,
                    {'If-Match': '"old-etag"', 'Content-Type': 'application/json'}, '{}')
                assert status == 200 and value['method'] == method, (method, path, status)
                assert response_headers.get('x-review-match') == '"old-etag"', (method, path)
                assert response_headers.get('etag') == '"new-etag"', (method, path)
                checks += 1
            # Every new mutation is explicitly allowed on both browser aliases.
            # Keep this group separate from the login/shared-bucket assertions.
            for prefix in ['data', 'ontoapp-ontology/data']:
                for suffix in ['upsert', 'bulk-create', 'bulk-update', 'bulk-delete', 'bulk-upsert']:
                    path = f'/api/{prefix}/schemas/model/instances/{suffix}'
                    status, response_headers, value = request('POST', path,
                        {'Content-Type': 'application/json', 'Origin': headers['Origin'],
                         'X-CSRF-Token': synthetic, 'Authorization': 'Bearer '+synthetic}, '{}')
                    assert status == 200 and value['path'] == f'/data/schemas/model/instances/{suffix}', (path, status)
                    assert value['authorization'] == '' and value['csrf'] == synthetic
                    assert response_headers.get('cache-control') == 'no-store'
                    checks += 1
                path = f'/api/{prefix}/schemas/model/write-capabilities'
                status, _, value = request('GET', path)
                assert status == 200 and value['path'] == '/data/schemas/model/write-capabilities'
                checks += 1
            for method, path in [('GET','/api/data/transactions'),('PUT','/api/data/transactions/status'),
                                 ('DELETE','/api/data/saved-queries'),('PATCH','/api/data/saved-queries/query-id'),
                                 ('GET','/api/data/saved-queries/query-id/execute'),('POST','/api/data/saved-queries/query-id'),
                                 ('GET','/api/ontoapp-ontology/data/saved-queries/query-id/execute'),
                                 ('POST','/api/data/transactions/unknown'),('POST','/internal/execution/transactions'),
                                 ('POST','/api/data/internal/execution/transactions'),('DELETE','/api/data/query'),('GET','/api/metadata/auth/unknown'),
                                 ('GET','/api/ontoapp-ontology/metadata/admin/users'),('GET','/api/execution/unknown'),
                                 ('POST','/api/data/schemas/model/instances/bulk-execute'),
                                 ('PUT','/api/data/schemas/model/instances/bulk-update'),
                                 ('POST','/api/data/../../internal/identity/session'),
                                 ('POST','/api/data/%2e%2e/%2e%2e/internal/identity/session'),
                                 ('GET','/api/%64ata/query/context')]:
                status, _, _ = request(method, path)
                assert status == 404, (method, path, status)
                checks += 1
            mcp = {'Authorization':'Bearer mcp_'+('a'*64), 'MCP-Protocol-Version':'2026-07-28'}
            status, _, value = request('POST','/mcp',mcp)
            assert status == 200 and value['authorization'] == mcp['Authorization'] and value['mcpVersion'] == mcp['MCP-Protocol-Version']
            status, _, value = request('POST','/mcp',{'Authorization':'Bearer '+synthetic})
            assert status == 200 and value['authorization'] == ''
            checks += 2
            # Oversized declared length must fail before reading the body.
            status, response_headers, value = request('POST','/api/data/query',{'Content-Length':str(9<<20)})
            assert status == 413 and value['code'] == 'request_too_large' and response_headers.get('cache-control') == 'no-store'
            checks += 1
            # Different host/port cannot reach any upstream.
            try:
                request('GET','/api/data/query/context',{'Host':'attacker.test'})
            except http.client.RemoteDisconnected:
                checks += 1
            else:
                raise AssertionError('untrusted Host accepted')
            # Both public login prefixes share the same edge bucket. Previous
            # requests stay below the global burst; separate login buckets
            # would incorrectly admit all of these requests.
            login_statuses = []
            # The mutation route matrix can exhaust the unrelated global burst.
            time.sleep(1.5)
            for prefix in ['metadata'] * 6 + ['ontoapp-ontology/metadata'] * 6:
                status, response_headers, value = request('POST',f'/api/{prefix}/auth/login')
                login_statuses.append(status)
                if status == 429:
                    assert value['code'] == 'service_busy' and response_headers.get('retry-after') == '1'
            assert login_statuses[:6] == [200] * 6 and 429 in login_statuses[6:], login_statuses
            checks += 1
            logs = run('docker','exec',name,'cat','/var/log/nginx/ontology-access.log')
            assert synthetic not in logs and 'review_marker' not in logs and '/api/' not in logs
            assert logs.strip(), 'privacy access log missing'
            checks += 1
            print(json.dumps({'checks':checks,'nginx':'passed','privacyLog':'passed','productionContacted':False}))
        finally:
            if started:
                run('docker','rm','-f',name)


if __name__ == '__main__':
    main()
