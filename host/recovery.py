#!/usr/bin/env python3
"""Private local recovery sets. No AWS calls, installer, retention or host shutdown."""
import argparse
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import stat
import sys
import tempfile

import hostlib
from hostlib import Failure
from render import verify_source

ROOTS = {
    'data': Path('/foundry/foundrydata'),
    'application': Path('/foundry/foundryvtt'),
    'host': Path('/etc/foundry'),
    'certificates': Path('/etc/letsencrypt'),
    'installed_library': Path('/usr/local/lib/foundry'),
    'installed_wrapper': Path('/usr/local/sbin/foundry-host'),
    'service': Path('/etc/systemd/system/foundry.service'),
    'renew_service': Path('/etc/systemd/system/foundry-cert-renew.service'),
    'renew_timer': Path('/etc/systemd/system/foundry-cert-renew.timer'),
}
DIGEST = re.compile(r'[0-9a-f]{64}')


def require(condition, message):
    if not condition:
        raise Failure(message)


def canonical_json(value):
    return (json.dumps(value, sort_keys=True, separators=(',', ':')) + '\n').encode()


def sha(data):
    return hashlib.sha256(data).hexdigest()


def no_duplicates(pairs):
    value = {}
    for key, item in pairs:
        require(key not in value, 'Duplicate JSON key')
        value[key] = item
    return value


def read_json(path):
    return json.loads(path.read_bytes(), object_pairs_hook=no_duplicates)


def relative(name):
    require(isinstance(name, str) and name and not any(ord(c) < 32 for c in name),
            'Invalid recovery path')
    path = PurePosixPath(name)
    require(not path.is_absolute() and '..' not in path.parts and str(path) == name and name != '.',
            'Noncanonical recovery path')
    return path


def source_path(value):
    require(isinstance(value, str) and Path(value).is_absolute(), 'Inventory paths must be absolute')
    path = Path(value)
    require(path.resolve(strict=True) == path, 'Inventory roots cannot traverse symlinks')
    return path


def destination(path):
    path = Path(path).absolute()
    require(not path.exists() and not path.is_symlink(), 'Destination already exists')
    require(path.parent.resolve(strict=True) == path.parent, 'Destination parent must exist without symlinks')
    parent = path.parent.stat()
    require(parent.st_uid == os.geteuid() and not parent.st_mode & 0o022,
            'Destination parent must be owned by the operator and not writable by others')
    return path


def sync_dir(path):
    fd = os.open(path, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def write_new(path, data):
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, 'wb') as stream:
        stream.write(data)
        stream.flush()
        os.fsync(stream.fileno())


def fingerprint(info):
    return (info.st_dev, info.st_ino, info.st_mode, info.st_size, info.st_mtime_ns, info.st_ctime_ns)


def copy_blob(source, blobs):
    """Stream regular files without following a substituted leaf symlink."""
    before = source.lstat()
    fd = os.open(source, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    temporary = None
    try:
        require(stat.S_ISREG(os.fstat(fd).st_mode)
                and fingerprint(before) == fingerprint(os.fstat(fd)), 'Capture input changed')
        digest = hashlib.sha256()
        with os.fdopen(fd, 'rb', closefd=False) as stream:
            with tempfile.NamedTemporaryFile(dir=blobs, prefix='.partial-', delete=False) as target:
                temporary = Path(target.name)
                while True:
                    block = stream.read(1024 * 1024)
                    if not block:
                        break
                    digest.update(block)
                    target.write(block)
                target.flush()
                os.fsync(target.fileno())
        require(fingerprint(before) == fingerprint(os.fstat(fd))
                == fingerprint(source.lstat()), 'Capture input changed while reading')
        name = digest.hexdigest()
        final = blobs / name
        if final.exists():
            require(hostlib.digest(final) == name, 'Existing recovery blob is corrupt')
            temporary.unlink()
        else:
            temporary.rename(final)
        temporary = None
        return name, before
    finally:
        os.close(fd)
        if temporary is not None:
            temporary.unlink(missing_ok=True)


def archive_nevra(path):
    return hostlib.run(['/usr/bin/rpm', '-qp', '--qf', '%{NEVRA}', str(path)])


def inventory_inputs(inventory, manifest):
    require(isinstance(inventory, dict) and set(inventory) == {
        'schema', 'external_assets', 'node_archive', 'foundry_archive', 'setup_checkout', 'recovery_ami',
        'rpm_archives'},
        'Incomplete recovery inventory')
    require(type(inventory['schema']) is int and inventory['schema'] == 1, 'Unsupported inventory')
    ami = inventory['recovery_ami']
    require(isinstance(ami, dict) and set(ami) == {'region', 'image_id'}
            and ami['region'] == 'us-east-2'
            and isinstance(ami['image_id'], str) and re.fullmatch(r'ami-[0-9a-f]{17}', ami['image_id']),
            'Declare the retained Ohio AMI; availability still requires live recovery proof')
    assets = inventory['external_assets']
    require(isinstance(assets, list), 'Explicitly inventory external assets, including an empty list')
    roots = dict(ROOTS)
    for index, path in enumerate(assets):
        roots[f'external-{index}'] = source_path(path)
    for key in ('node_archive', 'foundry_archive'):
        roots[key] = source_path(inventory[key])
        require(roots[key].is_file() and hostlib.digest(roots[key]) == manifest[key + '_sha256'],
                'Retained distribution archive differs from its pin')
    packages = inventory['rpm_archives']
    require(isinstance(packages, dict) and set(packages) == set(manifest['rpms']),
            'Retain every pinned prerequisite RPM archive')
    for name, package in packages.items():
        require(isinstance(package, dict) and set(package) == {'path', 'sha256'}, 'Invalid RPM inventory')
        path = source_path(package['path'])
        require(path.is_file() and isinstance(package['sha256'], str) and DIGEST.fullmatch(package['sha256'])
                and hostlib.digest(path) == package['sha256'], 'Retained RPM archive differs from its pin')
        require(archive_nevra(path) == manifest['rpms'][name], 'Retained RPM version differs from host pin')
        roots['rpm-' + name] = path
    for path in roots.values():
        source_path(str(path))
    paths = list(roots.values())
    for index, path in enumerate(paths):
        require(all(path != other and path not in other.parents and other not in path.parents
                    for other in paths[index + 1:]), 'Overlapping capture roots')
    checkout = source_path(inventory['setup_checkout'])
    verify_source(checkout / 'host', manifest['setup_revision'])
    return roots, checkout


def independent_storage(output, data):
    require(os.stat(output.parent).st_dev != os.stat(data).st_dev,
            'Recovery output must be on a filesystem independent of live data')


def capture(inventory, output, host, restart=False):
    """Caller explicitly authorizes interruption; all host actions share one lock."""
    output = destination(output)
    manifest = hostlib.validate_manifest(host.manifest)
    roots, checkout = inventory_inputs(inventory, manifest)
    require(all(output != path and path not in output.parents and output not in path.parents
                for path in [*roots.values(), checkout]), 'Recovery destination overlaps an input')
    independent_storage(output, ROOTS['data'])
    with hostlib.locked():
        host.inputs()
        host.stop()
        host.mount()
        host.stopped()
        captured_at = datetime.now(timezone.utc).isoformat()
        output.mkdir(mode=0o700)
        blobs = output / 'blobs'
        blobs.mkdir(mode=0o700)
        entries = []
        observed = []
        with tempfile.TemporaryDirectory(prefix='foundry-recovery-') as scratch:
            bundle = Path(scratch) / 'setup.bundle'
            hostlib.run(['git', '-C', str(checkout), 'bundle', 'create', str(bundle), 'HEAD'], timeout=120)
            hostlib.run(['git', '-C', str(checkout), 'bundle', 'verify', str(bundle)], timeout=120)
            roots['setup_bundle'] = bundle

            def walk(path, name):
                relative(name)
                before = path.lstat()
                entry = {'path': name, 'mode': stat.S_IMODE(before.st_mode),
                         'uid': before.st_uid, 'gid': before.st_gid, 'mtime_ns': before.st_mtime_ns}
                if stat.S_ISDIR(before.st_mode):
                    entry['kind'] = 'directory'
                    entries.append(entry)
                    for child in sorted(path.iterdir()):
                        walk(child, name + '/' + child.name)
                elif stat.S_ISREG(before.st_mode):
                    digest, copied = copy_blob(path, blobs)
                    require(fingerprint(copied) == fingerprint(before), 'Capture input changed')
                    entry.update(kind='file', sha256=digest, size=before.st_size)
                    entries.append(entry)
                elif stat.S_ISLNK(before.st_mode):
                    target = path.resolve(strict=True)
                    matches = [(key, root) for key, root in roots.items()
                               if target == root or root in target.parents]
                    require(len(matches) == 1, 'Symlink target is outside declared recovery coverage')
                    key, root = matches[0]
                    entry.update(kind='symlink', target=str(PurePosixPath(key) / target.relative_to(root)))
                    entries.append(entry)
                else:
                    raise Failure('Special files cannot enter a recovery set')
                require(fingerprint(before) == fingerprint(path.lstat()), 'Capture tree changed')
                observed.append((path, before))

            for key, path in sorted(roots.items()):
                walk(path, key)
            host.mount()
            host.stopped()
            require(all(fingerprint(before) == fingerprint(path.lstat()) for path, before in observed),
                    'Capture inputs changed before completion')
            verify_source(checkout / 'host', manifest['setup_revision'])
            value = {'schema': 1, 'captured_at': captured_at,
                     'completed_at': datetime.now(timezone.utc).isoformat(),
                     'host_manifest': manifest, 'recovery_ami': inventory['recovery_ami'],
                     'rpm_sha256': {name: package['sha256'] for name, package in inventory['rpm_archives'].items()},
                     'roots': {key: str(path) for key, path in roots.items() if key != 'setup_bundle'},
                     'entries': entries}
            raw = canonical_json(value)
            checksum = sha(raw)
            write_new(output / 'manifest.json', raw)
            verify(output, checksum, completed=False)
            sync_dir(blobs)
            sync_dir(output)
            write_new(output / 'COMPLETE', (checksum + '\n').encode())
            sync_dir(output)
            sync_dir(output.parent)
        if restart:
            host.start()
    return checksum


def verify(directory, expected, completed=True):
    require(isinstance(expected, str) and DIGEST.fullmatch(expected), 'Expected manifest checksum is required')
    directory = source_path(str(Path(directory).absolute()))
    require(directory.is_dir(), 'Recovery set must be a directory')
    allowed = {'manifest.json', 'blobs', 'COMPLETE'} if completed else {'manifest.json', 'blobs'}
    require({p.name for p in directory.iterdir()} == allowed, 'Partial or unexpected recovery-set contents')
    for name in allowed:
        require(not (directory / name).is_symlink(), 'Recovery-set components cannot be symlinks')
    if completed:
        require((directory / 'COMPLETE').read_bytes() == (expected + '\n').encode(), 'Incomplete recovery set')
    raw = (directory / 'manifest.json').read_bytes()
    require(sha(raw) == expected, 'Recovery manifest checksum differs')
    value = json.loads(raw, object_pairs_hook=no_duplicates)
    require(isinstance(value, dict) and set(value) == {
        'schema', 'captured_at', 'completed_at', 'host_manifest', 'recovery_ami', 'rpm_sha256', 'roots', 'entries'}
        and type(value['schema']) is int and value['schema'] == 1, 'Unsupported recovery manifest')
    started = datetime.fromisoformat(value['captured_at'])
    finished = datetime.fromisoformat(value['completed_at'])
    require(started.tzinfo is not None and finished.tzinfo is not None and finished >= started,
            'Invalid recovery timestamps')
    manifest = hostlib.validate_manifest(value['host_manifest'])
    require(isinstance(value['roots'], dict) and set(ROOTS) | {'node_archive', 'foundry_archive'}
            <= set(value['roots']), 'Missing capture roots')
    require(all(isinstance(path, str) and Path(path).is_absolute() for path in value['roots'].values()),
            'Invalid source inventory')
    entries = value['entries']
    require(isinstance(entries, list) and entries, 'Missing captured entries')
    indexed = {}
    hashes = set()
    for entry in entries:
        require(isinstance(entry, dict), 'Invalid recovery entry')
        path = relative(entry.get('path'))
        require(str(path) not in indexed, 'Duplicate recovery path')
        require(path.parts[0] in set(value['roots']) | {'setup_bundle'}, 'Undeclared capture root')
        kind = entry.get('kind')
        extra = {'file': {'sha256', 'size'}, 'directory': set(), 'symlink': {'target'}}
        require(kind in extra and set(entry) == {'path', 'kind', 'mode', 'uid', 'gid', 'mtime_ns'} | extra[kind],
                'Invalid recovery entry fields')
        require(all(type(entry[k]) is int and entry[k] >= 0 for k in ('mode', 'uid', 'gid'))
                and entry['mode'] <= 0o7777 and type(entry['mtime_ns']) is int,
                'Invalid recorded ownership, mode or timestamp')
        if kind == 'file':
            digest = entry['sha256']
            require(isinstance(digest, str) and DIGEST.fullmatch(digest)
                    and type(entry['size']) is int and entry['size'] >= 0, 'Invalid blob metadata')
            blob = directory / 'blobs' / digest
            require(not blob.is_symlink() and blob.is_file() and blob.stat().st_size == entry['size']
                    and hostlib.digest(blob) == digest, 'Missing or corrupt recovery blob')
            hashes.add(digest)
        elif kind == 'symlink':
            relative(entry['target'])
        indexed[str(path)] = entry
    require({p.name for p in (directory / 'blobs').iterdir()} == hashes, 'Unexpected recovery blobs')
    for name, entry in indexed.items():
        parent = str(PurePosixPath(name).parent)
        require(parent == '.' or indexed.get(parent, {}).get('kind') == 'directory',
                'Missing parent or write through a symlink')
        if entry['kind'] == 'symlink':
            target = PurePosixPath(entry['target'])
            link = PurePosixPath(name)
            require(indexed.get(entry['target'], {}).get('kind') in {'file', 'directory'}
                    and target != link and target not in link.parents and link not in target.parents,
                    'Dangling, recursive or chained recovery symlink')
    for name in set(value['roots']) | {'setup_bundle', 'data/Data', 'data/Config', 'data/Backups',
                                      'data/Data/worlds', 'host/host.json'}:
        require(name in indexed, 'Required recovery coverage is absent')
    for name in ('data', 'application', 'host', 'certificates', 'installed_library',
                 'data/Data', 'data/Config', 'data/Backups', 'data/Data/worlds'):
        require(indexed[name]['kind'] == 'directory', 'Required directory is not preserved')
    for name in ('installed_wrapper', 'service', 'renew_service', 'renew_timer'):
        require(indexed[name]['kind'] == 'file', 'Required installed file is not preserved')
    for world in manifest['worlds']:
        require(indexed.get('data/Data/worlds/' + world, {}).get('kind') == 'directory',
                'Declared world is missing')
    for name in ('node_archive', 'foundry_archive'):
        require(indexed[name].get('sha256') == manifest[name + '_sha256'], 'Distribution pin differs')
    require(isinstance(value['rpm_sha256'], dict) and set(value['rpm_sha256']) == set(manifest['rpms']),
            'Missing pinned RPM coverage')
    for name, digest in value['rpm_sha256'].items():
        require(isinstance(digest, str) and DIGEST.fullmatch(digest)
                and indexed.get('rpm-' + name, {}).get('kind') == 'file'
                and indexed.get('rpm-' + name, {}).get('sha256') == digest, 'Missing or mismatched RPM archive')
    require(indexed.get('application/main.js', {}).get('sha256') == manifest['foundry_main_sha256'],
            'Pinned installed Foundry entry point is missing or changed')
    package = indexed.get('application/package.json', {})
    require(package.get('kind') == 'file', 'Installed Foundry package metadata is missing')
    package_value = read_json(directory / 'blobs' / package['sha256'])
    require(isinstance(package_value, dict) and package_value.get('version') == manifest['foundry_version'],
            'Installed Foundry package metadata differs')
    config = indexed['host/host.json']
    require(config['kind'] == 'file'
            and read_json(directory / 'blobs' / config['sha256']) == manifest, 'Captured host manifest differs')
    require(indexed['setup_bundle']['kind'] == 'file', 'Missing Git reconstruction bundle')
    return value


def materialize(directory, expected, output):
    value = verify(directory, expected)
    directory = Path(directory).absolute()
    output = destination(output)
    require(directory not in output.parents and output not in directory.parents, 'Restore overlaps recovery set')
    require(all(Path(path) != output and Path(path) not in output.parents and output not in Path(path).parents
                for path in value['roots'].values()), 'Restore overlaps a captured source')
    output.mkdir(mode=0o700)
    # Directories/files first; never traverse a link when creating a child.
    for entry in sorted(value['entries'], key=lambda e: (len(PurePosixPath(e['path']).parts), e['path'])):
        target = output / entry['path']
        if entry['kind'] == 'directory':
            target.mkdir(mode=0o700)
        elif entry['kind'] == 'file':
            source = directory / 'blobs' / entry['sha256']
            fd = os.open(source, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
            with os.fdopen(fd, 'rb') as stream:
                require(stat.S_ISREG(os.fstat(fd).st_mode), 'Recovery blob is not a regular file')
                with target.open('xb') as destination_stream:
                    os.chmod(target, 0o600)
                    digest = hashlib.sha256()
                    while True:
                        block = stream.read(1024 * 1024)
                        if not block:
                            break
                        destination_stream.write(block)
                        digest.update(block)
                    destination_stream.flush()
                    os.fsync(destination_stream.fileno())
            require(digest.hexdigest() == entry['sha256'], 'Recovery blob changed during materialization')
    for entry in value['entries']:
        if entry['kind'] == 'symlink':
            target = output / entry['path']
            target.symlink_to(os.path.relpath(output / entry['target'], target.parent))
    for parent, _, _ in os.walk(output, topdown=False, followlinks=False):
        sync_dir(Path(parent))
    write_new(output / 'RECOVERY-MATERIALIZED.json', canonical_json({
        'schema': 1, 'manifest_sha256': expected, 'installed': False,
        'ownership_and_modes_applied': False, 'ohio_restore_verified': False}))
    sync_dir(output)
    sync_dir(output.parent)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest='action', required=True)
    capture_parser = sub.add_parser('capture', help='Explicit local capture; never scheduled automatically')
    capture_parser.add_argument('--inventory', type=Path, required=True)
    capture_parser.add_argument('--output', type=Path, required=True)
    capture_parser.add_argument('--confirm-no-active-session', action='store_true', required=True)
    capture_parser.add_argument('--restart', action='store_true')
    for action in ('verify', 'materialize'):
        command = sub.add_parser(action)
        command.add_argument('--set', dest='directory', type=Path, required=True)
        command.add_argument('--expected-sha256', required=True)
        if action == 'materialize':
            command.add_argument('--output', type=Path, required=True)
    args = parser.parse_args(argv)
    try:
        if args.action == 'capture':
            require(os.geteuid() == 0, 'Capture requires the authorized root-operated host command')
            for path in (hostlib.CONFIG, args.inventory):
                info = path.lstat()
                require(stat.S_ISREG(info.st_mode) and info.st_uid == 0 and not info.st_mode & 0o022,
                        'Capture manifests must be root-owned and not writable by others')
            checksum = capture(read_json(args.inventory), args.output, hostlib.Host(read_json(hostlib.CONFIG)),
                               restart=args.restart)
            print(json.dumps({'outcome': 'local-capture-verified', 'manifest_sha256': checksum,
                              'ohio_protection': 'unverified'}))
        elif args.action == 'verify':
            verify(args.directory, args.expected_sha256)
            print(json.dumps({'outcome': 'local-integrity-verified', 'ohio_protection': 'unverified'}))
        else:
            materialize(args.directory, args.expected_sha256, args.output)
            print(json.dumps({'outcome': 'materialized-offline', 'installed': False}))
        return 0
    except (Failure, OSError, ValueError, KeyError, TypeError, RuntimeError):
        # Config, filenames and underlying command output can contain private information.
        print('Recovery operation failed; preserve partial evidence and reconcile before retrying.', file=sys.stderr)
        return 1


if __name__ == '__main__':
    sys.exit(main())
