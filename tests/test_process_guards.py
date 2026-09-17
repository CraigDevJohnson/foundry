"""Offline process census regressions for Foundry writers without listeners."""
from contextlib import ExitStack
import os
from pathlib import Path
import shutil
import tempfile
import unittest
from unittest.mock import patch

from test_host import FakeMachine, hostlib, manifest


def process_stat(pid, state='S', flags=0, start=42):
    fields = [state] + ['0'] * 19
    fields[6] = str(flags)
    fields[19] = str(start)
    return f'{pid} (process name) ' + ' '.join(fields) + '\n'


class ProcessGuardTests(unittest.TestCase):
    def setUp(self):
        self.stack = ExitStack()
        self.addCleanup(self.stack.close)
        self.directory = Path(self.stack.enter_context(tempfile.TemporaryDirectory()))
        self.machine = FakeMachine(self.directory)
        self.stack.enter_context(patch.object(hostlib, 'CGROUP', self.machine.cgroup))
        self.stack.enter_context(patch.object(hostlib, 'PROC', self.machine.proc))
        self.stack.enter_context(patch.object(hostlib, 'run', side_effect=self.machine.run))
        self.host = hostlib.Host(manifest())

    def process(self, args, cwd='/tmp', exe='/usr/bin/node', pid=9000, state='S', flags=0):
        process = self.machine.proc / str(pid)
        process.mkdir(exist_ok=True)
        (process / 'stat').write_text(process_stat(pid, state, flags))
        (process / 'cmdline').write_bytes(b'\0'.join(os.fsencode(arg) for arg in args) + b'\0')
        (process / 'cwd').symlink_to(cwd)
        (process / 'exe').symlink_to(exe)
        return process

    def test_legacy_process_forms_block_stopped_and_start_without_a_listener(self):
        cases = (
            (['node', '/foundry/foundryvtt/main.js'], '/tmp'),
            (['node', 'main.js'], '/foundry/foundryvtt'),
            (['node', './resources/app/main.js'], '/foundry/foundryvtt'),
            (['node', '.'], '/foundry/foundryvtt'),
            (['node', '/tmp/main.js', '--dataPath=/foundry/foundrydata'], '/tmp'),
            (['node', '/tmp/main.js', '--dataPath', '/foundry/foundrydata'], '/tmp'),
            (['node', '/tmp/main.js', '--dataPath=../foundrydata'], '/foundry/foundryvtt'),
            (['node', '/home/ec2-user/foundryvtt/resources/app/main.js'], '/tmp'),
            (['python3', 'writer.py'], '/foundry/foundrydata/Data/worlds/alpha'),
        )
        for args, cwd in cases:
            with self.subTest(args=args, cwd=cwd):
                process = self.process(args, cwd)
                try:
                    with self.assertRaisesRegex(hostlib.Failure, 'Shutdown is not verified'):
                        self.host.stopped()
                    with self.assertRaises(hostlib.Failure):
                        self.host.start()
                    self.assertEqual(self.machine.mutations(), [])
                finally:
                    shutil.rmtree(process)

    def test_native_foundry_executable_blocks_stopped(self):
        self.process(['renamed'], exe='/opt/foundryvtt/foundryvtt')
        with self.assertRaisesRegex(hostlib.Failure, 'Shutdown is not verified'):
            self.host.stopped()

    def test_deleted_process_paths_do_not_hide_foundry_writers(self):
        cases = (
            (['/usr/bin/node', 'main.js'], '/foundry/foundryvtt', '/usr/bin/node (deleted)'),
            (['/usr/bin/node', 'main.js'], '/foundry/foundryvtt (deleted)', '/usr/bin/node'),
            (['renamed'], '/tmp', '/opt/foundry (deleted)'),
            (['/usr/bin/python3', 'writer.py'], '/foundry/foundrydata (deleted)', '/usr/bin/python3'),
        )
        for args, cwd, executable in cases:
            with self.subTest(cwd=cwd, executable=executable):
                process = self.process(args, cwd, executable)
                try:
                    with self.assertRaisesRegex(hostlib.Failure, 'Shutdown is not verified'):
                        self.host.stopped()
                    with self.assertRaises(hostlib.Failure):
                        self.host.start()
                    self.assertEqual(self.machine.mutations(), [])
                finally:
                    shutil.rmtree(process)

    def test_unrelated_deleted_executable_is_allowed(self):
        self.process(['/usr/bin/node', '/srv/app/main.js'], exe='/usr/bin/node (deleted)')
        self.assertIsNone(self.host.stopped())

    def test_extra_unlistened_writer_blocks_ready(self):
        self.machine.set_state(True)
        self.process(['node', '/foundry/foundryvtt/main.js'])
        with self.assertRaisesRegex(hostlib.Failure, 'single managed'):
            self.host.ready()

    def test_unrelated_node_and_similar_path_are_allowed(self):
        self.process(['node', '/srv/app/main.js', '--dataPath=/foundry/foundrydata-old'])
        self.assertIsNone(self.host.stopped())
        self.machine.set_state(True)
        self.assertIsNone(self.host.ready())

    def test_operator_shell_and_lifecycle_wrapper_in_application_directory_are_allowed(self):
        self.process(['/bin/bash'], cwd='/foundry/foundryvtt', exe='/bin/bash')
        self.process(['/usr/bin/python3', '/usr/local/sbin/foundry-host', 'start'],
                     cwd='/foundry/foundryvtt', exe='/usr/bin/python3', pid=9001)
        self.assertIsNone(self.host.stopped())

    def test_current_preflight_process_is_excluded(self):
        self.process(['/usr/bin/python3', '/usr/local/sbin/foundry-host', 'preflight'],
                     cwd='/foundry/foundryvtt', exe='/usr/bin/python3', pid=os.getpid())
        with patch.object(self.host, 'inputs'), patch.object(self.host, 'renew'), \
                patch.object(self.host, 'publish_certificate'), \
                patch.object(hostlib, 'CERT_LOCK', self.directory / 'certificate.lock'):
            self.assertIsNone(self.host.preflight())

    def test_unlistened_writer_blocks_preflight_before_certificate_work(self):
        self.process(['node', 'main.js'], '/foundry/foundryvtt')
        with patch.object(self.host, 'inputs'), patch.object(self.host, 'renew') as renew:
            with self.assertRaisesRegex(hostlib.Failure, 'duplicate startup'):
                self.host.preflight()
        renew.assert_not_called()

    def test_preflight_rechecks_for_writer_after_certificate_work(self):
        def launch_writer():
            self.process(['node', 'main.js'], '/foundry/foundryvtt')
        with patch.object(self.host, 'inputs'), patch.object(self.host, 'renew'), \
                patch.object(self.host, 'publish_certificate', side_effect=launch_writer), \
                patch.object(hostlib, 'CERT_LOCK', self.directory / 'certificate.lock'):
            with self.assertRaisesRegex(hostlib.Failure, 'during preflight'):
                self.host.preflight()

    def test_unreadable_live_process_blocks_verification(self):
        process = self.process(['node', '/tmp/unknown.js'])
        original = Path.read_bytes
        def deny_cmdline(path):
            if path == process / 'cmdline':
                raise PermissionError('denied')
            return original(path)
        with patch.object(Path, 'read_bytes', deny_cmdline):
            with self.assertRaisesRegex(hostlib.Failure, 'Cannot inspect live process 9000'):
                self.host.stopped()

    def test_missing_live_process_details_block_verification(self):
        process = self.process(['node', '/tmp/unknown.js'])
        (process / 'cwd').unlink()
        with self.assertRaisesRegex(hostlib.Failure, 'Cannot inspect live process 9000'):
            self.host.stopped()

    def test_vanished_process_is_skipped(self):
        process = self.process(['node', '/tmp/unknown.js'])
        original = Path.read_bytes
        def vanish(path):
            if path == process / 'cmdline':
                shutil.rmtree(process)
                raise FileNotFoundError('process exited')
            return original(path)
        with patch.object(Path, 'read_bytes', vanish):
            self.assertIsNone(self.host.stopped())

    def test_known_kernel_threads_and_zombies_are_skipped(self):
        for pid, state, flags in ((9000, 'S', 0x00200000), (9001, 'Z', 0)):
            process = self.process([], pid=pid, state=state, flags=flags)
            (process / 'exe').unlink()
            (process / 'cwd').unlink()
        self.assertIsNone(self.host.stopped())

    def test_empty_cmdline_for_live_userspace_process_is_unknown(self):
        self.process([])
        with self.assertRaisesRegex(hostlib.Failure, 'Cannot inspect live process 9000'):
            self.host.stopped()

    def test_reused_pid_is_not_accepted_as_an_unrelated_process(self):
        process = self.process(['node', '/tmp/unknown.js'])
        original = Path.read_bytes
        def replace_identity(path):
            if path == process / 'cmdline':
                (process / 'stat').write_text(process_stat(9000, start=43))
            return original(path)
        with patch.object(Path, 'read_bytes', replace_identity):
            with self.assertRaisesRegex(hostlib.Failure, 'Cannot inspect live process 9000'):
                self.host.stopped()
