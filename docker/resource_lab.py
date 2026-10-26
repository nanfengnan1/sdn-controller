#!/usr/bin/env python3
"""Isolated source-build acceptance lab; never targets sdn-docker/sevpn labs.

python3 docker/resource_lab.py build
python3 docker/resource_lab.py lab-up
python3 docker/resource_lab.py legacy-test
python3 docker/resource_lab.py down

The test project has independent images, credentials, volumes and ports. Its
generated configuration is under docker/state/resource-lab; evidence is under
artifacts/resources. The original deployment configuration is never rewritten.
"""
import argparse
import importlib.util
import os
from pathlib import Path
import secrets
import subprocess
import sys

import manage


def configure():
    source = Path(__file__).resolve().parent
    sandbox = source / 'state' / 'resource-lab'
    sandbox.mkdir(parents=True, exist_ok=True)
    os.chmod(sandbox, 0o700)
    project = 'sdn-resource-it'
    content = (source / 'compose.yaml').read_text()
    content = content.replace('sdn-docker', project)
    content = content.replace('context: ../..', 'context: '+str(manage.ROOT.parent))
    content = content.replace('./state/config/', str(sandbox / 'state' / 'config')+'/')
    for name in ('sdn-agent', 'sdn-controller', 'sdn-web'):
        content = content.replace(name+':alpine', name+':resource-it')
    (sandbox / 'compose.yaml').write_text(content)
    envfile = sandbox / '.env'
    if not envfile.exists():
        env = {'SDN_'+role.upper()+'_TOKEN': secrets.token_hex(24) for role in ('admin', 'viewer', 'agent')}
        env.update(SDN_WEB_PORT='38080', SDN_GRPC_PORT='39090', SDN_WEB_BIND='0.0.0.0')
        with os.fdopen(os.open(envfile, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600), 'w') as stream:
            stream.write(''.join(key+'='+value+'\n' for key, value in env.items()))
    manage.PROJECT = project
    manage.HERE = sandbox
    manage.STATE = sandbox / 'state'
    manage.CONFIG = manage.STATE / 'config'
    manage.ART = manage.ROOT / 'artifacts' / 'resources'
    manage.COMPOSE = ['docker', 'compose', '--project-name', project, '--env-file', str(envfile), '-f', str(sandbox / 'compose.yaml')]
    manage.initialize()
    return manage


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('command', choices=('init', 'build', 'up', 'lab-up', 'legacy-test', 'resource-test', 'status', 'clean-check', 'lab-reset', 'down'))
    command = parser.parse_args().command
    lab = configure()
    if command == 'init':
        lab.validate_owned_resources()
        print('Isolated configuration prepared; Web port 38080, gRPC port 39090; credentials remain private.')
    elif command == 'build':
        subprocess.run(lab.COMPOSE+['build', *lab.BUILD_SERVICES], check=True)
    elif command in ('up', 'lab-up'):
        lab.up(lab=command == 'lab-up')
    elif command == 'status':
        print(lab.compose('ps').stdout)
    elif command == 'clean-check':
        lab.verify_clean()
    elif command == 'lab-reset':
        lab.reset(lab=True)
    elif command == 'down':
        lab.validate_owned_resources()
        lab.compose('--profile', 'tools', '--profile', 'lab', 'down', '--volumes', '--remove-orphans', timeout=420)
        lab.verify_unrelated()
        print('Only isolated test containers/volumes removed; existing deployments retained.')
    else:
        filename = 'test.py' if command == 'legacy-test' else 'resource_test.py'
        spec = importlib.util.spec_from_file_location('resource_acceptance', Path(__file__).with_name(filename))
        test = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(test)
        test.main()


if __name__ == '__main__':
    main()
