#!/usr/bin/env python3
"""Render a complete manifest into an offline installation tree. Never install it."""
import argparse
import json
from pathlib import Path
import shutil
import sys

from hostlib import Failure, validate_manifest


def render(manifest, output):
    value = validate_manifest(json.loads(manifest.read_text()))
    if output.exists():
        raise Failure('Output already exists; choose a new staging directory')
    source = Path(__file__).resolve().parent
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
