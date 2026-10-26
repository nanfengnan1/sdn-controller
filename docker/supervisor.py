#!/usr/bin/env python3
"""Supervise one Controller and its optional private, single-member etcd."""
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import time

APP = '/usr/local/bin/sdn-controller'
LAUNCH = Path('/run/sdn-controller/launch.json')
stopping = False


def signal_stop(_signum, _frame):
    global stopping
    stopping = True


def app_arguments(args):
    if not any(arg.split('=', 1)[0] in ('-config', '--config') for arg in args):
        return ['-config', '/etc/sdn-controller/controller.conf', *args]
    return args


def utility_only(args):
    """Match Go flag's final boolean values without interpreting config values."""
    modes = {'check-config': False, 'runtime-plan': False, 'healthcheck': False}
    string_flags = {'config', 'listen', 'etcd', 'etcd-prefix', 'state-file', 'tls-cert', 'tls-key', 'agent-ca'}
    true_values = {'1', 't', 'T', 'TRUE', 'true', 'True'}
    false_values = {'0', 'f', 'F', 'FALSE', 'false', 'False'}
    index = 0
    while index < len(args):
        arg = args[index]
        index += 1
        if arg == '--' or not arg.startswith('-') or arg == '-':
            break  # Go stops parsing at -- or the first positional argument.
        name, equal, value = arg.lstrip('-').partition('=')
        if name in ('h', 'help'):
            return True
        if name in string_flags:
            if not equal:
                index += 1  # A string value can itself look like a diagnostic flag.
            continue
        if name not in modes and name != 'insecure':
            return True  # Let Go reject an unknown flag without launching services.
        if equal and value not in true_values | false_values:
            return True  # Go rejects invalid boolean syntax at this occurrence.
        if name in modes:
            modes[name] = not equal or value in true_values
    return any(modes.values())


def load_plan(args):
    # The Go application is the only config parser/validator; this output excludes tokens.
    result = subprocess.run([APP, *args, '-runtime-plan'], stdout=subprocess.PIPE, timeout=10, check=True)
    return json.loads(result.stdout)


def check_etcd(plan):
    etcd = plan['etcd']
    if not etcd['enabled']:
        return True
    try:
        result = subprocess.run([
            '/usr/local/bin/etcdctl', '--endpoints', f"http://127.0.0.1:{etcd['clientPort']}",
            '--dial-timeout=1s', '--command-timeout=2s', 'endpoint', 'health',
        ], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=3)
        return result.returncode == 0
    except (subprocess.TimeoutExpired, OSError):
        return False


def check_app(args):
    try:
        result = subprocess.run([APP, *args, '-healthcheck'], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=4)
        return result.returncode == 0
    except (subprocess.TimeoutExpired, OSError):
        return False


def healthcheck():
    try:
        launch = json.loads(LAUNCH.read_text())
        # Revalidate a changed host file; an incompatible hot edit requires restart.
        plan = load_plan(launch['args'])
        return 0 if check_etcd(plan) and check_app(launch['args']) else 1
    except (OSError, ValueError, subprocess.SubprocessError):
        return 1


def stop_child(child, timeout):
    if child is None or child.poll() is not None:
        return
    child.terminate()
    try:
        child.wait(timeout=timeout)
    except subprocess.TimeoutExpired:
        child.kill()
        child.wait(timeout=5)


def require_ready(probe, children, seconds, service):
    deadline = time.monotonic() + seconds
    while not stopping and time.monotonic() < deadline:
        if any(child.poll() is not None for child in children):
            raise RuntimeError(f'{service} exited during startup')
        if probe():
            return
        time.sleep(0.2)
    if stopping:
        raise InterruptedError('shutdown requested')
    raise RuntimeError(f'{service} readiness timeout')


def main(args):
    if args == ['--healthcheck']:
        return healthcheck()
    args = app_arguments(args)
    if utility_only(args):
        return subprocess.run([APP, *args], check=False).returncode
    signal.signal(signal.SIGTERM, signal_stop)
    signal.signal(signal.SIGINT, signal_stop)
    plan = load_plan(args)  # No process launch or data directory creation before validation.
    if stopping:
        return 0
    etcd_config = plan['etcd']
    timeout = etcd_config['shutdownTimeoutSeconds'] if etcd_config['enabled'] else 20
    etcd = app = None
    try:
        LAUNCH.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
        with os.fdopen(os.open(LAUNCH, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600), 'w') as handle:
            json.dump({'args': args}, handle)
        if etcd_config['enabled']:
            Path(etcd_config['dataDir']).mkdir(mode=0o700, parents=True, exist_ok=True)
            client = f"http://127.0.0.1:{etcd_config['clientPort']}"
            peer = f"http://127.0.0.1:{etcd_config['peerPort']}"
            etcd = subprocess.Popen([
                '/usr/local/bin/etcd', '--name', etcd_config['name'], '--data-dir', etcd_config['dataDir'],
                '--listen-client-urls', client, '--advertise-client-urls', client,
                '--listen-peer-urls', peer, '--initial-advertise-peer-urls', peer,
                '--initial-cluster', etcd_config['name'] + '=' + peer,
                '--initial-cluster-state', 'new', '--log-level', 'warn',
            ])
            require_ready(lambda: check_etcd(plan), [etcd], etcd_config['startupTimeoutSeconds'], 'etcd')
        if stopping:
            raise InterruptedError('shutdown requested')
        app = subprocess.Popen([APP, *args])
        children = [child for child in (etcd, app) if child is not None]
        require_ready(lambda: check_app(args), children, etcd_config['startupTimeoutSeconds'], 'Controller')
        print('Controller and configured bundled services are ready', flush=True)
        while not stopping:
            for child in children:
                if child.poll() is not None:
                    # Even an unexpected clean exit must stop the whole container.
                    print(('etcd' if child is etcd else 'Controller') + f' exited with code {child.returncode}', file=sys.stderr, flush=True)
                    raise RuntimeError('a required service exited; stopping the container')
            time.sleep(0.2)
        return 0
    except InterruptedError:
        return 0
    finally:
        # etcd must remain alive while the Controller drains tasks/releases its writer lease.
        stop_child(app, timeout)
        stop_child(etcd, timeout)
        LAUNCH.unlink(missing_ok=True)


if __name__ == '__main__':
    try:
        sys.exit(main(sys.argv[1:]))
    except (OSError, ValueError, RuntimeError, subprocess.SubprocessError) as error:
        # Avoid printing argv/configuration: those are not a diagnostics channel for credentials.
        print('controller supervisor failed: ' + type(error).__name__, file=sys.stderr, flush=True)
        sys.exit(1)
