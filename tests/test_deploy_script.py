"""Run scripts/deploy.sh against a stub tofu and go in an isolated Git checkout."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[1]
DIGEST = '0123456789abcdef'

TOFU = r'''#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"$STUB_DIR/calls"
for arg in "$@"; do
	case "$arg" in
	init) exit 0 ;;
	plan)
		for out in "$@"; do
			[[ "$out" == -out=* ]] && : >"${out#-out=}"
		done
		printf '  # aws_instance.host must be replaced\n      ~ user_data = "secret-looking value"\nPlan: 1 to add, 0 to change, 1 to destroy.\n'
		exit 0
		;;
	show) cat "$STUB_DIR/plan.json"; exit 0 ;;
	apply) printf 'Apply complete! Resources: 1 added, 0 changed, 1 destroyed.\n'; exit 0 ;;
	esac
done
exit 1
'''

GO = r'''#!/usr/bin/env bash
set -euo pipefail
cat >/dev/null
printf '%s\n' "$STUB_DIGEST"
'''


HOST_ID = 'i-0123456789abcdef0'


def plan_json(actions, state, before=None, after=None, attachment=('no-op',), address='aws_instance.host'):
    """A saved plan's JSON with one change to the host and its data volume's
    attachment, and the host as the plan's refresh found it."""
    before = {'id': HOST_ID, 'instance_state': state, 'instance_type': 't4g.small', **(before or {})}
    after = {**before, **(after or {})}
    return {
        'prior_state': {'values': {'root_module': {'resources': [{
            'address': address,
            'mode': 'managed',
            'type': 'aws_instance',
            'values': {'id': HOST_ID, 'instance_state': state},
        }]}}},
        'resource_changes': [{
            'address': address,
            'mode': 'managed',
            'type': 'aws_instance',
            'change': {'actions': actions, 'before': before, 'after': after},
        }, {
            'address': 'aws_volume_attachment.data',
            'mode': 'managed',
            'type': 'aws_volume_attachment',
            'change': {'actions': list(attachment), 'before': {'instance_id': HOST_ID}},
        }],
    }


class DeployScriptTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.directory = Path(temporary.name)
        self.checkout = self.directory / 'checkout'
        (self.checkout / 'scripts').mkdir(parents=True)
        (self.checkout / 'infra').mkdir()
        (self.checkout / 'dist').mkdir()
        (self.checkout / 'dist/bootstrap.zip').write_bytes(b'')
        shutil.copy2(ROOT / 'scripts/deploy.sh', self.checkout / 'scripts/deploy.sh')
        for args in (('init', '--quiet'), ('add', '.'),
                     ('-c', 'user.name=Deploy test', '-c', 'user.email=test@example.invalid',
                      '-c', 'commit.gpgsign=false', 'commit', '--quiet', '-m', 'Fixture')):
            subprocess.run(['git', *args], cwd=self.checkout, check=True)
        self.stubs = self.directory / 'stubs'
        self.stubs.mkdir()
        for name, body in (('tofu', TOFU), ('go', GO)):
            path = self.stubs / name
            path.write_text(body)
            path.chmod(0o755)
        self.summary = self.directory / 'summary.md'

    def deploy(self, mode, plan, approved=DIGEST, actions_env=True):
        (self.stubs / 'plan.json').write_text(json.dumps(plan))
        env = {key: value for key, value in os.environ.items()
               if key not in ('GITHUB_ACTIONS', 'GITHUB_STEP_SUMMARY')}
        env.update({
            'PATH': f'{self.stubs}:{env["PATH"]}',
            'TOFU': str(self.stubs / 'tofu'),
            'STUB_DIR': str(self.stubs),
            'STUB_DIGEST': DIGEST,
            'APPROVED_PLAN': approved,
        })
        if actions_env:
            env.update({'GITHUB_ACTIONS': 'true', 'GITHUB_STEP_SUMMARY': str(self.summary)})
        return subprocess.run(['bash', str(self.checkout / 'scripts/deploy.sh'), mode],
                              env=env, capture_output=True, text=True, check=False)

    def applied(self):
        calls = (self.stubs / 'calls').read_text().splitlines()
        return any(call.split()[1:2] == ['apply'] for call in calls)

    def test_an_apply_refuses_to_replace_a_running_host(self):
        result = self.deploy('apply', plan_json(['delete', 'create'], 'running'))
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertFalse(self.applied())
        self.assertIn('which is `running`', self.summary.read_text())
        self.assertIn('nothing was applied', result.stderr)

    def test_an_apply_refuses_to_remove_a_stopping_host(self):
        result = self.deploy('apply', plan_json(['delete'], 'stopping'))
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertFalse(self.applied())

    def test_a_plan_says_to_stop_the_host_first(self):
        # Stopping the host can change the digest, so the plan to apply is a
        # new one made once the host is stopped.
        result = self.deploy('plan', plan_json(['create', 'delete'], 'running'), approved='')
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        summary = self.summary.read_text()
        self.assertIn("Stop it first with Host control's stop", summary)
        self.assertIn('run Deploy again with mode `plan`', summary)
        self.assertNotIn('To apply exactly this plan', summary)
        self.assertFalse(self.applied())

    def test_a_plan_that_leaves_the_host_running_says_how_to_apply_it(self):
        result = self.deploy('plan', plan_json(['update'], 'running', after={'tags': {'Name': 'foundry'}}), approved='')
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn(f'To apply exactly this plan, run Deploy again with mode `apply` and plan `{DIGEST}`',
                      self.summary.read_text())

    def test_an_apply_replaces_a_stopped_host(self):
        result = self.deploy('apply', plan_json(['delete', 'create'], 'stopped'))
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertTrue(self.applied())
        self.assertNotIn('Stop it first', self.summary.read_text())

    def test_an_apply_changes_a_running_host_in_place(self):
        result = self.deploy('apply', plan_json(['update'], 'running', after={'tags': {'Name': 'foundry'}}))
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertTrue(self.applied())

    def test_an_apply_refuses_to_stop_a_running_host_for_a_change(self):
        # The AWS provider stops the host and starts it again to change these.
        for setting, value in (('instance_type', 't4g.medium'), ('user_data', 'changed'),
                               ('user_data_base64', 'Y2hhbmdlZA=='),
                               ('cpu_options', [{'core_count': 2}]),
                               ('capacity_reservation_specification',
                                [{'capacity_reservation_preference': 'none'}])):
            with self.subTest(setting=setting):
                result = self.deploy('apply', plan_json(['update'], 'running', after={setting: value}))
                self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
                self.assertFalse(self.applied())
                self.assertIn('which is `running`', self.summary.read_text())

    def test_an_apply_refuses_to_detach_the_data_volume_from_a_running_host(self):
        for actions in (['delete'], ['delete', 'create'], ['create', 'delete']):
            with self.subTest(actions=actions):
                result = self.deploy('apply', plan_json(['no-op'], 'running', attachment=actions))
                self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
                self.assertFalse(self.applied())
                self.assertIn('nothing was applied', result.stderr)

    def test_an_apply_resizes_a_stopped_host(self):
        result = self.deploy('apply', plan_json(['update'], 'stopped', after={'instance_type': 't4g.medium'}))
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertTrue(self.applied())

    def test_an_apply_refuses_to_resize_a_moved_host(self):
        for address in ('aws_instance.foundry', 'module.host.aws_instance.host'):
            with self.subTest(address=address):
                plan = plan_json(['update'], 'running', after={'instance_type': 't4g.medium'}, address=address)
                if address.startswith('module.'):
                    root = plan['prior_state']['values']['root_module']
                    root['child_modules'] = [{'address': 'module.host', 'resources': root.pop('resources')}]
                result = self.deploy('apply', plan)
                self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
                self.assertFalse(self.applied())
                self.assertIn('the plan stops a host that is not stopped', result.stderr)

    def test_an_apply_refuses_when_the_refresh_lost_the_host(self):
        plan = plan_json(['update'], 'running', after={'instance_type': 't4g.medium'})
        plan['prior_state']['values']['root_module']['resources'] = []
        result = self.deploy('apply', plan)
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertFalse(self.applied())
        self.assertIn('which is `unknown`', self.summary.read_text())

    def test_an_apply_without_the_host_runs(self):
        result = self.deploy('apply', {'resource_changes': []})
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertTrue(self.applied())

    def test_values_stay_out_of_the_actions_log(self):
        result = self.deploy('plan', plan_json(['update'], 'running'), approved='')
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('# aws_instance.host must be replaced', result.stdout)
        self.assertNotIn('secret-looking value', result.stdout + self.summary.read_text())

    def test_a_local_plan_prints_the_values(self):
        result = self.deploy('plan', plan_json(['update'], 'running'), approved='',
                             actions_env=False)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('secret-looking value', result.stdout)
        self.assertIn(f'Plan digest: `{DIGEST}`', result.stdout)


if __name__ == '__main__':
    unittest.main()
