"""Offline service tests. Service commands are simulated; local OpenSSL creates fixtures."""
from contextlib import ExitStack, redirect_stderr, redirect_stdout
from datetime import datetime, timedelta, timezone
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / 'host'))
import hostlib
import render


def manifest():
    """Synthetic pins, unrelated to any deployed host or world."""
    value = json.loads((ROOT / 'host/host.example.json').read_text())
    value.update({
        'setup_revision': 'a' * 40,
        'mount_uuid': '01234567-89ab-cdef-0123-456789abcdef',
        'uid': 1001,
        'gid': 1001,
        'worlds': ['alpha', 'beta'],
    })
    value['os'].update({
        'ami_id': 'ami-0123456789abcdef0',
        'system_release': 'Amazon Linux release 2023.8.20250908 (Amazon Linux)',
    })
    for key in ('node_binary_sha256', 'node_archive_sha256',
                'foundry_main_sha256', 'foundry_archive_sha256'):
        value[key] = 'b' * 64
    for name in value['rpms']:
        value['rpms'][name] = name + '-1.2.3-1.amzn2023.aarch64'
    return value


class FakeMachine:
    """An in-memory supervisor with real cgroup files in a temporary directory."""

    def __init__(self, directory):
        self.cgroup = directory / 'cgroup'
        self.cgroup.mkdir()
        self.proc = directory / 'proc'
        process = self.proc / '1234'
        process.mkdir(parents=True)
        (process / 'status').write_text('Uid:\t1001 1001 1001 1001\nGid:\t1001 1001 1001 1001\n')
        (process / 'cmdline').write_bytes(
            b'/usr/bin/node\0/foundry/foundryvtt/main.js\0--dataPath=/foundry/foundrydata\0--port=30000\0')
        (process / 'exe').symlink_to('/usr/bin/node')
        self.contract = {
            'User': 'foundry', 'Group': 'foundry', 'KillMode': 'control-group',
            'KillSignal': '15', 'SendSIGKILL': 'no', 'Restart': 'no',
            'FragmentPath': '/etc/systemd/system/foundry.service', 'DropInPaths': '',
        }
        self.calls = []
        self.owner = ''
        self.mount_output = manifest()['mount_uuid'] + ' ext4 rw,relatime'
        self.http_code = '200'
        self.synchronized = 'yes'
        self.stop_error = False
        self.remain_after_stop = False
        self.set_state(False)

    def set_state(self, active):
        self.fields = {
            'LoadState': 'loaded',
            'ActiveState': 'active' if active else 'inactive',
            'SubState': 'running' if active else 'dead',
            'Result': 'success',
            'MainPID': '1234' if active else '0',
            'ControlPID': '0',
            'ControlGroup': '/system.slice/foundry.service' if active else '',
        }
        (self.cgroup / 'cgroup.procs').write_text('1234\n' if active else '')
        self.owner = 'LISTEN 0 511 *:30000 *:* users:(("node",pid=1234,fd=9))' if active else ''

    def run(self, args, timeout=15):
        self.calls.append(args)
        command = Path(args[0]).name
        if command == 'findmnt':
            return self.mount_output
        if command == 'ss':
            return self.owner
        if command == 'curl':
            return self.http_code
        if command == 'timedatectl':
            return self.synchronized
        if command == 'runuser':
            return ''
        if command == 'systemctl':
            if args[1] == 'show':
                if args[-1].startswith('--property=User,'):
                    return '\n'.join(key + '=' + val for key, val in self.contract.items())
                return '\n'.join(key + '=' + val for key, val in self.fields.items())
            if args[1] == 'start':
                self.set_state(True)
                return ''
            if args[1] == 'stop':
                if self.stop_error:
                    raise hostlib.Failure('systemctl stop timed out')
                self.set_state(False)
                if self.remain_after_stop:
                    (self.cgroup / 'cgroup.procs').write_text('1234\n')
                return ''
        raise AssertionError('Unexpected external command: ' + repr(args))

    def mutations(self):
        return [args[1] for args in self.calls
                if Path(args[0]).name == 'systemctl' and args[1] in {'start', 'stop', 'restart'}]


class LifecycleTests(unittest.TestCase):
    def setUp(self):
        self.stack = ExitStack()
        self.addCleanup(self.stack.close)
        self.directory = Path(self.stack.enter_context(tempfile.TemporaryDirectory()))
        self.machine = FakeMachine(self.directory)
        self.stack.enter_context(patch.object(hostlib, 'CGROUP', self.machine.cgroup))
        self.stack.enter_context(patch.object(hostlib, 'PROC', self.machine.proc))
        self.stack.enter_context(patch.object(hostlib, 'run', side_effect=self.machine.run))
        self.host = hostlib.Host(manifest())

    def test_missing_wrong_readonly_or_non_ext4_mount_is_rejected(self):
        for output in ('', 'wrong ext4 rw', manifest()['mount_uuid'] + ' xfs rw',
                       manifest()['mount_uuid'] + ' ext4 ro,relatime'):
            with self.subTest(output=output):
                self.machine.mount_output = output
                with self.assertRaisesRegex(hostlib.Failure, 'mount UUID'):
                    self.host.mount()

    def test_mount_probe_failure_is_not_treated_as_empty_root_directory(self):
        with patch.object(hostlib, 'run', side_effect=hostlib.Failure('findmnt failed')):
            with self.assertRaisesRegex(hostlib.Failure, 'findmnt failed'):
                self.host.mount()

    def test_unmanaged_listener_prevents_any_start(self):
        self.machine.owner = 'LISTEN 0 511 *:30000 *:* users:(("node",pid=9000,fd=9))'
        with self.assertRaisesRegex(hostlib.Failure, 'Shutdown is not verified'):
            self.host.start()
        self.assertEqual(self.machine.mutations(), [])

    def test_opaque_listener_prevents_any_start(self):
        self.machine.owner = 'LISTEN 0 511 *:30000 *:*'
        with self.assertRaisesRegex(hostlib.Failure, 'Cannot identify'):
            self.host.start()
        self.assertEqual(self.machine.mutations(), [])

    def test_unmanaged_extra_listener_is_not_ready(self):
        self.machine.set_state(True)
        self.machine.owner += '\nLISTEN 0 511 [::]:30000 [::]:* users:(("node",pid=9000,fd=9))'
        with self.assertRaisesRegex(hostlib.Failure, 'single managed listener'):
            self.host.ready()

    def test_stopped_requires_empty_nested_cgroups(self):
        nested = self.machine.cgroup / 'child'
        nested.mkdir()
        (nested / 'cgroup.procs').write_text('9000\n')
        with self.assertRaisesRegex(hostlib.Failure, 'Shutdown is not verified'):
            self.host.stopped()

    def test_failed_service_result_cannot_be_reported_as_stopped(self):
        self.machine.fields['Result'] = 'timeout'
        with self.assertRaisesRegex(hostlib.Failure, 'Shutdown is not verified'):
            self.host.stopped()

    def test_clean_stopped_and_ready_states(self):
        self.assertIsNone(self.host.stopped())
        self.machine.set_state(True)
        self.assertIsNone(self.host.ready())

    def test_tls_http_readiness_does_not_accept_error_response(self):
        self.machine.set_state(True)
        self.machine.http_code = '503'
        with self.assertRaisesRegex(hostlib.Failure, 'HTTPS response: 503'):
            self.host.ready()
        request = next(call for call in self.machine.calls if Path(call[0]).name == 'curl')
        self.assertIn('https://foundry.craigdevjohnson.com:30000/', request)
        self.assertNotIn('--insecure', request)
        self.assertNotIn('-k', request)

    def test_start_stop_and_restart_converge_on_reported_states(self):
        self.host.start()
        self.host.start()
        self.host.restart()
        self.host.stop()
        self.host.stop()
        self.assertEqual(self.machine.mutations(), ['start', 'stop', 'start', 'stop'])
        self.assertIsNone(self.host.stopped())

    def test_failed_stop_prevents_restart(self):
        self.machine.set_state(True)
        self.machine.stop_error = True
        with self.assertRaisesRegex(hostlib.Failure, 'stop timed out'):
            self.host.restart()
        self.assertEqual(self.machine.mutations(), ['stop'])
        self.assertEqual(self.machine.fields['ActiveState'], 'active')

    def test_unsafe_effective_unit_prevents_stop_signal(self):
        self.machine.set_state(True)
        for key, value in (('User', 'root'), ('SendSIGKILL', 'yes'), ('KillMode', 'process'),
                           ('DropInPaths', '/etc/systemd/system/foundry.service.d/override.conf')):
            with self.subTest(key=key), patch.dict(self.machine.contract, {key: value}):
                with self.assertRaisesRegex(hostlib.Failure, 'shutdown contract differs'):
                    self.host.stop()
        self.assertEqual(self.machine.mutations(), [])
        self.assertEqual(self.machine.fields['ActiveState'], 'active')

    def test_failed_or_activating_service_refuses_another_stop_signal(self):
        self.machine.set_state(True)
        for active, substate, result in (
            ('failed', 'failed', 'timeout'), ('activating', 'start-pre', 'success'),
            ('deactivating', 'stop-sigterm', 'success'), ('active', 'running', 'exit-code'),
        ):
            with self.subTest(active=active, result=result):
                self.machine.fields.update(ActiveState=active, SubState=substate, Result=result)
                with self.assertRaisesRegex(hostlib.Failure, 'state is unresolved'):
                    self.host.stop()
        self.assertEqual(self.machine.mutations(), [])

    def test_wrong_process_identity_is_not_ready(self):
        self.machine.set_state(True)
        (self.machine.proc / '1234/status').write_text('Uid:\t0 0 0 0\nGid:\t1001 1001 1001 1001\n')
        with self.assertRaisesRegex(hostlib.Failure, 'pinned command and foundry identity'):
            self.host.ready()

    def test_wrong_process_command_is_not_ready(self):
        self.machine.set_state(True)
        (self.machine.proc / '1234/cmdline').write_bytes(b'/usr/bin/node\0/tmp/other.js\0')
        with self.assertRaisesRegex(hostlib.Failure, 'pinned command and foundry identity'):
            self.host.ready()

    def test_successful_stop_command_with_survivor_prevents_restart(self):
        self.machine.set_state(True)
        self.machine.remain_after_stop = True
        with self.assertRaisesRegex(hostlib.Failure, 'Shutdown is not verified'):
            self.host.restart()
        self.assertEqual(self.machine.mutations(), ['stop'])

    def test_readiness_timeout_never_claims_start_success(self):
        self.machine.http_code = '503'
        with patch.object(hostlib.time, 'monotonic', side_effect=[0, 61]):
            with self.assertRaisesRegex(hostlib.Failure, 'readiness timed out'):
                self.host.start()
        self.assertEqual(self.machine.fields['ActiveState'], 'active')
        self.assertEqual(self.machine.mutations(), ['start'])

    def test_duplicate_preflight_stops_before_any_certificate_action(self):
        self.machine.owner = 'LISTEN 0 511 *:30000 *:* users:(("node",pid=9000,fd=9))'
        with patch.object(self.host, 'inputs'), patch.object(self.host, 'renew') as renew:
            with self.assertRaisesRegex(hostlib.Failure, 'duplicate startup'):
                self.host.preflight()
        renew.assert_not_called()

    def test_preflight_rechecks_port_after_renewal(self):
        def occupy_port():
            self.machine.owner = 'LISTEN 0 511 *:30000 *:* users:(("node",pid=9000,fd=9))'
        with patch.object(self.host, 'inputs'), patch.object(self.host, 'renew'), \
                patch.object(self.host, 'publish_certificate', side_effect=occupy_port), \
                patch.object(hostlib, 'CERT_LOCK', self.directory / 'certificate.lock'):
            with self.assertRaisesRegex(hostlib.Failure, 'became occupied'):
                self.host.preflight()

    def test_unsynchronized_clock_blocks_startup_before_certificate_work(self):
        self.machine.synchronized = 'no'
        with patch.object(self.host, 'inputs'), patch.object(self.host, 'renew') as renew, \
                patch.object(hostlib.time, 'monotonic', side_effect=[0, 61]), \
                patch.object(hostlib.time, 'sleep') as sleep:
            with self.assertRaisesRegex(hostlib.Failure, 'Clock is not synchronized'):
                self.host.preflight()
        renew.assert_not_called()
        sleep.assert_not_called()

    def test_preflight_waits_for_certificate_lock_then_proceeds(self):
        lock = self.directory / 'certificate.lock'
        holder = hostlib.locked(lock)
        holder.__enter__()
        released = []
        def release_on_wait(seconds):
            holder.__exit__(None, None, None)
            released.append(seconds)
        try:
            with patch.object(self.host, 'inputs'), patch.object(self.host, 'renew') as renew, \
                    patch.object(self.host, 'publish_certificate'), \
                    patch.object(hostlib, 'CERT_LOCK', lock), \
                    patch.object(hostlib.time, 'monotonic', side_effect=[0, 0, 0.1]), \
                    patch.object(hostlib.time, 'sleep', side_effect=release_on_wait):
                self.assertIsNone(self.host.preflight())
            self.assertEqual(len(released), 1)
            renew.assert_called_once_with()
        finally:
            if not released:
                holder.__exit__(None, None, None)

    def test_certificate_lock_wait_has_a_deadline(self):
        lock = self.directory / 'certificate.lock'
        with hostlib.locked(lock), patch.object(hostlib.time, 'monotonic', side_effect=[0, 0.1, 201]), \
                patch.object(hostlib.time, 'sleep') as sleep:
            with self.assertRaisesRegex(hostlib.Busy, 'Another host operation'):
                with hostlib.locked(lock, wait=200):
                    self.fail('A contended certificate lock must not be acquired')
        self.assertEqual(sleep.call_count, 1)

    def test_renewal_failure_uses_only_independently_valid_current_pair(self):
        with patch.object(self.host, 'inputs'), \
                patch.object(self.host, 'renew', side_effect=hostlib.Failure('ACME unavailable')), \
                patch.object(self.host, 'certificate') as certificate, \
                patch.object(self.host, 'publish_certificate') as publish, \
                patch.object(hostlib, 'CERT_LOCK', self.directory / 'certificate.lock'), \
                redirect_stderr(io.StringIO()):
            self.assertIsNone(self.host.preflight())
            certificate.assert_called_once_with(hostlib.TLS / 'current')
            publish.assert_not_called()

    def test_renewal_failure_and_invalid_current_pair_block_preflight(self):
        with patch.object(self.host, 'inputs'), \
                patch.object(self.host, 'renew', side_effect=hostlib.Failure('ACME unavailable')), \
                patch.object(self.host, 'certificate', side_effect=hostlib.Failure('expired current pair')), \
                patch.object(self.host, 'publish_certificate') as publish, \
                patch.object(hostlib, 'CERT_LOCK', self.directory / 'certificate.lock'), \
                redirect_stderr(io.StringIO()):
            with self.assertRaisesRegex(hostlib.Failure, 'expired current pair'):
                self.host.preflight()
            publish.assert_not_called()


class InputTests(unittest.TestCase):
    def setUp(self):
        self.stack = ExitStack()
        self.addCleanup(self.stack.close)
        self.directory = Path(self.stack.enter_context(tempfile.TemporaryDirectory()))
        self.mount = self.directory / 'foundry'
        self.app = self.mount / 'foundryvtt'
        self.data = self.mount / 'foundrydata'
        self.tls = self.directory / 'tls'
        self.pin = manifest()
        for path in (self.app, self.data / 'Data/worlds/alpha', self.data / 'Data/worlds/beta',
                     self.data / 'Config', self.data / 'Backups'):
            path.mkdir(parents=True, exist_ok=True)
        for path in (self.data, *self.data.rglob('*')):
            path.chmod(0o770)
        (self.app / 'package.json').write_text(json.dumps({'version': '14.366.0'}))
        self.options = {'port': 30000, 'hostname': hostlib.HOSTNAME,
                        'sslCert': str(self.tls / 'current/fullchain.pem'),
                        'sslKey': str(self.tls / 'current/privkey.pem')}
        (self.data / 'Config/options.json').write_text(json.dumps(self.options))
        self.owner_overrides = {}
        real_stat = Path.stat
        def file_stat(path, *args, **kwargs):
            result = list(real_stat(path, *args, **kwargs))
            if path == self.mount or path == self.app:
                result[4:6] = [0, 0]
            elif path == self.data or self.data in path.parents:
                result[4:6] = [1001, 1001]
            if path in self.owner_overrides:
                result[4:6] = self.owner_overrides[path]
            return os.stat_result(result)
        real_read = Path.read_text
        def read_text(path, *args, **kwargs):
            if str(path) == '/etc/system-release':
                return self.pin['os']['system_release']
            return real_read(path, *args, **kwargs)
        self.node_version = 'v24.18.1'
        def command(args, timeout=15):
            if args[0] == '/usr/bin/findmnt':
                return self.pin['mount_uuid'] + ' ext4 rw'
            if args[0] == '/usr/bin/rpm':
                return self.pin['rpms'][args[-1]]
            if args == ['/usr/bin/node', '--version']:
                return self.node_version
            raise AssertionError('Unexpected external command: ' + repr(args))
        for name, value in (('MOUNT', self.mount), ('APP', self.app), ('DATA', self.data), ('TLS', self.tls)):
            self.stack.enter_context(patch.object(hostlib, name, value))
        self.stack.enter_context(patch.object(Path, 'stat', file_stat))
        self.stack.enter_context(patch.object(Path, 'read_text', read_text))
        self.stack.enter_context(patch.object(hostlib.platform, 'machine', return_value='aarch64'))
        self.stack.enter_context(patch.object(hostlib.pwd, 'getpwnam',
                                            return_value=SimpleNamespace(pw_uid=1001, pw_gid=1001)))
        self.stack.enter_context(patch.object(hostlib.grp, 'getgrnam', return_value=SimpleNamespace(gr_gid=1001)))
        self.stack.enter_context(patch.object(hostlib, 'run', side_effect=command))
        self.hash = self.stack.enter_context(patch.object(hostlib, 'digest', return_value='b' * 64))
        self.host = hostlib.Host(self.pin)

    def test_complete_pinned_filesystem_and_ownership_are_accepted(self):
        self.assertIsNone(self.host.inputs())

    def test_wrong_data_owner_is_rejected(self):
        self.owner_overrides[self.data] = [0, 1001]
        with self.assertRaisesRegex(hostlib.Failure, 'unsafe ownership or mode'):
            self.host.inputs()

    def test_writable_application_directory_is_rejected(self):
        self.app.chmod(0o775)
        with self.assertRaisesRegex(hostlib.Failure, 'not writable by others'):
            self.host.inputs()

    def test_missing_second_world_is_rejected(self):
        (self.data / 'Data/worlds/beta').rmdir()
        with self.assertRaises(FileNotFoundError):
            self.host.inputs()

    def test_changed_node_version_is_rejected(self):
        self.node_version = 'v24.99.0'
        with self.assertRaisesRegex(hostlib.Failure, 'Node version differs'):
            self.host.inputs()

    def test_changed_node_binary_is_rejected(self):
        self.hash.return_value = 'c' * 64
        with self.assertRaisesRegex(hostlib.Failure, 'Runtime hash differs.*node'):
            self.host.inputs()

    def test_changed_foundry_version_is_rejected(self):
        (self.app / 'package.json').write_text(json.dumps({'version': '14.999.0'}))
        with self.assertRaisesRegex(hostlib.Failure, 'Foundry version differs'):
            self.host.inputs()

    def test_changed_tls_options_are_rejected(self):
        self.options['sslKey'] = '/tmp/other-key.pem'
        (self.data / 'Config/options.json').write_text(json.dumps(self.options))
        with self.assertRaisesRegex(hostlib.Failure, 'fixed hostname, port and TLS paths'):
            self.host.inputs()


class CommandTests(unittest.TestCase):
    def setUp(self):
        self.stack = ExitStack()
        self.addCleanup(self.stack.close)
        self.directory = Path(self.stack.enter_context(tempfile.TemporaryDirectory()))
        self.host = Mock(spec=hostlib.Host)
        self.stack.enter_context(patch.object(hostlib, 'Host', return_value=self.host))
        self.stack.enter_context(patch.object(hostlib.os, 'geteuid', return_value=0))
        config = Mock()
        config.stat.return_value = SimpleNamespace(st_uid=0, st_mode=0o100600)
        config.is_symlink.return_value = False
        config.read_text.return_value = json.dumps(manifest())
        self.stack.enter_context(patch.object(hostlib, 'CONFIG', config))
        self.real_locked = hostlib.locked
        self.lock_path = self.directory / 'lifecycle.lock'
        self.stack.enter_context(patch.object(hostlib, 'locked', side_effect=lambda path=None:
            self.real_locked(self.lock_path if path is None else self.directory / 'certificate.lock')))

    def invoke(self, action):
        output = io.StringIO()
        with redirect_stdout(output):
            code = hostlib.main([action])
        return code, json.loads(output.getvalue())

    def test_reported_status_requires_successful_verification(self):
        self.host.state.return_value = {'ActiveState': 'inactive'}
        self.assertEqual(self.invoke('status'), (0, {'schema': 1, 'action': 'status', 'outcome': 'stopped'}))
        self.host.stopped.side_effect = hostlib.Failure('unmanaged listener')
        code, output = self.invoke('status')
        self.assertEqual(code, 1)
        self.assertEqual(output['outcome'], 'error')
        self.assertIn('unmanaged listener', output['message'])

    def test_mutation_lock_refuses_start_stop_restart_and_renew(self):
        for action in ('start', 'stop', 'restart', 'renew'):
            with self.subTest(action=action), self.real_locked(self.lock_path):
                code, output = self.invoke(action)
                self.assertEqual(code, 2)
                self.assertEqual(output['outcome'], 'error')
                self.assertIn('Another host operation', output['message'])
        self.assertEqual(self.host.method_calls, [])

    def test_lock_is_held_for_entire_operation_and_released_after_failure(self):
        nested = []
        def stop_while_starting():
            nested.append(self.invoke('stop'))
            raise hostlib.Failure('startup failed')
        self.host.start.side_effect = stop_while_starting
        self.assertEqual(self.invoke('start')[0], 1)
        self.assertEqual(nested[0][0], 2)
        self.host.stop.assert_not_called()
        self.assertEqual(self.invoke('stop'), (0, {'schema': 1, 'action': 'stop', 'outcome': 'stopped'}))

    def test_help_works_without_privileges_or_reading_host_configuration(self):
        for argv in (['--help'], ['start', '--help']):
            with self.subTest(argv=argv), redirect_stdout(io.StringIO()) as output, \
                    patch.object(hostlib.os, 'geteuid', side_effect=AssertionError('must not require root')), \
                    patch.object(hostlib, 'CONFIG') as config:
                self.assertEqual(hostlib.main(argv), 0)
                self.assertIn('Usage: foundry-host', output.getvalue())
                self.assertIn('Examples:', output.getvalue())
                self.assertEqual(config.mock_calls, [])
        self.assertEqual(self.host.method_calls, [])


class RenewalTests(unittest.TestCase):
    def setUp(self):
        self.host = hostlib.Host(manifest())

    def test_renewal_targets_one_lineage_with_directory_hooks_disabled(self):
        with patch.object(Path, 'read_text', return_value='[renewalparams]\nauthenticator = standalone\n'), \
                patch.object(hostlib, 'run', return_value='') as run, \
                patch.object(self.host, 'certificate') as certificate:
            self.assertIsNone(self.host.renew())
        self.assertEqual(run.call_args.args[0], [
            '/usr/bin/certbot', '--config', '/etc/foundry/certbot.ini', 'renew',
            '--cert-name', 'foundry.craigdevjohnson.com', '--non-interactive',
            '--no-random-sleep-on-renew', '--no-directory-hooks',
        ])
        certificate.assert_called_once_with(hostlib.LINEAGE)

    def test_legacy_hook_or_wrong_authenticator_prevents_certbot_execution(self):
        configs = [
            '[renewalparams]\nauthenticator = nginx\n',
            '[renewalparams]\nauthenticator = standalone\ninstaller = nginx\n',
            '[renewalparams]\nauthenticator = standalone\npre_hook = killall node\n',
            'deploy_hook = systemctl restart foundry\n[renewalparams]\nauthenticator = standalone\n',
        ]
        for config in configs:
            with self.subTest(config=config), patch.object(Path, 'read_text', return_value=config), \
                    patch.object(hostlib, 'run') as run:
                with self.assertRaises(hostlib.Failure):
                    self.host.renew()
                run.assert_not_called()


class CertificateTests(unittest.TestCase):
    """Real OpenSSL checks, with a throwaway CA and no network or system trust changes."""

    @classmethod
    def setUpClass(cls):
        cls.openssl = shutil.which('openssl')
        if not cls.openssl:
            raise unittest.SkipTest('OpenSSL is required for certificate fixtures')
        cls.temp = tempfile.TemporaryDirectory()
        cls.addClassCleanup(cls.temp.cleanup)
        cls.directory = Path(cls.temp.name)
        cls.ca = cls.directory / 'ca.pem'
        cls.ca_key = cls.directory / 'ca.key'
        cls.execute(['req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '30',
                     '-subj', '/CN=Offline test CA', '-keyout', str(cls.ca_key), '-out', str(cls.ca)])
        (cls.directory / 'newcerts').mkdir()
        (cls.directory / 'index.txt').write_text('')
        (cls.directory / 'serial').write_text('01\n')
        cls.pairs = {}
        now = datetime.now(timezone.utc)
        for name, hostname, start, end in (
            ('valid', hostlib.HOSTNAME, now - timedelta(days=1), now + timedelta(days=7)),
            ('expired', hostlib.HOSTNAME, now - timedelta(days=7), now - timedelta(days=1)),
            ('future', hostlib.HOSTNAME, now + timedelta(days=1), now + timedelta(days=7)),
            ('wrong-host', 'other.example.test', now - timedelta(days=1), now + timedelta(days=7)),
            ('too-short', hostlib.HOSTNAME, now - timedelta(days=1), now + timedelta(hours=1)),
        ):
            pair = cls.directory / name
            pair.mkdir()
            config = pair / 'ca.cnf'
            config.write_text(
                '[ca]\ndefault_ca = test_ca\n[test_ca]\n'
                'dir = ' + str(cls.directory) + '\n'
                'database = $dir/index.txt\nnew_certs_dir = $dir/newcerts\n'
                'certificate = $dir/ca.pem\nprivate_key = $dir/ca.key\nserial = $dir/serial\n'
                'default_md = sha256\npolicy = test_policy\nunique_subject = no\n'
                '[test_policy]\ncommonName = supplied\n[server_cert]\n'
                'basicConstraints = critical,CA:FALSE\nkeyUsage = critical,digitalSignature,keyEncipherment\n'
                'extendedKeyUsage = serverAuth\nsubjectAltName = DNS:' + hostname + '\n')
            cls.execute(['req', '-new', '-newkey', 'rsa:2048', '-nodes',
                         '-subj', '/CN=' + hostname, '-keyout', str(pair / 'privkey.pem'),
                         '-out', str(pair / 'server.csr')])
            cls.execute(['ca', '-batch', '-notext', '-config', str(config), '-extensions', 'server_cert',
                         '-startdate', start.strftime('%y%m%d%H%M%SZ'),
                         '-enddate', end.strftime('%y%m%d%H%M%SZ'),
                         '-in', str(pair / 'server.csr'), '-out', str(pair / 'fullchain.pem')])
            cls.pairs[name] = pair

    @classmethod
    def execute(cls, arguments):
        return subprocess.run([cls.openssl] + arguments, check=True, capture_output=True, text=True)

    def local_openssl(self, args, timeout=15):
        self.assertEqual(args[0], '/usr/bin/openssl')
        args = [self.openssl] + args[1:]
        if '-CAfile' in args:
            args[args.index('-CAfile') + 1] = str(self.ca)
        result = subprocess.run(args, capture_output=True, text=True, timeout=timeout)
        if result.returncode:
            raise hostlib.Failure('openssl rejected certificate: ' + result.stderr.strip())
        return result.stdout.strip()

    def test_trusted_valid_matching_pair_is_accepted(self):
        with patch.object(hostlib, 'run', side_effect=self.local_openssl):
            self.assertIsNone(hostlib.Host(manifest()).certificate(self.pairs['valid']))

    def test_expired_future_wrong_hostname_and_short_validity_are_rejected(self):
        for name in ('expired', 'future', 'wrong-host', 'too-short'):
            with self.subTest(name=name), patch.object(hostlib, 'run', side_effect=self.local_openssl):
                with self.assertRaisesRegex(hostlib.Failure, 'openssl rejected'):
                    hostlib.Host(manifest()).certificate(self.pairs[name])

    def test_invalid_certificate_and_mismatched_private_key_are_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            pair = Path(directory)
            shutil.copyfile(self.pairs['valid'] / 'fullchain.pem', pair / 'fullchain.pem')
            shutil.copyfile(self.pairs['future'] / 'privkey.pem', pair / 'privkey.pem')
            with patch.object(hostlib, 'run', side_effect=self.local_openssl):
                with self.assertRaisesRegex(hostlib.Failure, 'do not match'):
                    hostlib.Host(manifest()).certificate(pair)
                (pair / 'fullchain.pem').write_text('invalid certificate\n')
                with self.assertRaisesRegex(hostlib.Failure, 'openssl rejected'):
                    hostlib.Host(manifest()).certificate(pair)

    def test_publication_failure_preserves_active_certificate_pair(self):
        with tempfile.TemporaryDirectory() as directory:
            tls = Path(directory) / 'tls'
            previous = tls / 'previous'
            previous.mkdir(parents=True)
            (previous / 'fullchain.pem').write_text('original certificate')
            (previous / 'privkey.pem').write_text('original key')
            (tls / 'current').symlink_to('previous')
            host = hostlib.Host(manifest())
            with patch.object(hostlib, 'TLS', tls), patch.object(hostlib, 'LINEAGE', self.pairs['valid']), \
                    patch.object(hostlib.os, 'chown'), \
                    patch.object(host, 'certificate', side_effect=[None, hostlib.Failure('copied pair invalid')]):
                with self.assertRaisesRegex(hostlib.Failure, 'copied pair invalid'):
                    host.publish_certificate()
            self.assertEqual((tls / 'current').resolve(), previous.resolve())
            self.assertEqual((tls / 'current/fullchain.pem').read_text(), 'original certificate')
            self.assertEqual((tls / 'current/privkey.pem').read_text(), 'original key')

    def test_publication_activates_a_valid_complete_pair_with_read_only_group_access(self):
        with tempfile.TemporaryDirectory() as directory:
            tls = Path(directory) / 'tls'
            with patch.object(hostlib, 'TLS', tls), patch.object(hostlib, 'LINEAGE', self.pairs['valid']), \
                    patch.object(hostlib.os, 'chown'), patch.object(hostlib, 'run', side_effect=self.local_openssl):
                hostlib.Host(manifest()).publish_certificate()
            self.assertTrue((tls / 'current').is_symlink())
            self.assertEqual((tls / 'current').resolve().parent, tls.resolve())
            self.assertEqual((tls / 'current').stat().st_mode & 0o777, 0o750)
            for name in ('fullchain.pem', 'privkey.pem'):
                self.assertEqual((tls / 'current' / name).read_bytes(), (self.pairs['valid'] / name).read_bytes())
                self.assertEqual((tls / 'current' / name).stat().st_mode & 0o777, 0o640)


class RenderTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.directory = Path(self.temp.name)
        self.manifest = self.directory / 'manifest.json'
        self.output = self.directory / 'staged'

    def test_incomplete_example_fails_without_creating_output(self):
        with self.assertRaises(hostlib.Failure):
            render.render(ROOT / 'host/host.example.json', self.output)
        self.assertFalse(self.output.exists())

    def test_complete_pins_render_contract_paths_modes_and_template_content(self):
        value = manifest()
        self.manifest.write_text(json.dumps(value))
        render.render(self.manifest, self.output)
        expected = {
            'etc/foundry/host.json': 0o600,
            'etc/foundry/certbot.ini': 0o644,
            'etc/fstab.foundry': 0o644,
            'etc/systemd/system/foundry.service': 0o644,
            'etc/systemd/system/foundry-cert-renew.service': 0o644,
            'etc/systemd/system/foundry-cert-renew.timer': 0o644,
            'usr/local/lib/foundry/hostlib.py': 0o644,
            'usr/local/sbin/foundry-host': 0o755,
        }
        files = {str(path.relative_to(self.output)) for path in self.output.rglob('*') if path.is_file()}
        self.assertEqual(files, set(expected))
        for relative, mode in expected.items():
            with self.subTest(path=relative):
                self.assertEqual((self.output / relative).stat().st_mode & 0o777, mode)
        self.assertEqual(json.loads((self.output / 'etc/foundry/host.json').read_text()), value)
        self.assertEqual((self.output / 'etc/fstab.foundry').read_text(),
                         'UUID=01234567-89ab-cdef-0123-456789abcdef /foundry ext4 '
                         'defaults,nofail,x-systemd.device-timeout=30s 0 2\n')
        unit = (self.output / 'etc/systemd/system/foundry.service').read_text()
        for directive in ('User=foundry', 'Group=foundry', 'RequiresMountsFor=/foundry',
                          'KillSignal=SIGTERM', 'KillMode=control-group', 'SendSIGKILL=no',
                          'Restart=no', 'ExecStartPre=+/usr/local/sbin/foundry-host preflight',
                          'ExecStart=/usr/bin/node /foundry/foundryvtt/main.js '
                          '--dataPath=/foundry/foundrydata --port=30000'):
            self.assertIn(directive + '\n', unit)
        timer = (self.output / 'etc/systemd/system/foundry-cert-renew.timer').read_text()
        self.assertIn('Persistent=true\n', timer)

    def test_existing_output_is_preserved(self):
        self.manifest.write_text(json.dumps(manifest()))
        self.output.mkdir()
        sentinel = self.output / 'existing'
        sentinel.write_text('preserve me')
        with self.assertRaisesRegex(hostlib.Failure, 'Output already exists'):
            render.render(self.manifest, self.output)
        self.assertEqual(sentinel.read_text(), 'preserve me')
        self.assertEqual(list(self.output.iterdir()), [sentinel])

    def test_changed_runtime_incomplete_rpm_and_root_uid_are_rejected(self):
        for key, invalid in (('node_version', '24.99.0'), ('foundry_version', '14.999.0'),
                             ('uid', 0), ('gid', True), ('worlds', ['alpha', 'alpha']),
                             ('node_archive_sha256', None), ('rpms', {}),
                             ('schema', True), ('worlds', [11, 22])):
            with self.subTest(key=key):
                value = manifest()
                value[key] = invalid
                self.manifest.write_text(json.dumps(value))
                with self.assertRaises(hostlib.Failure):
                    render.render(self.manifest, self.output)
                self.assertFalse(self.output.exists())


if __name__ == '__main__':
    unittest.main()
