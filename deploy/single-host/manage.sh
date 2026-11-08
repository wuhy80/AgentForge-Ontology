#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

repo=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
state=/var/lib/agentforge-ontology
config=/etc/agentforge-ontology
cache=/var/cache/agentforge-ontology
services=(metadata data execution mcp)
units=()
for service in "${services[@]}"; do units+=("agentforge-ontology@$service.service"); done
[[ $EUID -eq 0 ]] || { echo '请使用 root 执行部署管理命令' >&2; exit 1; }
action=${1:-status}
exec 9>/var/lock/agentforge-ontology-deploy.lock
flock -n 9 || { echo '另一个部署操作正在进行' >&2; exit 1; }

compose() { docker compose --env-file "$config/compose.env" -f "$repo/deploy/single-host/compose.yaml" "$@"; }
revision() { git -C "$repo" rev-parse HEAD; }
release_path() { printf '%s/releases/%s' "$state" "$(revision)"; }
configure_units() {
    install -d -m 755 "$state" "$state/releases" "$cache"
    install -d -m 700 "$state/backups" "$config"
    for service in "${services[@]}"; do
        id "ontology-$service" &>/dev/null || useradd --system --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin "ontology-$service"
        [[ -s "$config/$service.env" ]] || { echo "缺少 $config/$service.env" >&2; exit 1; }
    done
    install -m 644 "$repo/deploy/single-host/agentforge-ontology@.service" /etc/systemd/system/
    install -d -m 755 /etc/systemd/system/agentforge-ontology@execution.service.d
    printf '[Service]\nSupplementaryGroups=docker\n' > /etc/systemd/system/agentforge-ontology@execution.service.d/docker.conf
    chmod 644 /etc/systemd/system/agentforge-ontology@execution.service.d/docker.conf
    systemctl daemon-reload
}
build_release() {
    local target
    target=$(release_path)
    [[ ! -e "$target/complete" ]] || { echo "已构建 $(revision)"; return; }
    install -d -m 755 "$target" "$target/bin" "$target/web" "$cache/go" "$cache/npm" "$cache/web"
    docker run --rm --cpus=1 --memory=768m --memory-swap=1536m \
        -v "$repo:/src:ro" -v "$target:/out" -v "$cache/go:/cache" \
        -e GOWORK=off -e CGO_ENABLED=0 -e GOMAXPROCS=1 -e GOCACHE=/cache/build -e GOMODCACHE=/cache/mod \
        -e GOPROXY="${ONTOLOGY_BUILD_GOPROXY:-https://goproxy.cn,https://proxy.golang.org,direct}" \
        golang:1.25-alpine sh -ec '
            for service in metadata data execution mcp; do
                cd "/src/services/$service"
                go build -p=1 -trimpath -ldflags="-s -w" -o "/out/bin/$service" ./cmd/server
            done'
    docker run --rm --cpus=1 --memory=768m --memory-swap=1536m \
        -v "$repo/apps/web:/source:ro" -v "$target:/out" -v "$cache/npm:/root/.npm" -v "$cache/web:/work" \
        -e NODE_OPTIONS=--max-old-space-size=512 node:22-alpine sh -ec '
            cp -R /source/. /work/
            cd /work
            npm ci --no-audit --no-fund
            npm run build
            cp -R dist/. /out/web/'
    chmod -R a+rX "$target"
    revision > "$target/complete"
    chmod 644 "$target/complete"
}
backup_data() {
    local target
    target="$state/backups/$(date -u +%Y%m%dT%H%M%SZ)-$(revision | cut -c1-12)"
    install -d -m 700 "$target"
    compose exec -T mysql sh -ec 'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" exec mysqldump -uroot --single-transaction --quick --hex-blob --routines --triggers --set-gtid-purged=OFF --column-statistics=0 --databases ontology_metadata ontology_data ontology_execution ontology_knowledge' | gzip > "$target/databases.sql.gz"
    tar -czf "$target/config.tar.gz" -C /etc agentforge-ontology
    readlink -f "$state/current" > "$target/release.txt"
    (cd "$target" && sha256sum databases.sql.gz config.tar.gz > SHA256SUMS)
    echo "备份保存在 $target"
}
migrate_release() {
    local target service
    target=$(release_path)
    [[ -s "$target/complete" ]] || { echo '请先构建发布目录' >&2; exit 1; }
    for service in metadata data execution; do
        (set -a; source "$config/$service.env"; set +a; "$target/bin/$service" -migrate)
    done
}
activate_release() {
    local target previous=''
    target=$(release_path)
    [[ -s "$target/complete" ]] || { echo '请先构建发布目录' >&2; exit 1; }
    [[ ! -L "$state/current" ]] || previous=$(readlink -f "$state/current")
    ln -sfn "$target" "$state/current.next"
    mv -Tf "$state/current.next" "$state/current"
    systemctl enable "${units[@]}" >/dev/null
    systemctl restart "${units[@]}"
    if ! ready; then
        echo "新发布启动失败，检查 journalctl -u 'agentforge-ontology@*'。旧发布：$previous；数据库迁移不自动回滚。" >&2
        return 1
    fi
    nginx -t
    systemctl reload nginx
    echo "已启用 $(revision)"
}
ready() {
    local port attempt
    for port in 18080 18081 18082 18084; do
        for attempt in $(seq 1 30); do
            if curl -fsS --max-time 3 "http://127.0.0.1:$port/readyz" >/dev/null; then break; fi
            [[ $attempt -lt 30 ]] || return 1
            sleep 1
        done
    done
}
case "$action" in
    prepare) configure_units; compose up -d --wait --wait-timeout 150 ;;
    build) build_release ;;
    migrate) migrate_release ;;
    activate) configure_units; activate_release ;;
    backup) backup_data ;;
    stop) systemctl stop "${units[@]}" ;;
    start) systemctl start "${units[@]}"; ready ;;
    update)
        git -C "$repo" diff --quiet && git -C "$repo" diff --cached --quiet
        git -C "$repo" pull --ff-only
        # 释放锁后执行拉取版本的脚本，避免更新仍使用旧脚本。
        flock -u 9
        exec bash "$repo/deploy/single-host/manage.sh" deploy
        ;;
    deploy)
        build_release
        configure_units
        systemctl stop "${units[@]}"
        backup_data
        migrate_release
        activate_release
        ;;
    status)
        git -C "$repo" log -1 --oneline
        readlink -f "$state/current" || true
        compose ps
        systemctl --no-pager --full status "${units[@]}"
        ready
        ;;
    *) echo '用法：manage.sh prepare|build|migrate|activate|backup|stop|start|update|status' >&2; exit 2 ;;
esac
