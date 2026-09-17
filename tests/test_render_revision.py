"""Exercise renderer provenance against a real, isolated Git checkout."""
import json
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest

from test_host import manifest


ROOT = Path(__file__).resolve().parents[1]


class RenderRevisionTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.directory = Path(temporary.name)
        self.checkout = self.directory / 'checkout'
        shutil.copytree(ROOT / 'host', self.checkout / 'host',
                        ignore=shutil.ignore_patterns('__pycache__'))
        (self.checkout / '.gitignore').write_text('__pycache__/\n*.ignored\n')
        self.git('init', '--quiet')
        self.git('add', '.')
        self.git('-c', 'user.name=Renderer test', '-c', 'user.email=test@example.invalid',
                 '-c', 'commit.gpgsign=false', 'commit', '--quiet', '-m', 'Fixture')
        self.value = manifest()
        self.value['setup_revision'] = self.git('rev-parse', 'HEAD').strip()
        self.manifest = self.directory / 'manifest.json'
        self.output = self.directory / 'rendered'

    def git(self, *arguments):
        return subprocess.run(['git', '-c', 'core.hooksPath=/dev/null', *arguments],
                              cwd=self.checkout, check=True, capture_output=True,
                              text=True, timeout=15).stdout

    def render(self):
        self.manifest.write_text(json.dumps(self.value))
        return subprocess.run([sys.executable, str(self.checkout / 'host/render.py'),
                               '--manifest', str(self.manifest), '--output', str(self.output)],
                              capture_output=True, text=True, timeout=15)

    def assert_refused(self, message):
        result = self.render()
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn(message, result.stderr)
        self.assertFalse(self.output.exists())

    def test_matching_clean_checkout_renders_the_pinned_sources(self):
        result = self.render()
        self.assertEqual(result.returncode, 0, result.stderr)
        installed = json.loads((self.output / 'etc/foundry/host.json').read_text())
        self.assertEqual(installed['setup_revision'], self.value['setup_revision'])
        self.assertEqual((self.output / 'usr/local/lib/foundry/hostlib.py').read_bytes(),
                         (self.checkout / 'host/hostlib.py').read_bytes())
        self.assertEqual((self.output / 'etc/systemd/system/foundry.service').read_bytes(),
                         (self.checkout / 'host/templates/foundry.service').read_bytes())

    def test_clean_detached_worktree_renders(self):
        worktree = self.directory / 'worktree'
        self.git('worktree', 'add', '--detach', str(worktree), 'HEAD')
        self.checkout = worktree
        result = self.render()
        self.assertEqual(result.returncode, 0, result.stderr)
        installed = json.loads((self.output / 'etc/foundry/host.json').read_text())
        self.assertEqual(installed['setup_revision'], self.value['setup_revision'])

    def test_wrong_revision_is_refused_without_output(self):
        self.value['setup_revision'] = '0' * 40
        self.assert_refused('setup_revision does not match')

    def test_modified_tracked_source_is_refused_without_output(self):
        with (self.checkout / 'host/hostlib.py').open('a') as source:
            source.write('\n# Unreviewed change\n')
        self.assert_refused('clean Git checkout')

    def test_staged_source_is_refused_without_output(self):
        template = self.checkout / 'host/templates/foundry.service'
        template.write_text(template.read_text() + '\n# Unreviewed change\n')
        self.git('add', 'host/templates/foundry.service')
        self.assert_refused('clean Git checkout')

    def test_untracked_source_is_refused_without_output(self):
        (self.checkout / 'host/templates/extra.service').write_text('[Unit]\n')
        self.assert_refused('clean Git checkout')

    def test_ignored_untracked_render_input_is_refused_without_output(self):
        (self.checkout / 'host/templates/extra.ignored').write_text('[Unit]\n')
        self.assert_refused('untracked render input')

    def test_missing_git_metadata_is_refused_without_output(self):
        shutil.rmtree(self.checkout / '.git')
        self.assert_refused('Git checkout')


if __name__ == '__main__':
    unittest.main()
