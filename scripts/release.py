#!/usr/bin/env python3
"""Build deterministic Linux archives using the repository's pinned Go toolchain."""
import argparse
import gzip
import hashlib
import io
import json
import os
from pathlib import Path
import re
import subprocess
import tarfile
import tempfile

ROOT = Path(__file__).resolve().parent.parent


def run(*args, env=None):
    return subprocess.check_output(args, cwd=ROOT, env=env, text=True).strip()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--version', required=True)
    parser.add_argument('--output', required=True, type=Path)
    args = parser.parse_args()
    if not re.fullmatch(r'v\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?', args.version):
        parser.error('version must look like v1.2.3 or v1.2.3-beta.1')
    expected = 'go' + (ROOT / '.go-version').read_text().strip()
    env = dict(os.environ, GOTOOLCHAIN='local', GOENV='off', GOWORK='off',
               GOFLAGS='', CGO_ENABLED='0', GOOS='linux', GOAMD64='v1', GOARM64='v8.0')
    actual = run('go', 'env', 'GOVERSION', env=env)
    if actual != expected:
        parser.error(f'install {expected}; found {actual}')
    # Refuse stale output rather than accidentally publish unrelated files.
    args.output.mkdir(parents=True, exist_ok=True)
    if any(args.output.iterdir()):
        parser.error('output directory must be empty')
    revision = run('git', 'rev-parse', 'HEAD')
    run('go', 'mod', 'verify', env=env)
    with tempfile.TemporaryDirectory(prefix='zela-release-') as temp:
        build = Path(temp)
        workers = {}
        for arch in ('amd64', 'arm64'):
            target_env = dict(env, GOARCH=arch)
            path = build / f'zela-worker-linux-{arch}'
            run('go', 'build', '-mod=readonly', '-trimpath', '-buildvcs=false',
                '-ldflags=-s -w -buildid=', '-o', str(path), './cmd/worker', env=target_env)
            workers[f'bin/{path.name}'] = (path.read_bytes(), 0o755)
        for arch in ('amd64', 'arm64'):
            files = dict(workers)
            files['install.sh'] = ((ROOT / 'scripts/install.sh').read_bytes(), 0o755)
            files['bin/zela-worker'] = workers[f'bin/zela-worker-linux-{arch}']
            for name, package in (('zela-server', './cmd/zela-server'), ('zela', './cmd/zela')):
                path = build / f'{name}-{arch}'
                run('go', 'build', '-mod=readonly', '-trimpath', '-buildvcs=false',
                    '-ldflags=-s -w -buildid=', '-o', str(path), package, env=dict(env, GOARCH=arch))
                files[f'bin/{name}'] = (path.read_bytes(), 0o755)
            documentation = ['LICENSE', 'README.md', 'CONTRIBUTING.md', 'SECURITY.md']
            documentation += sorted(str(p.relative_to(ROOT)) for p in (ROOT / 'docs').glob('*.md'))
            documentation += sorted(str(p.relative_to(ROOT)) for p in (ROOT / 'deploy').rglob('*') if p.is_file())
            for name in documentation:
                files[name] = ((ROOT / name).read_bytes(), 0o644)
            metadata = dict(version=args.version, revision=revision, go=actual, os='linux', arch=arch)
            files['BUILD.json'] = ((json.dumps(metadata, sort_keys=True, indent=2) + '\n').encode(), 0o644)
            prefix = f'zela-{args.version}-linux-{arch}'
            archive = args.output / f'{prefix}.tar.gz'
            with archive.open('wb') as raw:
                with gzip.GzipFile(filename='', mode='wb', fileobj=raw, mtime=0) as zipped:
                    with tarfile.open(fileobj=zipped, mode='w', format=tarfile.USTAR_FORMAT) as tar:
                        for name, (data, mode) in sorted(files.items()):
                            info = tarfile.TarInfo(f'{prefix}/{name}')
                            info.size, info.mode, info.mtime = len(data), mode, 0
                            tar.addfile(info, io.BytesIO(data))
    checksums = ''.join(f'{hashlib.sha256(p.read_bytes()).hexdigest()}  {p.name}\n'
                        for p in sorted(args.output.glob('*.tar.gz')))
    (args.output / 'SHA256SUMS').write_text(checksums)
    print(checksums, end='')


if __name__ == '__main__':
    main()
