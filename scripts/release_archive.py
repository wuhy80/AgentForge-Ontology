"""生成、验证和提取 Linux 制品；不读取服务器环境文件或业务数据。"""
import argparse
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import tarfile

FORMAT = 'agentforge-release/v1'
REQUIRED = {
    'bin/metadata', 'bin/data', 'bin/execution', 'bin/mcp', 'web/index.html',
    'deploy/single-host/agentforge-ontology@.service',
    'deploy/single-host/nginx.conf', 'deploy/single-host/security-headers.conf',
    'deploy/single-host/proxy-headers.conf',
}


def digest(path):
    with Path(path).open('rb') as stream:
        return stream_digest(stream)


def stream_digest(stream):
    value = hashlib.sha256()
    while chunk := stream.read(1024 * 1024):
        value.update(chunk)
    return value.hexdigest()


def create(source, archive, revision):
    if not re.fullmatch(r'[0-9a-f]{40}', revision):
        raise ValueError('提交号必须是完整 SHA')
    source, archive = Path(source), Path(archive)
    files = {}
    for directory in ['bin', 'web', 'deploy']:
        for path in sorted((source / directory).rglob('*')):
            if path.is_symlink():
                raise ValueError('制品不能包含符号链接')
            if path.is_file():
                files[path.relative_to(source).as_posix()] = digest(path)
    if not REQUIRED.issubset(files):
        raise ValueError('制品缺少必要文件')
    manifest = {'format': FORMAT, 'revision': revision, 'target': 'linux/amd64', 'files': files}
    (source / 'manifest.json').write_text(json.dumps(manifest, indent=2) + '\n', encoding='utf-8')
    archive.parent.mkdir(parents=True, exist_ok=True)
    with tarfile.open(archive, 'w:gz') as output:
        for name in ['manifest.json', *files]:
            output.add(source / name, arcname=name, recursive=False)
    archive.with_name(archive.name + '.sha256').write_text(digest(archive) + '  ' + archive.name + '\n', encoding='ascii')


def verify(archive, revision=None, destination=None):
    with tarfile.open(archive, 'r:gz') as source:
        members = source.getmembers()
        if len(members) > 3000 or sum(item.size for item in members) > 512 * 1024 * 1024:
            raise ValueError('制品超过资源预算')
        names = set()
        for item in members:
            path = PurePosixPath(item.name)
            if (not item.isfile() or item.name in names or path.is_absolute()
                    or '..' in path.parts or '\\' in item.name or str(path) != item.name
                    or not (item.name == 'manifest.json' or path.parts[0] in {'bin', 'web', 'deploy'})):
                raise ValueError('制品含非法路径、链接或重复文件')
            names.add(item.name)
        if 'manifest.json' not in names or source.getmember('manifest.json').size > 1024 * 1024:
            raise ValueError('缺少有效制品清单')
        manifest = json.load(source.extractfile('manifest.json'))
        files = manifest.get('files', {})
        if (manifest.get('format') != FORMAT or manifest.get('target') != 'linux/amd64'
                or not re.fullmatch(r'[0-9a-f]{40}', manifest.get('revision', ''))
                or (revision and manifest['revision'] != revision)
                or not isinstance(files, dict) or not REQUIRED.issubset(files)
                or names != set(files) | {'manifest.json'}):
            raise ValueError('制品格式、目标、提交号或文件清单不匹配')
        for name, expected in files.items():
            if stream_digest(source.extractfile(name)) != expected:
                raise ValueError('制品文件校验失败：' + name)
        if destination:
            destination = Path(destination)
            destination.mkdir(mode=0o755, parents=False, exist_ok=False)
            for member in members:
                output = destination / member.name
                output.parent.mkdir(mode=0o755, parents=True, exist_ok=True)
                with output.open('xb') as stream, source.extractfile(member) as content:
                    while chunk := content.read(1024 * 1024):
                        stream.write(chunk)
                output.chmod(0o755 if member.name.startswith('bin/') else 0o644)
            # 服务器通常使用 umask 077；目录须允许各服务用户和 Nginx 遍历。
            for root, directories, _ in os.walk(destination):
                Path(root).chmod(0o755)
                for name in directories:
                    (Path(root) / name).chmod(0o755)
        return manifest


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=['verify', 'extract'])
    parser.add_argument('archive')
    parser.add_argument('--revision')
    parser.add_argument('--destination')
    args = parser.parse_args()
    if args.action == 'extract' and not args.destination:
        parser.error('extract 需要 --destination')
    result = verify(args.archive, args.revision, args.destination if args.action == 'extract' else None)
    print('制品校验通过：' + result['revision'])
