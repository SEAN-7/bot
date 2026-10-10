#!/usr/bin/env python3
"""Build on Linux, using the official Node runtime."""
import hashlib, json, os, platform, shutil, subprocess, sys, tarfile, tempfile, urllib.request
from pathlib import Path, PurePosixPath

NODE = '24.21.0'
ROOT = Path(__file__).resolve().parents[1]

def download(url, path):
    print(f'Downloading {url}', flush=True)
    with urllib.request.urlopen(url, timeout=120) as response, open(path, 'wb') as output:
        shutil.copyfileobj(response, output)

def digest(path):
    with open(path, 'rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()

def run(*args, **kw):
    print('Running ' + str(args[0]) + ' ' + ' '.join(str(x) for x in args[1:]), flush=True)
    subprocess.run([str(x) for x in args], check=True, **kw)

def extract_checked(archive, destination):
    destination = Path(destination).resolve()
    for member in archive.getmembers():
        name = PurePosixPath(member.name)
        if name.is_absolute() or '..' in name.parts or not (member.isdir() or member.isfile() or member.issym() or member.islnk()):
            raise RuntimeError('Unsafe runtime archive entry')
        if member.issym() or member.islnk():
            base = destination / member.name if member.issym() else destination / '.'
            target = base.parent / member.linkname if member.issym() else base / member.linkname
            if not target.resolve().is_relative_to(destination):
                raise RuntimeError('Runtime archive link escapes extraction directory')
    archive.extractall(destination)

def trim_node(runtime):
    # npm is needed only while building production dependencies.
    for name in ('include', 'share', 'lib'):
        path = runtime / name
        if path.exists(): shutil.rmtree(path)
    for path in (runtime / 'bin').iterdir():
        if path.name != 'node': path.unlink()

def main():
    if platform.system() != 'Linux':
        raise SystemExit('Build on Linux (matching the target architecture).')
    arch = {'x86_64': 'x64', 'aarch64': 'arm64'}.get(platform.machine())
    if arch is None:
        raise SystemExit('Supported architectures: x86_64 and aarch64.')
    if not (ROOT / 'install/manage.mjs').is_file():
        raise SystemExit('Missing install/manage.mjs; finish the installer before packaging.')
    out = Path(sys.argv[1] if len(sys.argv) > 1 else ROOT / 'dist').resolve()
    out.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix='bot-build-') as temporary:
        temp = Path(temporary)
        bundle = temp / 'bundle'
        app = bundle / 'app'
        app.mkdir(parents=True)
        for name in ('package.json', 'package-lock.json', 'README.md', 'src', 'apps', 'harness', 'docs', 'examples'):
            source = ROOT / name
            if source.is_dir(): shutil.copytree(source, app / name)
            else: shutil.copy2(source, app / name)
        shutil.copytree(ROOT / 'install', bundle / 'install')
        filename = f'node-v{NODE}-linux-{arch}.tar.xz'
        base = f'https://nodejs.org/dist/v{NODE}/'
        download(base + 'SHASUMS256.txt', temp / 'node-shasums')
        download(base + filename, temp / filename)
        expected = next(line.split()[0] for line in (temp / 'node-shasums').read_text().splitlines() if line.split()[-1] == filename)
        if digest(temp / filename) != expected: raise RuntimeError('Node archive checksum mismatch')
        with tarfile.open(temp / filename) as archive: extract_checked(archive, temp / 'node')
        runtime = bundle / 'runtime'
        runtime.mkdir()
        shutil.move(temp / 'node' / filename.removesuffix('.tar.xz'), runtime / 'node')
        node = runtime / 'node' / 'bin' / 'node'
        env = {**os.environ, 'PATH': f'{node.parent}:{os.environ["PATH"]}'}
        run(node, runtime / 'node/lib/node_modules/npm/bin/npm-cli.js', 'ci', '--omit=dev', '--ignore-scripts', cwd=app, env=env)
        trim_node(runtime / 'node')
        version = json.loads((app / 'package.json').read_text())['version']
        (bundle / 'bundle.json').write_text(json.dumps({'format': 1, 'repository': 'SEAN-7/bot', 'version': version, 'platform': 'linux', 'arch': arch, 'runtimes': {'node': NODE}}, indent=2) + '\n')
        run(node, '--version')
        run(node, '--input-type=module', '-e', 'import { TuiAltScreen } from "@earendil-works/pi-tui"; if (typeof TuiAltScreen !== "function") process.exit(1)', cwd=app)
        stage = os.environ.get('BOT_BUNDLE_STAGE')
        if stage:
            stagepath = Path(stage).resolve()
            if stagepath.exists():
                raise SystemExit('Build staging destination already exists; use --repack or choose a new path.')
            stagepath.parent.mkdir(parents=True, exist_ok=True)
            shutil.move(bundle, stagepath)
            bundle = stagepath
        package(bundle, out, arch)

def package(bundle, out, arch):
    # Validate the actual release, including installer/core compatibility, before packaging.
    run(bundle / "runtime/node/bin/node", "--input-type=module", "-e", 'import { validateBundle } from "./install/manage.mjs"; await validateBundle(process.cwd());', cwd=bundle)
    archivepath = out / '.payload.tar.gz'
    with tarfile.open(archivepath, 'w:gz', compresslevel=6) as archive:
        for child in sorted(bundle.iterdir()): archive.add(child, arcname=child.name)
    with tarfile.open(archivepath) as archive:
        for member in archive.getmembers():
            if PurePosixPath(member.name).is_absolute() or '..' in PurePosixPath(member.name).parts:
                raise RuntimeError('Unsafe bundle archive path')
            if member.issym():
                if PurePosixPath(member.linkname).is_absolute():
                    raise RuntimeError(f'Absolute bundle symlink is not relocatable: {member.name}')
                destination = (bundle / member.name).parent / member.linkname
                if not destination.resolve().is_relative_to(bundle.resolve()):
                    raise RuntimeError(f'Bundle symlink escapes bundle: {member.name}')
    stub = (ROOT / 'install/bootstrap.sh').read_text().replace('@PAYLOAD_SHA256@', digest(archivepath))
    target = out / f'bot-linux-{arch}'
    target.write_bytes(stub.encode() + archivepath.read_bytes())
    target.chmod(0o755)
    (out / f'{target.name}.sha256').write_text(f'{digest(target)}  {target.name}\n')
    print(target)
    archivepath.unlink()

def repack():
    bundle = Path(sys.argv[2]).resolve()
    out = Path(sys.argv[3]).resolve()
    out.mkdir(parents=True, exist_ok=True)
    def dependency_lock(path):
        value = json.loads(path.read_text())
        value.pop('version', None)
        value.get('packages', {}).get('', {}).pop('version', None)
        return value
    if dependency_lock(bundle / 'app/package-lock.json') != dependency_lock(ROOT / 'package-lock.json'):
        raise SystemExit('Dependency lock changed; rebuild runtimes and production dependencies.')
    shutil.rmtree(bundle / 'app' / 'defaults', ignore_errors=True)
    shutil.copy2(ROOT / 'package-lock.json', bundle / 'app/package-lock.json')
    for name in ('src', 'apps', 'harness', 'docs', 'examples'):
        shutil.rmtree(bundle / 'app' / name, ignore_errors=True)
        shutil.copytree(ROOT / name, bundle / 'app' / name)
    shutil.copy2(ROOT / 'package.json', bundle / 'app/package.json')
    shutil.copy2(ROOT / 'README.md', bundle / 'app/README.md')
    shutil.rmtree(bundle / 'install')
    shutil.copytree(ROOT / 'install', bundle / 'install')
    metadata = json.loads((bundle / 'bundle.json').read_text())
    metadata['version'] = json.loads((ROOT / 'package.json').read_text())['version']
    (bundle / 'bundle.json').write_text(json.dumps(metadata, indent=2) + '\n')
    trim_node(bundle / 'runtime/node')
    package(bundle, out, metadata['arch'])

if __name__ == '__main__':
    if len(sys.argv) == 4 and sys.argv[1] == '--repack': repack()
    else: main()
