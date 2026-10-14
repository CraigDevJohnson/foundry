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


def plan_json(actions, state):
    """A saved plan's JSON with one change to the host."""
    return {
        'resource_changes': [{
            'address': 'aws_instance.host',
            'change': {'actions': actions, 'before': {'instance_state': state}},
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
        result = self.deploy('plan', plan_json(['create', 'delete'], 'running'), approved='')
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("Stop it first with Host control's stop", self.summary.read_text())
        self.assertFalse(self.applied())

    def test_an_apply_replaces_a_stopped_host(self):
        result = self.deploy('apply', plan_json(['delete', 'create'], 'stopped'))
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertTrue(self.applied())
        self.assertNotIn('Stop it first', self.summary.read_text())

    def test_an_apply_changes_a_running_host_in_place(self):
        result = self.deploy('apply', plan_json(['update'], 'running'))
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertTrue(self.applied())

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
