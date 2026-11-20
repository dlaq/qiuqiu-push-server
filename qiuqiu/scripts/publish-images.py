#!/usr/bin/env python3
"""Merge tested architecture tags and save public references, never registry tokens."""
import json
import os
from pathlib import Path
import re
import subprocess


def run(*args):
    return subprocess.run(args, check=True, capture_output=True, text=True).stdout


def digest(reference):
    info = run('docker', 'buildx', 'imagetools', 'inspect', reference)
    return re.search(r'^Digest:\s+(sha256:[0-9a-f]{64})$', info, re.M)[1]


namespace = os.environ['DOCKERHUB_NAMESPACE']
sha = os.environ['GITHUB_SHA']
ref = os.environ['GITHUB_REF']
assert re.fullmatch(r'[a-z0-9][a-z0-9_-]*', namespace)
assert re.fullmatch(r'[0-9a-f]{40}', sha)
tags = ['latest']
if ref.startswith('refs/tags/qiuqiu-v'):
    version = ref.removeprefix('refs/tags/qiuqiu-v')
    assert re.fullmatch(r'\d+\.\d+\.\d+', version), 'Release tag must be qiuqiu-vX.Y.Z'
    tags.append(version)
else:
    assert ref == 'refs/heads/main', 'Publishing is limited to main and release tags'

report = {'commit': sha, 'run_url': os.environ['GITHUB_SERVER_URL'] + '/' + os.environ['GITHUB_REPOSITORY'] + '/actions/runs/' + os.environ['GITHUB_RUN_ID'], 'images': []}
for component in ('gateway', 'ntfy'):
    image = f'docker.io/{namespace}/qiuqiu-push-{component}'
    sources = [image + '@' + digest(f'{image}:sha-{sha}-{arch}') for arch in ('amd64', 'arm64')]
    immutable = f'{image}:sha-{sha}'
    run('docker', 'buildx', 'imagetools', 'create', '--tag', immutable, *sources)
    content_digest = digest(immutable)
    pinned = image + '@' + content_digest
    manifest = json.loads(run('docker', 'buildx', 'imagetools', 'inspect', '--raw', pinned))
    platforms = sorted(f"{x['platform']['os']}/{x['platform']['architecture']}" for x in manifest['manifests'])
    assert platforms == ['linux/amd64', 'linux/arm64'], platforms
    for tag in tags:
        run('docker', 'buildx', 'imagetools', 'create', '--tag', image + ':' + tag, pinned)
        assert digest(image + ':' + tag) == content_digest
    report['images'].append({'image': image, 'tags': tags + ['sha-' + sha], 'digest': content_digest, 'platforms': platforms, 'native_container_integration': 'passed on both architectures'})

Path('docker-publication.json').write_text(json.dumps(report, indent=2) + '\n')
with open(os.environ['GITHUB_STEP_SUMMARY'], 'a') as summary:
    summary.write('## Published QiuQiu images\n\n')
    for item in report['images']:
        summary.write(f"- `{item['image']}:latest` — amd64 + arm64; `{item['digest']}`\n")
    summary.write('\nPublic image references and digests are in the docker-publication artifact.\n')

