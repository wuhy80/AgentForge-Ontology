"""从成功的 GitHub Actions 下载制品，通过 SSH 交给服务器安装。"""
import argparse
import json
from pathlib import Path
import re
import shlex
import subprocess

from release_archive import digest, verify

REPO = Path(__file__).resolve().parent.parent
GITHUB_REPO = 'wuhy80/AgentForge-Ontology'


def deploy(run_id, host, port, directory, upload_only):
    run = json.loads(subprocess.check_output(['gh', 'api', f'repos/{GITHUB_REPO}/actions/runs/{run_id}'], text=True))
    if run['status'] != 'completed' or run['conclusion'] != 'success' or run['path'] != '.github/workflows/build-linux.yml':
        raise SystemExit('仅接受本仓库 Linux 构建工作流的成功运行')
    revision = run['head_sha']
    if not re.fullmatch(r'[0-9a-f]{40}', revision):
        raise SystemExit('无效提交号')
    destination = REPO / '.cache/artifacts' / str(run_id)
    destination.mkdir(parents=True, exist_ok=True)
    subprocess.run(['gh', 'run', 'download', str(run_id), '--repo', GITHUB_REPO, '--name', 'ontology-linux-amd64-' + revision, '--dir', str(destination)], check=True)
    archive = destination / 'ontology-linux-amd64.tar.gz'
    checksum = archive.with_name(archive.name + '.sha256')
    if checksum.read_text(encoding='ascii').split()[0] != digest(archive):
        raise SystemExit('制品整体摘要不匹配')
    verify(archive, revision)
    remote = '/var/cache/agentforge-ontology/incoming/' + str(run_id)
    ssh = ['ssh', '-p', str(port), '-o', 'BatchMode=yes', '-o', 'ConnectTimeout=15', host]
    # 先核对源码状态和提交；不覆盖服务器上的开发修改。
    prepare = f'''set -e
cd {shlex.quote(directory)}
test -z "$(git status --porcelain)" || {{ echo '服务器源码有未提交修改，请先处理'; exit 1; }}
git fetch origin {revision}
git merge --ff-only {revision}
install -d -m 700 {shlex.quote(remote)}
'''
    subprocess.run(ssh + ['bash', '-se'], input=prepare.encode(), check=True)
    subprocess.run(['scp', '-P', str(port), '-o', 'BatchMode=yes', '-o', 'ConnectTimeout=15', str(archive), str(checksum), host + ':' + remote + '/'], check=True)
    commands = f'''set -e
cd {shlex.quote(remote)}
sha256sum -c ontology-linux-amd64.tar.gz.sha256
install -d -m 700 /var/cache/agentforge-ontology/artifacts
ln -sfn {shlex.quote(remote + '/ontology-linux-amd64.tar.gz')} /var/cache/agentforge-ontology/artifacts/ontology-linux-amd64.tar.gz
cd {shlex.quote(directory)}
'''
    if not upload_only:
        commands += 'make run\n'
    subprocess.run(ssh + ['bash', '-se'], input=commands.encode(), check=True)
    print('制品已上传' if upload_only else '服务器更新完成')


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--run', type=int, required=True)
    parser.add_argument('--host', default='root@47.100.85.94')
    parser.add_argument('--port', type=int, default=22233)
    parser.add_argument('--directory', default='/root/github/agentforge')
    parser.add_argument('--upload-only', action='store_true', help='只传入制品，供显式迁移后手动 make run')
    args = parser.parse_args()
    deploy(args.run, args.host, args.port, args.directory, args.upload_only)
