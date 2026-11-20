#!/usr/bin/env python3
"""Test both local images in an isolated, disposable Compose project."""
import json
import os
from pathlib import Path
import secrets
import subprocess
import sys
import tempfile
import time
import urllib.request


def run(*args, **kwargs):
    return subprocess.run(args, check=True, text=True, capture_output=True, **kwargs).stdout


gateway = os.environ.get('QQ_GATEWAY_IMAGE', 'qiuqiu-ci-gateway:test')
ntfy = os.environ.get('QQ_NTFY_IMAGE', 'qiuqiu-ci-ntfy:test')
expected = os.environ.get('QQ_EXPECTED_ARCH')
for image in (gateway, ntfy):
    arch = run('docker', 'image', 'inspect', '--format', '{{.Architecture}}', image).strip()
    if expected and arch != expected:
        raise SystemExit(f'Image architecture mismatch: expected {expected}, found {arch}')

with tempfile.TemporaryDirectory(prefix='qiuqiu-image-test-') as directory:
    root = Path(directory)
    root.chmod(0o700)
    password = secrets.token_hex(24)
    for name in ('admin_password', 'ntfy_admin_password'):
        (root / name).write_text(password)
        (root / name).chmod(0o600)
    hashed = run('docker', 'run', '--rm', '-i', gateway, 'hash-password', input=password)
    (root / 'ntfy_admin_hash').write_text(hashed.strip())
    (root / 'ntfy_admin_hash').chmod(0o600)
    config = {
        'services': {
            'ntfy': {
                'image': ntfy,
                'environment': {
                    'NTFY_BASE_URL': 'http://ntfy.localhost',
                    'NTFY_LISTEN_HTTP': ':80',
                    'NTFY_EXPERIMENTAL_LISTEN_ADMIN_UNIX': '/run/ntfy/admin.sock',
                    'NTFY_AUTH_FILE': '/var/lib/ntfy/auth.db',
                    'NTFY_AUTH_DEFAULT_ACCESS': 'deny-all',
                    'NTFY_CACHE_FILE': '/var/lib/ntfy/cache.db',
                    'NTFY_CACHE_DURATION': '168h',
                    'NTFY_MESSAGE_SIZE_LIMIT': '64k',
                    'NTFY_ATTACHMENT_CACHE_DIR': '/var/lib/ntfy/attachments',
                    'NTFY_ENABLE_SIGNUP': 'false',
                },
                'secrets': ['ntfy_admin_hash'],
                'volumes': ['ntfy_data:/var/lib/ntfy', 'ntfy_socket:/run/ntfy'],
            },
            'gateway': {
                'image': gateway,
                'ports': ['127.0.0.1::8080'],
                'environment': {
                    'QQ_NTFY_URL': 'http://ntfy:80',
                    'QQ_NTFY_PUBLIC_URL': 'http://ntfy.localhost',
                    'QQ_NTFY_HOST': 'ntfy.localhost',
                    'QQ_ADMIN_PASSWORD_FILE': '/run/secrets/admin_password',
                    'QQ_NTFY_ADMIN_PASSWORD_FILE': '/run/secrets/ntfy_admin_password',
                    'QQ_NTFY_ADMIN_SOCKET': '/run/ntfy/admin.sock',
                },
                'secrets': ['admin_password', 'ntfy_admin_password'],
                'volumes': ['gateway_data:/data', 'ntfy_socket:/run/ntfy'],
                'depends_on': ['ntfy'],
            },
        },
        'volumes': {name: {} for name in ('gateway_data', 'ntfy_data', 'ntfy_socket')},
        'secrets': {name: {'file': str(root / name)} for name in ('admin_password', 'ntfy_admin_password', 'ntfy_admin_hash')},
    }
    compose = root / 'compose.json'
    compose.write_text(json.dumps(config))
    command = ['docker', 'compose', '-p', 'qiuqiu-ci-' + secrets.token_hex(5), '-f', str(compose)]
    try:
        run(*command, 'up', '-d', '--no-build', '--pull', 'never')
        address = run(*command, 'port', 'gateway', '8080').strip()
        base = 'http://' + address
        for _ in range(90):
            try:
                with urllib.request.urlopen(base + '/healthz', timeout=2) as response:
                    if json.load(response).get('healthy'):
                        break
            except OSError:
                pass
            time.sleep(1)
        else:
            raise RuntimeError('Gateway did not become healthy')
        for path in ('/', '/brand.png'):
            with urllib.request.urlopen(base + path, timeout=10) as response:
                assert response.status == 200 and response.read(), path
        env = dict(os.environ, QQ_TEST_URL=base, QQ_TEST_ADMIN_PASSWORD_FILE=str(root / 'admin_password'))
        print(run(sys.executable, str(Path(__file__).with_name('integration-test.py')), env=env))
    except Exception:
        print(run(*command, 'logs', '--no-color', '--tail', '40'), file=sys.stderr)
        raise
    finally:
        run(*command, 'down', '--volumes', '--remove-orphans')

