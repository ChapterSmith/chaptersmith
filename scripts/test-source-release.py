#!/usr/bin/env python3
"""Real git/tar staging tests; small source/prefix fixtures avoid native builds."""
import hashlib
import io
import json
import os
import runpy
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile
import unittest

ROOT = Path(__file__).resolve().parent.parent
LIBS = ('avcodec', 'avdevice', 'avfilter', 'avformat', 'avutil', 'swresample', 'swscale')

class ReleaseTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix='source release ')
        self.addCleanup(self.tmp.cleanup)
        self.base = Path(self.tmp.name)
        self.repo = self.base / 'app'
        self.repo.mkdir()
        # Use actual tracked inputs, including assets and dependency lockfiles.
        data = subprocess.check_output(['git', 'archive', 'HEAD'], cwd=ROOT)
        with tarfile.open(fileobj=io.BytesIO(data)) as archive:
            archive.extractall(self.repo, filter='data')
        for name in ('source-release.py', 'stage-rebuild-sources.sh', 'test-rebuild-sources.sh'):
            if (ROOT / 'scripts' / name).exists():
                shutil.copy2(ROOT / 'scripts' / name, self.repo / 'scripts' / name)
        # Independent bytes exercise required inclusion, not the prose itself.
        self.application_license = b'application license fixture\r\n'
        self.licensing_guide = b'licensing guide fixture\n'
        (self.repo / 'LICENSE').write_bytes(self.application_license)
        (self.repo / 'README.md').write_bytes(self.licensing_guide)
        self.git('init', '-q')
        self.git('add', '.')
        self.git('-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid', 'commit', '-qm', 'fixture')
        self.prefix = self.repo / 'vendor/ffmpeg'
        (self.prefix / 'share').mkdir(parents=True)
        (self.prefix / 'lib').mkdir()
        for lib in LIBS:
            (self.prefix / f'lib/lib{lib}.a').write_text('archive fixture')
        (self.prefix / 'share/BUILDING-FFMPEG.txt').write_text('Linkage: static\n')
        self.source = self.base / 'ffmpeg input'
        self.source.mkdir()
        for name in ('configure', 'COPYING.LGPLv2.1', 'modified.c'):
            (self.source / name).write_text('local modification retained\n')
        self.archive = self.prefix / 'share/ffmpeg-source.tar.gz'
        with tarfile.open(self.archive, 'w:gz') as archive:
            for path in self.source.iterdir():
                archive.add(path, arcname=path.name)
        self.out = self.base / 'output'

    def git(self, *args):
        return subprocess.check_output(['git', *args], cwd=self.repo).decode().strip()

    def run_tool(self, *args, ok=True, env=None):
        result = subprocess.run(['python3', str(self.repo / 'scripts/source-release.py'), *map(str, args)],
                                cwd=self.repo, capture_output=True, text=True, env=env)
        if ok:
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        else:
            self.assertNotEqual(result.returncode, 0, 'accepted invalid release inputs')
        return result

    def record(self):
        self.run_tool('record', self.prefix, 'ffmpeg-fixture-revision')

    def stage(self, ok=True):
        return self.run_tool('stage', self.repo, self.prefix, self.out, 'v1.2.3', 'fixture-os', ok=ok)

    def test_complete_exact_archive_and_detached_extract(self):
        self.record()
        revision = self.git('rev-parse', 'HEAD')
        self.stage()
        bundle = next(self.out.glob('*.tar.gz'))
        self.assertIn(revision[:12], bundle.name)
        checksum = (self.out / (bundle.name + '.sha256')).read_text().split()[0]
        self.assertEqual(checksum, hashlib.sha256(bundle.read_bytes()).hexdigest())
        extracted = self.base / 'extracted'
        self.run_tool('extract', bundle, extracted)
        for name in ('go.mod', 'go.sum', 'frontend/package-lock.json', 'frontend/package.json',
                     'logo.png', 'build/darwin/icon.icns', 'build/windows/icon.ico',
                     'scripts/build-ffmpeg.sh', 'scripts/test-rebuild-sources.sh',
                     'licenses/LGPL-2.1.txt', 'README.md'):
            self.assertTrue((extracted / 'app' / name).is_file(), name)
        self.assertEqual((extracted / 'app/LICENSE').read_bytes(),
                         self.application_license)
        self.assertEqual((extracted / 'app/README.md').read_bytes(),
                         self.licensing_guide)
        self.assertEqual((extracted / 'ffmpeg/modified.c').read_text(), 'local modification retained\n')
        meta = json.loads((extracted / 'release.json').read_text())
        self.assertEqual(meta['application_revision'], revision)
        self.assertEqual(meta['application_license'], 'MIT')
        self.assertEqual(meta['ffmpeg_license'], 'LGPL-2.1-or-later')
        self.assertFalse((extracted / 'app/.git').exists())

    def test_dirty_revision_is_rejected(self):
        self.record()
        (self.repo / 'go.mod').write_text('changed\n')
        result = self.stage(ok=False)
        self.assertIn('dirty application', result.stderr)

    def test_untracked_source_is_rejected(self):
        self.record()
        (self.repo / 'new-source.go').write_text('package main\n')
        self.assertIn('dirty application', self.stage(ok=False).stderr)

    def test_missing_and_changed_ffmpeg_materials_are_rejected(self):
        self.assertIn('build record', self.stage(ok=False).stderr)
        self.record()
        (self.prefix / 'lib/libavcodec.a').write_text('foreign archive')
        self.assertIn('mismatch', self.stage(ok=False).stderr)
        (self.prefix / 'lib/libavcodec.a').write_text('archive fixture')
        with self.archive.open('ab') as out:
            out.write(b'tampering')
        self.assertIn('mismatch', self.stage(ok=False).stderr)

    def test_missing_required_asset_is_rejected(self):
        self.record()
        self.git('rm', 'logo.png')
        self.git('-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid', 'commit', '-qm', 'remove asset')
        self.assertIn('required rebuild input', self.stage(ok=False).stderr)

    def test_missing_application_license_or_guide_is_rejected(self):
        self.record()
        for name in ('LICENSE', 'README.md'):
            with self.subTest(name=name):
                contents = (self.repo / name).read_bytes()
                self.git('rm', name)
                self.git('-c', 'user.name=Fixture', '-c',
                         'user.email=fixture@example.invalid',
                         'commit', '-qm', 'remove licensing input')
                self.assertIn('missing required rebuild input: ' + name,
                              self.stage(ok=False).stderr)
                (self.repo / name).write_bytes(contents)
                self.git('add', name)
                self.git('-c', 'user.name=Fixture', '-c',
                         'user.email=fixture@example.invalid',
                         'commit', '-qm', 'restore licensing input')

    def test_go_parser_rejects_quoted_local_replacements(self):
        self.record()
        original = (self.repo / 'go.mod').read_text()
        replacements = (
            'replace example.com/dep => "../private-dep"',
            'replace example.com/dep => "/private/dep"',
            r'replace example.com/dep => "C:\\private\\dep"',
            'replace example.com/dep => "C:/private/dep"',
            'replace (\n example.com/dep v1.0.0 => "../private-dep"\n)',
        )
        for replacement in replacements:
            with self.subTest(replacement=replacement):
                (self.repo / 'go.mod').write_text(original + '\n' + replacement + '\n')
                self.git('add', 'go.mod')
                self.git('-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid', 'commit', '-qm', 'replacement')
                # Go itself rejects backslash Windows paths on non-Windows hosts;
                # both that parse error and the versionless-replacement guard
                # must reject staging, before any archive is produced.
                self.assertRegex(self.stage(ok=False).stderr,
                                 'local Go module replacement|replacement directory appears to be Windows path')

    def test_versioned_go_replacement_is_read_only_and_recoverable(self):
        self.record()
        path = self.repo / 'go.mod'
        contents = path.read_text() + '\nreplace (\n example.com/dep => example.com/fork v1.2.3\n)\n'
        path.write_bytes(contents.encode())  # exact bytes: native Windows Python would write CRLF
        self.git('add', 'go.mod')
        self.git('-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid', 'commit', '-qm', 'versioned replacement')
        env = dict(os.environ, GOENV=str(self.base / 'missing-go-env'),
                   GOWORK=str(self.base / 'missing.go.work'),
                   GOFLAGS='-modfile=missing.mod', GOTOOLCHAIN='go0.0.0')
        self.run_tool('stage', self.repo, self.prefix, self.out, 'v1.2.3', 'fixture-os', env=env)
        self.assertEqual(path.read_text(), contents)
        with tarfile.open(next(self.out.glob('*.tar.gz'))) as archive:
            self.assertEqual(archive.extractfile('app/go.mod').read().decode(), contents)

    def test_extract_rejects_checksum_damage_and_unsafe_paths(self):
        self.record()
        self.stage()
        bundle = next(self.out.glob('*.tar.gz'))
        with bundle.open('ab') as out:
            out.write(b'corruption')
        self.assertIn('checksum', self.run_tool('extract', bundle, self.base / 'bad', ok=False).stderr)
        attack = self.base / 'attack.tar.gz'
        with tarfile.open(attack, 'w:gz') as archive:
            member = tarfile.TarInfo('../escape')
            member.size = 4
            archive.addfile(member, io.BytesIO(b'evil'))
        (self.base / 'attack.tar.gz.sha256').write_text(hashlib.sha256(attack.read_bytes()).hexdigest() + '  attack.tar.gz\n')
        self.assertIn('unsafe archive', self.run_tool('extract', attack, self.base / 'bad2', ok=False).stderr)
        self.assertFalse((self.base / 'escape').exists())

class ArchiveDestinationTests(unittest.TestCase):
    def setUp(self):
        self.safe_extract = runpy.run_path(str(ROOT / 'scripts/source-release.py'))['safe_extract']
        self.tmp = tempfile.TemporaryDirectory(prefix='source member ')
        self.addCleanup(self.tmp.cleanup)
        self.target = Path(self.tmp.name) / 'extracted'

    def archive(self, names):
        data = io.BytesIO()
        with tarfile.open(fileobj=data, mode='w') as archive:
            for name in names:
                member = tarfile.TarInfo(name)
                if name == '.':
                    member.type = tarfile.DIRTYPE
                    archive.addfile(member)
                else:
                    member.size = 4
                    archive.addfile(member, io.BytesIO(b'data'))
        data.seek(0)
        return tarfile.open(fileobj=data)

    def test_portable_destination_collisions_fail_before_writing(self):
        for names in (('file', './file'), ('dir/file', 'dir//file'),
                      ('File', 'file'), ('caf\u00e9', 'cafe\u0301'),
                      ('file', 'file.'), ('file', 'file ')):
            with self.subTest(names=names), self.archive(names) as archive:
                with self.assertRaisesRegex(ValueError, 'unsafe archive'):
                    self.safe_extract(archive, self.target)
                self.assertFalse(self.target.exists(), 'wrote files before rejecting collisions')

    def test_ffmpeg_root_directory_member_remains_supported(self):
        with self.archive(('.', './configure')) as archive:
            self.safe_extract(archive, self.target)
        self.assertEqual((self.target / 'configure').read_bytes(), b'data')

if __name__ == '__main__':
    unittest.main()
