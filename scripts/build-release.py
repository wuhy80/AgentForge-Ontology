"""在开发机或 CI 构建 Linux/amd64 制品；禁止在已配置的业务服务器构建。"""
import argparse
import os
from pathlib import Path
import shutil
import subprocess
import tempfile

from release_archive import create, verify

REPO = Path(__file__).resolve().parent.parent


def build(output):
    if Path('/etc/agentforge-ontology/metadata.env').exists():
        raise SystemExit('业务服务器只安装制品，请在开发机或 GitHub Actions 构建')
    revision = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=REPO, text=True).strip()
    if subprocess.check_output(['git', 'status', '--porcelain'], cwd=REPO):
        raise SystemExit('请先提交源码修改，制品必须对应一个明确提交')
    env = os.environ.copy()
    env.update(CGO_ENABLED='0', GOOS='linux', GOARCH='amd64', GOWORK='off', GOMAXPROCS='2')
    with tempfile.TemporaryDirectory(prefix='agentforge-release-') as temporary:
        target = Path(temporary)
        (target / 'bin').mkdir()
        for service in ['metadata', 'data', 'execution', 'mcp']:
            subprocess.run(['go', 'build', '-p=2', '-trimpath', '-ldflags=-s -w', '-o', str(target / 'bin' / service), './cmd/server'], cwd=REPO / 'services' / service, env=env, check=True)
        npm = 'npm.cmd' if os.name == 'nt' else 'npm'
        subprocess.run([npm, 'ci', '--no-audit', '--no-fund'], cwd=REPO / 'apps/web', check=True)
        subprocess.run([npm, 'run', 'build'], cwd=REPO / 'apps/web', check=True)
        shutil.copytree(REPO / 'apps/web/dist', target / 'web')
        shutil.copytree(REPO / 'deploy/single-host', target / 'deploy/single-host')
        shutil.copytree(REPO / 'deploy/opa', target / 'deploy/opa')
        for path in (target / 'deploy').rglob('*'):
            if path.is_file():
                path.write_bytes(path.read_bytes().replace(b'\r\n', b'\n'))
        create(target, output, revision)
        verify(output, revision)
    print('Linux 制品：' + str(output))


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', type=Path, default=REPO / '.cache/artifacts/ontology-linux-amd64.tar.gz')
    build(parser.parse_args().output.resolve())
