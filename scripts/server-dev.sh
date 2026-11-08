#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

repo=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
state=/var/lib/agentforge-ontology
config=/etc/agentforge-ontology
cache=/var/cache/agentforge-ontology
development="$state/development"
artifact=${ARTIFACT:-$cache/artifacts/ontology-linux-amd64.tar.gz}
services=(metadata data execution mcp)
units=()
for service in "${services[@]}"; do units+=("agentforge-ontology@$service.service"); done
action=${1:-status}
[[ $EUID -eq 0 ]] || { echo '请使用 sudo make 或 root 执行服务器管理命令' >&2; exit 1; }
case "$action" in run|stage|stop|status|logs|migrate|backup) ;; *) echo '未知管理命令' >&2; exit 2 ;; esac
if [[ "$action" != status && "$action" != logs ]]; then
    exec 9>/var/lock/agentforge-ontology-deploy.lock
    flock -n 9 || { echo '另一个部署操作正在进行' >&2; exit 1; }
fi
compose() { docker compose --env-file "$config/compose.env" -f "$repo/deploy/single-host/compose.yaml" "$@"; }
check_config() {
    for service in "${services[@]}"; do
        [[ -s "$config/$service.env" ]] || { echo "缺少 $config/$service.env" >&2; exit 1; }
    done
    [[ -s "$config/compose.env" ]] || { echo '缺少数据库容器配置' >&2; exit 1; }
}
stage() {
    [[ -s "$artifact" ]] || { echo '未找到部署制品，请先在开发机或 GitHub Actions 构建并传入服务器' >&2; exit 1; }
    local revision target
    revision=$(git -C "$repo" rev-parse HEAD)
    python3 "$repo/scripts/release_archive.py" verify "$artifact" --revision "$revision"
    target="$development/releases/${revision:0:12}-$(date -u +%Y%m%dT%H%M%S%N)-$$"
    install -d -m 755 "$development" "$development/releases"
    python3 "$repo/scripts/release_archive.py" extract "$artifact" --revision "$revision" --destination "$target"
    printf '%s\n' "$revision" > "$target/revision"
    chmod 644 "$target/revision"
    ln -sfn "$target" "$development/latest.next"
    mv -Tf "$development/latest.next" "$development/latest"
    echo "制品已准备：$target"
}
latest() {
    local target
    target=$(readlink -f "$development/latest")
    [[ "$target" == "$development/releases/"* && -s "$target/revision" ]] || { echo '请先执行 make stage' >&2; return 1; }
    [[ "$(cat "$target/revision")" == "$(git -C "$repo" rev-parse HEAD)" ]] || { echo '已准备制品与当前源码提交不同，请重新 make stage' >&2; return 1; }
    printf '%s\n' "$target"
}
check_release() {
    local target=$1
    bash "$target/deploy/single-host/check-tls.sh" "$config/tls/server.crt" "$config/tls/server.key" "${ONTOLOGY_TLS_HOST:-47.100.85.94}"
    (
        set -a; source "$config/execution.env"; set +a
        [[ "${ONTOLOGY_EXECUTION_ISOLATION:-sandbox}" == sandbox ]] || { echo '服务器执行必须使用 sandbox 隔离' >&2; exit 1; }
        export DOCKER_HOST=unix:///run/ontology-sandbox/docker.sock
        unset DOCKER_CONTEXT DOCKER_TLS_VERIFY DOCKER_CERT_PATH
        runuser --preserve-environment -u ontology-execution -- "$target/bin/execution" -check-isolation
    )
}
ready() {
    local port attempt
    for port in 18080 18081 18082 18084; do
        for attempt in $(seq 1 30); do
            if curl -fsS --max-time 3 "http://127.0.0.1:$port/readyz" >/dev/null 2>&1; then break; fi
            if [[ $attempt -eq 30 ]]; then echo "服务 $port 未就绪，请运行 make logs" >&2; return 1; fi
            sleep 1
        done
    done
}
backup() {
    local target="$state/backups/$(date -u +%Y%m%dT%H%M%S%N)-server-dev"
    install -d -m 700 "$target"
    compose exec -T mysql sh -ec 'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" exec mysqldump -uroot --single-transaction --quick --hex-blob --routines --triggers --set-gtid-purged=OFF --column-statistics=0 --databases ontology_metadata ontology_data ontology_execution ontology_knowledge ontoapp_config' | gzip > "$target/databases.sql.gz"
    tar -czf "$target/config.tar.gz" -C /etc agentforge-ontology
    readlink -f "$state/current" > "$target/release.txt"
    (cd "$target" && sha256sum databases.sql.gz config.tar.gz > SHA256SUMS)
    echo "五库及配置备份：$target"
}
run() {
    check_config
    stage
    local target previous=''
    target=$(latest)
    check_release "$target"
    compose up -d --wait --wait-timeout 150
    cp -a /etc/nginx/sites-available/agentforge-ontology "$target/nginx.previous.conf"
    install -m 644 "$target/deploy/single-host/security-headers.conf" "$config/nginx-security-headers.conf"
    install -m 644 "$target/deploy/single-host/proxy-headers.conf" "$config/nginx-proxy-headers.conf"
    install -m 644 "$target/deploy/single-host/nginx.conf" /etc/nginx/sites-available/agentforge-ontology
    if ! nginx -t; then
        cp -a "$target/nginx.previous.conf" /etc/nginx/sites-available/agentforge-ontology
        return 1
    fi
    [[ ! -L "$state/current" ]] || previous=$(readlink -f "$state/current")
    echo '停止旧程序'
    systemctl stop "${units[@]}"
    install -m 644 "$target/deploy/single-host/agentforge-ontology@.service" /etc/systemd/system/
    ln -sfn "$target" "$state/current.next"
    mv -Tf "$state/current.next" "$state/current"
    systemctl daemon-reload
    systemctl enable "${units[@]}" >/dev/null
    echo '启动新程序'
    systemctl start "${units[@]}"
    if ! ready; then
        echo "新程序启动失败。旧制品：$previous；本命令未执行数据库迁移。" >&2
        return 1
    fi
    systemctl reload nginx
    echo '更新完成，systemd 已在后台运行；终端可以退出。'
    echo '入口：https://47.100.85.94:30011'
}
case "$action" in
    run) run ;;
    stage) stage ;;
    migrate)
        check_config
        target=$(latest)
        for service in metadata data execution; do
            (set -a; source "$config/$service.env"; set +a; "$target/bin/$service" -migrate)
        done
        echo '显式平台迁移完成'
        ;;
    backup) check_config; backup ;;
    stop) systemctl stop "${units[@]}"; echo '应用服务已停止；数据库和 OPA 继续运行' ;;
    status)
        readlink -f "$state/current"
        systemctl --no-pager --full status "${units[@]}"
        compose ps
        ready
        ;;
    logs) journalctl -u 'agentforge-ontology@*' -n 100 --no-pager ;;
esac
