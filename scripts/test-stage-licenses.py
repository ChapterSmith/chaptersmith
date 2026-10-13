#!/usr/bin/env python3
"""Run notice staging with a small real Go dependency; no native builds."""
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parent.parent
# Avoid Windows CreateProcess selecting System32/bash.exe (the WSL launcher).
BASH = shutil.which('bash')
if BASH is None:
    raise RuntimeError('Bash is required on PATH for license staging tests')


class LicenseStagingTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix='license staging ')
        self.addCleanup(self.tmp.cleanup)
        base = Path(self.tmp.name)
        self.repo = base / 'application'
        for name in ('scripts', 'licenses', 'cmd/fixture'):
            (self.repo / name).mkdir(parents=True)
        shutil.copy2(ROOT / 'scripts/stage-licenses.sh',
                     self.repo / 'scripts/stage-licenses.sh')
        for path in (ROOT / 'licenses').glob('*.txt'):
            shutil.copy2(path, self.repo / 'licenses' / path.name)
        self.application_license = b'Application license fixture\r\n\xc3\xa9\n'
        self.guide = b'Application and rebuild guide fixture\n'
        (self.repo / 'LICENSE').write_bytes(self.application_license)
        (self.repo / 'README.md').write_bytes(self.guide)
        dependency = base / 'dependency'
        dependency.mkdir()
        (dependency / 'go.mod').write_text('module example.com/dependency\n\ngo 1.23\n')
        (dependency / 'dep.go').write_text('package dependency\n')
        (dependency / 'LICENSE').write_bytes(b'Independent dependency license\n')
        (self.repo / 'go.mod').write_text(
            'module example.com/application\n\ngo 1.23\n'
            'require example.com/dependency v0.0.0\n'
            'replace example.com/dependency => ../dependency\n')
        (self.repo / 'cmd/fixture/main.go').write_text(
            'package main\nimport _ "example.com/dependency"\nfunc main() {}\n')
        self.prefix = base / 'prefix'
        (self.prefix / 'share').mkdir(parents=True)
        (self.prefix / 'share/BUILDING-FFMPEG.txt').write_bytes(
            b'FFmpeg build fixture\n')
        (self.prefix / 'share/ffmpeg-build.json').write_bytes(b'{"fixture": true}\n')
        self.destination = base / 'notices'
        self.mingw = base / 'ucrt64'
        pthreads = self.mingw / 'share/licenses/libwinpthread'
        pthreads.mkdir(parents=True)
        (pthreads / 'COPYING').write_text('Pthreads license fixture\n')

    def stage(self):
        env = dict(os.environ, FFMPEG_PREFIX=str(self.prefix), GOENV='off',
                   GOWORK='off', GOFLAGS='-mod=readonly', GOTOOLCHAIN='local',
                   GOPROXY='off', GOSUMDB='off', MINGW_PREFIX=str(self.mingw))
        return subprocess.run(
            [BASH, str(self.repo / 'scripts/stage-licenses.sh'),
             str(self.destination)], env=env, capture_output=True, text=True)

    def test_stages_application_materials_and_independent_notices_byte_for_byte(self):
        result = self.stage()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual((self.destination / 'ChapterSmith-LICENSE.txt').read_bytes(),
                         self.application_license)
        self.assertTrue((self.destination / 'README.md').is_file(),
                        'licensing guide was not staged')
        self.assertEqual((self.destination / 'README.md').read_bytes(), self.guide)
        for path in (self.repo / 'licenses').glob('*.txt'):
            self.assertEqual((self.destination / path.name).read_bytes(),
                             path.read_bytes())
        for name in ('BUILDING-FFMPEG.txt', 'ffmpeg-build.json'):
            self.assertEqual((self.destination / name).read_bytes(),
                             (self.prefix / 'share' / name).read_bytes())
        dependency_license = 'go-modules/example.com/dependency/LICENSE'
        self.assertEqual((self.destination / dependency_license).read_bytes(),
                         b'Independent dependency license\n')

    def test_missing_application_license_is_rejected(self):
        (self.repo / 'LICENSE').unlink()
        result = self.stage()
        self.assertNotEqual(result.returncode, 0,
                            'accepted missing application license')
        self.assertIn('LICENSE', result.stderr)
        self.assertFalse(self.destination.exists(), 'staged a partial notice directory')

    def test_missing_licensing_guide_is_rejected(self):
        (self.repo / 'README.md').unlink()
        result = self.stage()
        self.assertNotEqual(result.returncode, 0, 'accepted missing licensing guide')
        self.assertIn('README.md', result.stderr)
        self.assertFalse(self.destination.exists(), 'staged a partial notice directory')


if __name__ == '__main__':
    unittest.main()
