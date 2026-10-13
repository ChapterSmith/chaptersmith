#!/usr/bin/env python3
"""Exact source release records, staging and checked extraction (stdlib only)."""
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import shutil
import subprocess
import sys
import tarfile
import tempfile
import unicodedata

LIBS = ('avcodec', 'avdevice', 'avfilter', 'avformat', 'avutil', 'swresample', 'swscale')
REQUIRED = ('go.mod', 'go.sum', 'Makefile', 'frontend/package.json', 'frontend/package-lock.json',
            'logo.png', 'build/darwin/icon.icns', 'build/windows/icon.ico',
            'scripts/build-ffmpeg.sh', 'scripts/stage-rebuild-sources.sh',
            'scripts/test-rebuild-sources.sh', 'scripts/source-release.py',
            'licenses/LGPL-2.1.txt', 'licenses/FFMPEG-NOTICE.txt', 'README.md', 'LICENSE')


def digest(path):
    result = hashlib.sha256()
    with Path(path).open('rb') as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b''):
            result.update(chunk)
    return result.hexdigest()


def write_json(path, value):
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + '\n', newline='\n')


def safe_extract(archive, target):
    # Source releases need regular files/directories only. Reject links/devices,
    # traversal, duplicate paths and Windows path syntax before writing anything.
    seen = set()
    for member in archive.getmembers():
        name = member.name
        parts = PurePosixPath(name).parts
        # PurePosixPath collapses dot components and repeated separators. NFC
        # and case folding catch aliases on macOS/Windows as well as Linux.
        destination = tuple(unicodedata.normalize('NFC', part).casefold() for part in parts)
        if (not name or name.startswith('/') or '..' in parts or '\\' in name or ':' in name
                or any(part.endswith((' ', '.')) for part in parts)
                or (not parts and not member.isdir())
                or not (member.isfile() or member.isdir()) or destination in seen):
            raise ValueError('unsafe archive member: ' + name)
        seen.add(destination)
        # A directory recorded without owner access (bare tar headers default
        # to 0644) would make its own contents unreadable once extracted.
        if member.isdir():
            member.mode |= 0o700
    archive.extractall(target)


def record(prefix, revision):
    prefix = Path(prefix)
    paths = ['share/ffmpeg-source.tar.gz', 'share/BUILDING-FFMPEG.txt']
    paths += [f'lib/lib{lib}.a' for lib in LIBS]
    paths += [str(p.relative_to(prefix)).replace(os.sep, '/')
              for folder in ('include', 'lib/pkgconfig')
              for p in sorted((prefix / folder).rglob('*')) if p.is_file()]
    write_json(prefix / 'share/ffmpeg-build.json', {
        'format': 1, 'linkage': 'static', 'source_revision': revision,
        'files': {name: digest(prefix / name) for name in paths},
    })


def validate_prefix(prefix):
    record_path = prefix / 'share/ffmpeg-build.json'
    if not record_path.is_file():
        raise ValueError('missing FFmpeg build record; rebuild FFmpeg with current scripts')
    meta = json.loads(record_path.read_text())
    required = ['share/ffmpeg-source.tar.gz', 'share/BUILDING-FFMPEG.txt']
    required += [f'lib/lib{lib}.a' for lib in LIBS]
    if meta.get('format') != 1 or meta.get('linkage') != 'static':
        raise ValueError('unsupported FFmpeg build record')
    for name in required:
        if name not in meta['files']:
            raise ValueError('incomplete FFmpeg build record: ' + name)
    for name, expected in meta['files'].items():
        path = prefix / name
        if not path.resolve().is_relative_to(prefix.resolve()):
            raise ValueError('unsafe build record path')
        if not path.is_file() or digest(path) != expected:
            raise ValueError('FFmpeg build/source mismatch: ' + name)
    return meta


def git(root, *args):
    return subprocess.check_output(['git', '-C', str(root), *args])


def stage(root, prefix, output, version, platform):
    root, prefix, output = map(Path, (root, prefix, output))
    for label in (version, platform):
        if not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9._-]*', label):
            raise ValueError('invalid version/platform label')
    dirty = git(root, 'status', '--porcelain', '--untracked-files=all').decode(errors='replace').strip()
    if dirty:
        listed = dirty.splitlines()
        shown = '; '.join(listed[:8]) + (f'; and {len(listed) - 8} more' if len(listed) > 8 else '')
        raise ValueError(f'dirty application revision; commit all source changes before staging ({shown})')
    revision = git(root, 'rev-parse', 'HEAD').decode().strip()
    meta = validate_prefix(prefix)
    output.mkdir(parents=True, exist_ok=True)
    name = f'ChapterSmith-{version}-{revision[:12]}-{platform}-sources.tar.gz'
    target = output / name
    if target.exists() or Path(str(target) + '.sha256').exists():
        raise ValueError('refusing to overwrite existing source release')
    with tempfile.TemporaryDirectory(prefix='source-stage-') as scratch:
        work = Path(scratch)
        app = work / 'app'
        app.mkdir()
        git_tar = work / 'application.tar'
        with git_tar.open('wb') as stream:
            subprocess.run(['git', '-C', str(root), 'archive', revision], stdout=stream, check=True)
        with tarfile.open(git_tar) as archive:
            safe_extract(archive, app)
        git_tar.unlink()
        for path in REQUIRED:
            if not (app / path).is_file():
                raise ValueError('missing required rebuild input: ' + path)
        # Use Go's grammar (quotes, Windows paths and replacement blocks), not
        # a regex. -json only reads the staged manifest. Disable caller flags,
        # workspaces/config and toolchain/network downloads for this parse.
        go_env = dict(os.environ, GOENV='off', GOWORK='off', GOFLAGS='',
                      GOTOOLCHAIN='local', GOPROXY='off', GOSUMDB='off')
        module = json.loads(subprocess.check_output(
            ['go', 'mod', 'edit', '-json', 'go.mod'], cwd=app, env=go_env))
        # A replacement without a version is a filesystem path and cannot be
        # recovered from pinned module versions/checksums in this archive.
        if any(not replacement['New'].get('Version') for replacement in module.get('Replace') or []):
            raise ValueError('local Go module replacement is not self-contained')
        dependencies = work / 'dependencies'
        dependencies.mkdir()
        for path in ('ffmpeg-source.tar.gz', 'ffmpeg-build.json', 'BUILDING-FFMPEG.txt'):
            shutil.copy2(prefix / 'share' / path, dependencies / path)
        # Check the source tar now, without retaining another extracted source tree.
        with tarfile.open(dependencies / 'ffmpeg-source.tar.gz') as archive:
            names = {member.name.removeprefix('./') for member in archive.getmembers()}
            if not {'configure', 'COPYING.LGPLv2.1'} <= names:
                raise ValueError('incomplete corresponding FFmpeg source/license')
        write_json(work / 'release.json', {
            'format': 1, 'application_revision': revision, 'version': version,
            'platform': platform, 'ffmpeg_source_revision': meta['source_revision'],
            'application_license': 'MIT',
            'ffmpeg_license': 'LGPL-2.1-or-later',
        })
        entries = {str(p.relative_to(work)).replace(os.sep, '/'): digest(p)
                   for p in sorted(work.rglob('*')) if p.is_file()}
        write_json(work / 'CONTENTS.sha256.json', entries)
        # Recheck before finalizing: never silently stage concurrent source edits.
        if git(root, 'status', '--porcelain', '--untracked-files=all').strip() or git(root, 'rev-parse', 'HEAD').decode().strip() != revision:
            raise ValueError('dirty application revision during staging')
        validate_prefix(prefix)
        partial = output / (name + '.partial')
        try:
            with tarfile.open(partial, 'w:gz') as archive:
                for path in sorted(work.iterdir()):
                    archive.add(path, arcname=path.name)
            partial.rename(target)
        finally:
            partial.unlink(missing_ok=True)
    Path(str(target) + '.sha256').write_text(digest(target) + '  ' + target.name + '\n', newline='\n')
    print(target)


def extract(source, destination):
    source, destination = Path(source), Path(destination)
    checksum = Path(str(source) + '.sha256')
    if not checksum.is_file() or checksum.read_text().split()[0] != digest(source):
        raise ValueError('source archive checksum missing or mismatch')
    if destination.exists() and any(destination.iterdir()):
        raise ValueError('extraction destination must be empty')
    destination.mkdir(parents=True, exist_ok=True)
    with tarfile.open(source) as archive:
        safe_extract(archive, destination)
    manifest = json.loads((destination / 'CONTENTS.sha256.json').read_text())
    actual = {str(p.relative_to(destination)).replace(os.sep, '/') for p in destination.rglob('*') if p.is_file()}
    if actual != set(manifest) | {'CONTENTS.sha256.json'}:
        raise ValueError('source archive file inventory mismatch')
    for name, expected in manifest.items():
        if digest(destination / name) != expected:
            raise ValueError('source archive content checksum mismatch: ' + name)
    with tarfile.open(destination / 'dependencies/ffmpeg-source.tar.gz') as archive:
        safe_extract(archive, destination / 'ffmpeg')
    print(destination)


if __name__ == '__main__':
    try:
        commands = {'record': record, 'stage': stage, 'extract': extract}
        commands[sys.argv[1]](*sys.argv[2:])
    except (ValueError, KeyError, IndexError, TypeError, OSError, tarfile.TarError, subprocess.CalledProcessError) as error:
        sys.exit('source-release: ' + str(error))
