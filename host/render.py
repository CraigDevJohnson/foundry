#!/usr/bin/env python3
"""Render a complete manifest into an offline installation tree. Never install it."""
import argparse
import json
from pathlib import Path
import shutil
import sys

from hostlib import Failure, run, validate_manifest


def verify_source(source, revision):
    checkout = source.parent
    git = ['git', '-C', str(checkout)]
    try:
        root = Path(run(git + ['rev-parse', '--show-toplevel'])).resolve()
        head = run(git + ['rev-parse', 'HEAD'])
        changes = run(git + ['status', '--porcelain=v1', '--untracked-files=all',
                             '--ignore-submodules=none'])
        untracked_inputs = run(git + ['ls-files', '--others', '--',
                                     'host/templates', 'host/hostlib.py',
                                     'host/foundry-host', 'host/render.py'])
    except Failure as exc:
        raise Failure(f'Cannot verify the source Git checkout: {exc}') from exc
    if root != checkout:
        raise Failure('Render from the host directory in the source Git checkout')
    if head != revision:
        raise Failure('setup_revision does not match the source Git HEAD')
    if changes:
        raise Failure('Render requires a clean Git checkout, including untracked files')
    if untracked_inputs:
        raise Failure('Source contains an untracked render input, including ignored files')


def render(manifest, output):
    value = validate_manifest(json.loads(manifest.read_text()))
    if output.exists():
        raise Failure('Output already exists; choose a new staging directory')
    source = Path(__file__).resolve().parent
    verify_source(source, value['setup_revision'])
    output.mkdir(parents=True)
    files = {
        'etc/foundry/host.json': json.dumps(value, indent=2) + '\n',
        'etc/foundry/certbot.ini': '# Hooks are disabled; Foundry controls certificate activation.\n',
        'etc/fstab.foundry': f"UUID={value['mount_uuid']} /foundry ext4 defaults,nofail,x-systemd.device-timeout=30s 0 2\n",
    }
    for target, content in files.items():
        path = output / target
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(content)
        path.chmod(0o600 if target.endswith('host.json') else 0o644)
    for template in (source / 'templates').iterdir():
        path = output / 'etc/systemd/system' / template.name
        path.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(template, path)
        path.chmod(0o644)
    for name, destination, mode in (
        ('hostlib.py', 'usr/local/lib/foundry/hostlib.py', 0o644),
        ('foundry-host', 'usr/local/sbin/foundry-host', 0o755),
    ):
        path = output / destination
        path.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(source / name, path)
        path.chmod(mode)


def main():
    parser = argparse.ArgumentParser(description=__doc__, epilog=(
        'Example: python3 host/render.py --manifest /private/host.json --output /private/rendered'))
    parser.add_argument('--manifest', required=True, type=Path)
    parser.add_argument('--output', required=True, type=Path)
    args = parser.parse_args()
    try:
        render(args.manifest, args.output)
    except (Failure, OSError, ValueError) as exc:
        parser.exit(1, f'{exc}\n')


if __name__ == '__main__':
    main()
