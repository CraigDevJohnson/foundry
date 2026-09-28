"""Recovery tests use synthetic trees and a stopped-writer fixture; never a live host."""
from contextlib import ExitStack, redirect_stderr
import hashlib
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

from test_host import manifest

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / 'host'))
import hostlib
import recovery


class Writer:
    def __init__(self, value, lock):
        self.manifest = value
        self.lock = lock
        self.calls = []
        self.fail_stop = False
        self.fail_start = False
        self.fail_mount = False
        self.fail_final_stop = False
        self.active = True

    def locked_call(self, name):
        self.calls.append(name)
        try:
            with hostlib.locked(self.lock):
                raise AssertionError('Lifecycle lock was not held')
        except hostlib.Busy:
            pass

    def inputs(self):
        self.locked_call('inputs')

    def stop(self):
        self.locked_call('stop')
        if self.fail_stop:
            raise hostlib.Failure('uncertain stop')
        self.active = False

    def mount(self):
        self.locked_call('mount')
        if self.fail_mount:
            raise hostlib.Failure('mount vanished')

    def stopped(self):
        self.locked_call('stopped')
        if self.active or self.fail_final_stop and self.calls.count('stopped') > 1:
            raise hostlib.Failure('writer state uncertain')

    def start(self):
        self.locked_call('start')
        if self.fail_start:
            raise hostlib.Failure('start failed')
        self.active = True


class RecoveryTest(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name).resolve()
        self.sources = self.root / 'source'
        self.sources.mkdir()
        self.roots = {}
        for name in recovery.ROOTS:
            path = self.sources / name
            self.roots[name] = path
            if name in {'data', 'application', 'host', 'certificates', 'installed_library'}:
                path.mkdir()
                (path / 'fixture').write_bytes(b'synthetic reconstruction fixture')
            else:
                path.write_bytes(b'synthetic installed file')
        for name in ('Data/worlds/alpha', 'Data/worlds/beta', 'Data/assets', 'Config', 'Backups', 'Logs', 'Cache'):
            path = self.roots['data'] / name
            path.mkdir(parents=True, exist_ok=True)
            (path / 'fixture').write_bytes(('synthetic ' + name).encode())
        certs = self.roots['certificates']
        (certs / 'archive').mkdir()
        (certs / 'archive/cert.pem').write_text('synthetic certificate, not a real key')
        (certs / 'live.pem').symlink_to('archive/cert.pem')
        self.checkout = self.root / 'checkout'
        (self.checkout / 'host').mkdir(parents=True)
        (self.checkout / 'host/fixture').write_text('reconstruction source fixture')
        self.git('init', '--quiet')
        self.git('add', '.')
        self.git('-c', 'user.name=Recovery fixture', '-c', 'user.email=test@example.invalid',
                 '-c', 'commit.gpgsign=false', 'commit', '--quiet', '-m', 'Fixture')
        self.value = manifest()
        self.value['setup_revision'] = self.git('rev-parse', 'HEAD').strip()
        (self.roots['application'] / 'main.js').write_bytes(b'synthetic licensed entry point')
        self.value['foundry_main_sha256'] = recovery.sha((self.roots['application'] / 'main.js').read_bytes())
        (self.roots['application'] / 'package.json').write_text(json.dumps({'version': self.value['foundry_version']}))
        self.inventory = {'schema': 1, 'external_assets': [], 'rpm_archives': {},
                          'setup_checkout': str(self.checkout),
                          'recovery_ami': {'region': 'us-east-2', 'image_id': 'ami-0123456789abcdef0'}}
        for name in ('node_archive', 'foundry_archive'):
            path = self.root / (name + '.fixture')
            path.write_bytes(('synthetic ' + name).encode())
            self.inventory[name] = str(path)
            self.value[name + '_sha256'] = hashlib.sha256(path.read_bytes()).hexdigest()
        for name in self.value['rpms']:
            path = self.root / (name + '.rpm')
            path.write_bytes(('synthetic RPM ' + name).encode())
            self.inventory['rpm_archives'][name] = {'path': str(path), 'sha256': recovery.sha(path.read_bytes())}
        (self.roots['host'] / 'host.json').write_text(json.dumps(self.value))
        self.lock = self.root / 'lifecycle.lock'
        self.writer = Writer(self.value, self.lock)
        self.output = self.root / 'recovery-set'
        stack = ExitStack()
        self.addCleanup(stack.close)
        stack.enter_context(patch.object(recovery, 'ROOTS', self.roots))
        stack.enter_context(patch.object(recovery, 'archive_nevra',
                                       lambda path: self.value['rpms'][path.stem]))
        # All tests are on temporary storage; actual device separation has its own negative test.
        stack.enter_context(patch.object(recovery, 'independent_storage'))
        real_lock = hostlib.locked
        stack.enter_context(patch.object(hostlib, 'locked', lambda path=None: real_lock(path or self.lock)))

    def git(self, *args):
        return subprocess.run(['git', '-C', str(self.checkout), *args], check=True,
                              capture_output=True, text=True).stdout

    def capture(self, restart=False):
        return recovery.capture(self.inventory, self.output, self.writer, restart=restart)

    def rewrite(self, change):
        value = json.loads((self.output / 'manifest.json').read_text())
        change(value)
        raw = recovery.canonical_json(value)
        (self.output / 'manifest.json').write_bytes(raw)
        digest = recovery.sha(raw)
        (self.output / 'COMPLETE').write_text(digest + '\n')
        return digest

    def test_roundtrip_preserves_both_worlds_dependencies_and_safe_links(self):
        checksum = self.capture(restart=True)
        value = recovery.verify(self.output, checksum)
        self.assertEqual(value['host_manifest'], self.value)
        self.assertEqual(self.writer.calls, ['inputs', 'stop', 'mount', 'stopped', 'mount', 'stopped', 'start'])
        restored = self.root / 'isolated'
        recovery.materialize(self.output, checksum, restored)
        for name in ('Data/worlds/alpha', 'Data/worlds/beta', 'Data/assets', 'Config', 'Backups', 'Logs', 'Cache'):
            self.assertEqual((restored / 'data' / name / 'fixture').read_bytes(),
                             (self.roots['data'] / name / 'fixture').read_bytes())
        self.assertEqual((restored / 'certificates/live.pem').resolve(), restored / 'certificates/archive/cert.pem')
        self.assertEqual((restored / 'certificates/live.pem').read_bytes(),
                         (self.roots['certificates'] / 'archive/cert.pem').read_bytes())
        for path in restored.rglob('*'):
            if not path.is_symlink():
                self.assertEqual(path.stat().st_mode & 0o077, 0)
        evidence = json.loads((restored / 'RECOVERY-MATERIALIZED.json').read_text())
        self.assertFalse(evidence['installed'])
        self.assertFalse(evidence['ohio_restore_verified'])
        subprocess.run(['git', 'clone', '--quiet', str(restored / 'setup_bundle'), str(self.root / 'clone')],
                       check=True, capture_output=True)
        self.assertEqual(subprocess.check_output(['git', '-C', str(self.root / 'clone'), 'rev-parse', 'HEAD'],
                                                text=True).strip(), self.value['setup_revision'])

    def test_external_assets_are_explicit_and_captured(self):
        assets = self.root / 'external-assets'
        assets.mkdir()
        (assets / 'map').write_bytes(b'synthetic external map')
        self.inventory['external_assets'] = [str(assets)]
        (self.roots['data'] / 'Data/map').symlink_to(assets / 'map')
        checksum = self.capture()
        restored = self.root / 'isolated'
        recovery.materialize(self.output, checksum, restored)
        self.assertEqual((restored / 'data/Data/map').read_bytes(), b'synthetic external map')
        self.assertEqual((restored / 'data/Data/map').resolve(), restored / 'external-0/map')

    def test_missing_or_unpinned_artifact_fails_before_stopping(self):
        Path(self.inventory['node_archive']).write_bytes(b'wrong archive')
        with self.assertRaises(hostlib.Failure):
            self.capture()
        self.assertEqual(self.writer.calls, [])
        self.assertFalse(self.output.exists())

    def test_missing_pinned_rpm_fails_before_stop(self):
        del self.inventory['rpm_archives']['python3']
        with self.assertRaises(hostlib.Failure):
            self.capture()
        self.assertEqual(self.writer.calls, [])

    def test_wrong_rpm_metadata_fails_before_stop(self):
        with patch.object(recovery, 'archive_nevra', return_value='python3-wrong'):
            with self.assertRaises(hostlib.Failure):
                self.capture()
        self.assertEqual(self.writer.calls, [])

    def test_unreviewed_source_cannot_be_captured(self):
        (self.checkout / 'host/fixture').write_text('unreviewed')
        with self.assertRaises(hostlib.Failure):
            self.capture()
        self.assertEqual(self.writer.calls, [])

    def test_stop_failure_never_reads_data_or_restarts(self):
        self.writer.fail_stop = True
        with self.assertRaises(hostlib.Failure):
            self.capture(restart=True)
        self.assertEqual(self.writer.calls, ['inputs', 'stop'])
        self.assertFalse(self.output.exists())

    def test_lock_contention_never_stops_host(self):
        with hostlib.locked(self.lock):
            with self.assertRaises(hostlib.Busy):
                self.capture()
        self.assertEqual(self.writer.calls, [])

    def test_missing_mount_stops_before_capture(self):
        self.writer.fail_mount = True
        with self.assertRaises(hostlib.Failure):
            self.capture()
        self.assertFalse(self.output.exists())

    def test_restart_failure_keeps_verified_capture_and_no_ec2_action(self):
        self.writer.fail_start = True
        with self.assertRaises(hostlib.Failure):
            self.capture(restart=True)
        checksum = (self.output / 'COMPLETE').read_text().strip()
        recovery.verify(self.output, checksum)
        self.assertFalse(self.writer.active)

    def test_failed_final_stopped_check_cannot_complete(self):
        self.writer.fail_final_stop = True
        with self.assertRaises(hostlib.Failure):
            self.capture()
        self.assertFalse((self.output / 'COMPLETE').exists())

    def test_missing_world_cannot_complete(self):
        shutil.rmtree(self.roots['data'] / 'Data/worlds/beta')
        with self.assertRaises(hostlib.Failure):
            self.capture()
        self.assertFalse((self.output / 'COMPLETE').exists())

    def test_missing_installed_distribution_cannot_complete(self):
        (self.roots['application'] / 'main.js').unlink()
        with self.assertRaises(hostlib.Failure):
            self.capture()
        self.assertFalse((self.output / 'COMPLETE').exists())

    def test_uninventoried_symlink_cannot_complete(self):
        (self.roots['data'] / 'Data/outside').symlink_to(self.root / 'node_archive.fixture')
        # Distribution archives are declared, so use an unrelated file instead.
        (self.root / 'unlisted').write_bytes(b'not in coverage')
        (self.roots['data'] / 'Data/outside').unlink()
        (self.roots['data'] / 'Data/outside').symlink_to(self.root / 'unlisted')
        with self.assertRaises(hostlib.Failure):
            self.capture()
        self.assertFalse((self.output / 'COMPLETE').exists())

    def test_special_file_cannot_complete(self):
        os.mkfifo(self.roots['data'] / 'Data/pipe')
        with self.assertRaises(hostlib.Failure):
            self.capture()
        self.assertFalse((self.output / 'COMPLETE').exists())

    def test_partial_write_never_becomes_complete(self):
        original = recovery.copy_blob
        count = 0

        def fail_second(*args):
            nonlocal count
            count += 1
            if count == 2:
                raise OSError('simulated disk full')
            return original(*args)
        with patch.object(recovery, 'copy_blob', fail_second):
            with self.assertRaises(OSError):
                self.capture()
        self.assertFalse((self.output / 'COMPLETE').exists())
        with self.assertRaises(hostlib.Failure):
            recovery.verify(self.output, 'a' * 64)

    def test_change_during_capture_is_rejected(self):
        original = recovery.copy_blob
        changed = False

        def change_after_copy(source, blobs):
            nonlocal changed
            result = original(source, blobs)
            if not changed:
                changed = True
                source.write_bytes(b'changed after it was read')
            return result
        with patch.object(recovery, 'copy_blob', change_after_copy):
            with self.assertRaises(hostlib.Failure):
                self.capture()
        self.assertFalse((self.output / 'COMPLETE').exists())

    def test_corruption_or_missing_blob_blocks_materialization(self):
        checksum = self.capture()
        blob = next((self.output / 'blobs').iterdir())
        blob.write_bytes(b'corrupt')
        with self.assertRaises(hostlib.Failure):
            recovery.materialize(self.output, checksum, self.root / 'isolated')
        self.assertFalse((self.root / 'isolated').exists())

    def test_manifest_tamper_cannot_use_original_checksum(self):
        checksum = self.capture()
        self.rewrite(lambda v: v.update(captured_at='changed'))
        with self.assertRaises(hostlib.Failure):
            recovery.verify(self.output, checksum)

    def test_traversal_duplicate_and_unsafe_links_rejected_even_with_new_digest(self):
        checksum = self.capture()
        original = (self.output / 'manifest.json').read_bytes()
        mutations = [
            lambda v: v['entries'][0].update(path='../escaped'),
            lambda v: v['entries'][0].update(path='/absolute'),
            lambda v: v['entries'].append(v['entries'][0]),
            lambda v: next(e for e in v['entries'] if e['kind'] == 'symlink').update(target='../outside'),
            lambda v: next(e for e in v['entries'] if e['kind'] == 'symlink').update(target='certificates'),
            lambda v: v['entries'].append({'path': 'certificates/live.pem/child', 'kind': 'directory',
                                          'mode': 0o700, 'uid': 1, 'gid': 1}),
        ]
        for mutate in mutations:
            (self.output / 'manifest.json').write_bytes(original)
            bad = self.rewrite(mutate)
            with self.subTest(mutate=mutate), self.assertRaises(hostlib.Failure):
                recovery.materialize(self.output, bad, self.root / 'isolated')
            self.assertFalse((self.root / 'isolated').exists())
        self.assertFalse((self.root / 'escaped').exists())

    def test_output_never_overwrites_existing_directory(self):
        self.output.mkdir()
        (self.output / 'valuable').write_bytes(b'preserve')
        with self.assertRaises(hostlib.Failure):
            self.capture()
        self.assertEqual((self.output / 'valuable').read_bytes(), b'preserve')

    def test_materialization_cannot_target_captured_source(self):
        checksum = self.capture()
        with self.assertRaises(hostlib.Failure):
            recovery.materialize(self.output, checksum, self.roots['data'] / 'new-directory')

    def test_symlinked_destination_parent_is_rejected(self):
        link = self.root / 'link'
        link.symlink_to(self.root / 'source', target_is_directory=True)
        with self.assertRaises(hostlib.Failure):
            recovery.destination(link / 'new')

    def test_writable_shared_destination_parent_is_rejected(self):
        shared = self.root / 'shared'
        shared.mkdir()
        shared.chmod(0o777)
        with self.assertRaises(hostlib.Failure):
            recovery.destination(shared / 'new')

    def test_blob_symlink_is_not_followed(self):
        checksum = self.capture()
        blob = next((self.output / 'blobs').iterdir())
        saved = self.root / 'saved'
        blob.rename(saved)
        blob.symlink_to(saved)
        with self.assertRaises(hostlib.Failure):
            recovery.verify(self.output, checksum)

    def test_help_does_not_touch_host(self):
        result = subprocess.run([sys.executable, str(ROOT / 'host/recovery.py'), 'capture', '--help'],
                                capture_output=True, text=True)
        self.assertEqual(result.returncode, 0)
        self.assertIn('--confirm-no-active-session', result.stdout)

    def test_cli_errors_do_not_echo_private_paths_or_contents(self):
        private = self.root / 'private-world-name'
        private.mkdir()
        stderr = io.StringIO()
        with redirect_stderr(stderr):
            status = recovery.main(['verify', '--set', str(private), '--expected-sha256', 'a' * 64])
        self.assertEqual(status, 1)
        self.assertNotIn('private-world-name', stderr.getvalue())


class StorageBoundaryTest(unittest.TestCase):
    def test_same_filesystem_cannot_be_capture_destination(self):
        with tempfile.TemporaryDirectory() as directory:
            with self.assertRaises(hostlib.Failure):
                recovery.independent_storage(Path(directory) / 'output', Path(directory))


if __name__ == '__main__':
    unittest.main()
