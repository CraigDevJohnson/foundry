"""Effective systemd 252 properties, checked before any service launch."""
import copy
import json
from pathlib import Path
import sys
import unittest
from unittest.mock import Mock, patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'host'))
import hostlib


def unit_properties():
    return {
        'User': 'foundry', 'Group': 'foundry', 'KillMode': 'control-group',
        'KillSignal': '15', 'SendSIGKILL': 'no', 'Restart': 'no',
        'FragmentPath': '/etc/systemd/system/foundry.service', 'DropInPaths': '',
        'LoadState': 'loaded', 'NeedDaemonReload': 'no', 'Type': 'exec',
        'WorkingDirectory': '/foundry/foundryvtt',
        'TimeoutStartUSec': '10min', 'TimeoutStopUSec': '1min 30s',
        'TimeoutStartFailureMode': 'terminate', 'TimeoutStopFailureMode': 'terminate',
        'UMask': '0007', 'NoNewPrivileges': 'yes', 'PrivateTmp': 'yes',
        'RemainAfterExit': 'no', 'DynamicUser': 'no', 'SupplementaryGroups': '',
        'Environment': '', 'PassEnvironment': '', 'UnsetEnvironment': '',
        'RootDirectory': '', 'RootImage': '', 'BindPaths': '',
        'BindReadOnlyPaths': '', 'TemporaryFileSystem': '',
        'RequiresMountsFor': '/var/tmp /foundry/foundryvtt /foundry',
        'Requires': 'sysinit.target -.mount system.slice foundry.mount',
        'After': 'foundry.mount sysinit.target network-online.target time-sync.target',
        'Wants': 'time-sync.target network-online.target tmp.mount',
    }


def command_properties():
    commands = {name: {'type': 'a(sasasttttuii)', 'data': []} for name in (
        'ExecStartEx', 'ExecStartPreEx', 'ExecConditionEx', 'ExecStartPostEx',
        'ExecReloadEx', 'ExecStopEx', 'ExecStopPostEx')}
    commands['ExecStartEx']['data'] = [[
        '/usr/bin/node', ['/usr/bin/node', '/foundry/foundryvtt/main.js',
                          '--dataPath=/foundry/foundrydata', '--port=30000'],
        [], 0, 0, 0, 0, 0, 0, 0]]
    commands['ExecStartPreEx']['data'] = [[
        '/usr/local/sbin/foundry-host', ['/usr/local/sbin/foundry-host', 'preflight'],
        ['privileged'], 0, 0, 0, 0, 0, 0, 0]]
    commands['EnvironmentFiles'] = {'type': 'a(sb)', 'data': []}
    commands['PermissionsStartOnly'] = {'type': 'b', 'data': False}
    return commands


class UnitContractTests(unittest.TestCase):
    def setUp(self):
        self.properties = unit_properties()
        self.commands = command_properties()
        self.host = hostlib.Host.__new__(hostlib.Host)
        self.host.state = Mock(return_value={'ActiveState': 'inactive'})
        self.host.stopped = Mock()
        self.host.ready = Mock()
        self.calls = []
        self.runner = patch.object(hostlib, 'run', side_effect=self.run_command)
        self.runner.start()
        self.addCleanup(self.runner.stop)

    def run_command(self, args, timeout=15):
        self.calls.append(args)
        if args[:2] == ['/usr/bin/systemctl', 'show']:
            return '\n'.join(key + '=' + value for key, value in self.properties.items())
        if args[0] == '/usr/bin/busctl':
            return '\n'.join(json.dumps(self.commands[name]) for name in args[6:])
        if args == ['/usr/bin/systemctl', 'start', 'foundry.service']:
            return ''
        raise AssertionError(args)

    def assert_start_rejected(self):
        with self.assertRaisesRegex(hostlib.Failure, 'contract'):
            self.host.start()
        self.assertNotIn(['/usr/bin/systemctl', 'start', 'foundry.service'], self.calls)

    def test_reviewed_contract_starts_after_both_effective_queries(self):
        self.host.start()
        self.assertEqual([Path(args[0]).name for args in self.calls],
                         ['systemctl', 'busctl', 'systemctl'])
        self.host.stopped.assert_called_once()
        self.host.ready.assert_called_once()

    def test_execution_and_shutdown_property_drift_never_starts(self):
        for name in self.properties:
            with self.subTest(property=name), patch.dict(self.properties, {name: 'drift'}):
                self.assert_start_rejected()

    def test_missing_properties_never_start(self):
        for name in list(self.properties):
            saved = self.properties.pop(name)
            with self.subTest(property=name):
                self.assert_start_rejected()
            self.properties[name] = saved

    def test_dependency_order_and_system_added_dependencies_are_accepted(self):
        self.properties['RequiresMountsFor'] = '/foundry /var/tmp /foundry/foundryvtt'
        self.properties['After'] += ' basic.target systemd-journald.socket'
        self.properties['Requires'] += ' basic.target'
        self.host.unit_contract()

    def test_removing_or_adding_any_command_never_starts(self):
        for name, prop in self.commands.items():
            if not name.startswith('Exec'):
                continue
            changed = copy.deepcopy(prop)
            changed['data'].append(copy.deepcopy(self.commands['ExecStartEx']['data'][0]))
            with self.subTest(command=name, change='added'), patch.dict(self.commands, {name: changed}):
                self.assert_start_rejected()
            if prop['data']:
                with self.subTest(command=name, change='removed'), patch.dict(
                        self.commands, {name: {'type': prop['type'], 'data': []}}):
                    self.assert_start_rejected()

    def test_paths_arguments_and_privilege_flags_are_exact(self):
        for name in ('ExecStartEx', 'ExecStartPreEx'):
            original = self.commands[name]
            mutations = [(0, '/bin/sh'), (1, ['/bin/sh', '-c', 'true']),
                         (1, [' '.join(original['data'][0][1])]),
                         (2, ['ignore-failure']), (2, ['no-setuid']),
                         (2, ['no-env-expand']), (2, ['privileged', 'ignore-failure'])]
            mutations.append((2, ['privileged'] if name == 'ExecStartEx' else []))
            for index, value in mutations:
                changed = copy.deepcopy(original)
                changed['data'][0][index] = value
                with self.subTest(command=name, index=index, value=value), patch.dict(
                        self.commands, {name: changed}):
                    self.assert_start_rejected()

    def test_command_history_does_not_change_execution_contract(self):
        for name in ('ExecStartEx', 'ExecStartPreEx'):
            self.commands[name]['data'][0][3:] = [1000, 2000, 3000, 4000, 1234, 1, 0]
        self.host.unit_contract()

    def test_legacy_privileges_and_environment_file_drift_never_start(self):
        self.commands['PermissionsStartOnly']['data'] = True
        self.assert_start_rejected()
        self.commands = command_properties()
        self.commands['EnvironmentFiles']['data'] = [['/tmp/foundry.env', False]]
        self.assert_start_rejected()

    def test_incomplete_or_malformed_dbus_data_never_starts(self):
        for value in ({}, {'type': 'wrong', 'data': []},
                      {'type': 'a(sasasttttuii)', 'data': None},
                      {'type': 'a(sasasttttuii)', 'data': [[]]}):
            with self.subTest(value=value), patch.dict(self.commands, {'ExecStartEx': value}):
                self.assert_start_rejected()
        with patch.object(hostlib, 'run', side_effect=[
                '\n'.join(key + '=' + value for key, value in self.properties.items()), '{']):
            self.assert_start_rejected()

    def test_failed_effective_query_never_starts(self):
        with patch.object(hostlib, 'run', side_effect=hostlib.Failure('contract query failed')):
            self.assert_start_rejected()


if __name__ == '__main__':
    unittest.main()
