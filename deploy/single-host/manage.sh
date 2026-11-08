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
check_tls() {
    bash "$repo/deploy/single-host/check-tls.sh" "$config/tls/server.crt" "$config/tls/server.key" "${ONTOLOGY_TLS_HOST:-47.100.85.94}"
}
configure_units() {
    install -d -m 755 "$state" "$state/releases" "$cache"
    install -d -m 700 "$state/backups" "$config"
    for service in "${services[@]}"; do
        getent group "ontology-$service" >/dev/null || groupadd --system "ontology-$service"
        id "ontology-$service" &>/dev/null || useradd --system --gid "ontology-$service" --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin "ontology-$service"
        [[ -s "$config/$service.env" ]] || { echo "缺少 $config/$service.env" >&2; exit 1; }
    done
    install -m 644 "$repo/deploy/single-host/agentforge-ontology@.service" /etc/systemd/system/
    install -d -m 755 /etc/systemd/system/agentforge-ontology@execution.service.d
    rm -f /etc/systemd/system/agentforge-ontology@execution.service.d/docker.conf
    install -m 644 "$repo/deploy/single-host/security-headers.conf" "$config/nginx-security-headers.conf"
    install -m 644 "$repo/deploy/single-host/proxy-headers.conf" "$config/nginx-proxy-headers.conf"
    # Rootless Docker with cgroup v2 requires the dedicated user's systemd
    # manager and bus. A system unit with User= is not a supported substitute.
    local sandbox_uid sandbox_home=/var/lib/ontology-sandbox
    id ontology-sandbox &>/dev/null || { echo '请先按部署文档准备ontology-sandbox用户、subuid/subgid、rootless Docker和runsc' >&2; exit 1; }
    sandbox_uid=$(id -u ontology-sandbox)
    [[ "$sandbox_uid" != 0 && " $(id -nG ontology-sandbox) " != *" docker "* ]] || { echo '沙箱用户不得具有root或宿主docker组权限' >&2; exit 1; }
    [[ "$(id -g ontology-sandbox)" == "$(getent group ontology-execution | cut -d: -f3)" && "$(getent passwd ontology-sandbox | cut -d: -f6)" == "$sandbox_home" ]] || { echo '沙箱用户须使用文档指定的主目录及ontology-execution主组' >&2; exit 1; }
    for binary in dockerd-rootless.sh rootlesskit newuidmap newgidmap; do
        command -v "$binary" >/dev/null || { echo "缺少rootless执行依赖：$binary" >&2; exit 1; }
    done
    [[ -x /usr/local/bin/runsc ]] || { echo '缺少/usr/local/bin/runsc' >&2; exit 1; }
    awk -F: -v user=ontology-sandbox '$1==user && $3+0 >= 65536 { found=1 } END { exit found ? 0 : 1 }' /etc/subuid || { echo 'ontology-sandbox没有至少65536个独立subuid' >&2; exit 1; }
    awk -F: -v user=ontology-sandbox '$1==user && $3+0 >= 65536 { found=1 } END { exit found ? 0 : 1 }' /etc/subgid || { echo 'ontology-sandbox没有至少65536个独立subgid' >&2; exit 1; }
    [[ " $(id -nG ontology-execution) " != *" docker "* ]] || gpasswd -d ontology-execution docker
    install -d -m 700 -o ontology-sandbox -g ontology-execution "$sandbox_home" "$sandbox_home/.config" "$sandbox_home/.config/systemd" "$sandbox_home/.config/systemd/user"
    install -m 644 -o ontology-sandbox -g ontology-execution "$repo/deploy/single-host/agentforge-sandbox.service" "$sandbox_home/.config/systemd/user/"
    printf 'd /run/ontology-sandbox 0710 ontology-sandbox ontology-execution -\n' > /etc/tmpfiles.d/agentforge-sandbox.conf
    systemd-tmpfiles --create /etc/tmpfiles.d/agentforge-sandbox.conf
    install -d -m 755 /etc/agentforge-sandbox
    install -m 644 "$repo/deploy/single-host/sandbox-daemon.json" /etc/agentforge-sandbox/daemon.json
    install -d -m 755 "/etc/systemd/system/user@$sandbox_uid.service.d"
    printf '[Service]\nDelegate=cpu cpuset io memory pids\n' > "/etc/systemd/system/user@$sandbox_uid.service.d/delegate.conf"
    systemctl daemon-reload
    loginctl enable-linger ontology-sandbox
    systemctl start "user@$sandbox_uid.service"
    runuser -u ontology-sandbox -- env "XDG_RUNTIME_DIR=/run/user/$sandbox_uid" "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/$sandbox_uid/bus" systemctl --user daemon-reload
    runuser -u ontology-sandbox -- env "XDG_RUNTIME_DIR=/run/user/$sandbox_uid" "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/$sandbox_uid/bus" systemctl --user enable --now agentforge-sandbox.service
    printf '[Unit]\nRequires=user@%s.service\nAfter=user@%s.service\n[Service]\nUnsetEnvironment=DOCKER_CONTEXT DOCKER_TLS_VERIFY DOCKER_CERT_PATH\nEnvironment=DOCKER_HOST=unix:///run/ontology-sandbox/docker.sock ONTOLOGY_EXECUTION_ISOLATION=sandbox\n' "$sandbox_uid" "$sandbox_uid" > /etc/systemd/system/agentforge-ontology@execution.service.d/sandbox.conf
    chmod 644 /etc/systemd/system/agentforge-ontology@execution.service.d/sandbox.conf
    systemctl daemon-reload
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
    build)
        echo '业务服务器已禁用编译；请在开发机执行 python scripts/build-release.py 或使用 GitHub Actions' >&2
        exit 1
        ;;
    migrate|backup)
        flock -u 9
        exec bash "$repo/scripts/server-dev.sh" "$action"
        ;;
    activate|deploy|update)
        flock -u 9
        exec bash "$repo/scripts/server-dev.sh" run
        ;;
    stop) systemctl stop "${units[@]}" ;;
    start) systemctl start "${units[@]}"; ready ;;
    status)
        git -C "$repo" log -1 --oneline
        readlink -f "$state/current" || true
        compose ps
        systemctl --no-pager --full status "${units[@]}"
        ready
        ;;
    check-tls) check_tls ;;
    *) echo '用法：manage.sh prepare|build|migrate|activate|backup|stop|start|update|status|check-tls' >&2; exit 2 ;;
esac
