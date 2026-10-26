#!/usr/bin/env python3
"""Reliability regressions, isolated etcd keys and a disposable FRR instance.

Does not deploy binaries to, restart, or reconfigure the existing EVPN lab.
Requires Go, Docker, and a dedicated development etcd endpoint (not production).
"""
import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile
import time
import uuid

ROOT = Path(__file__).resolve().parents[1]
AGENT = ROOT.parent / "sdn-agent"
WEB = ROOT.parent / "sdn-web"
ART = ROOT / "artifacts" / "reliability"
IMAGE = "quay.io/frrouting/frr:10.0.0"
LABEL = "sdn.reliability=rollback-test"


def run(*args, timeout=180, **kwargs):
    return subprocess.run([str(x) for x in args], capture_output=True, text=True,
                          timeout=timeout, **kwargs)


def checked(*args, **kwargs):
    p = run(*args, **kwargs)
    if p.returncode:
        raise RuntimeError(p.stdout + p.stderr)
    return p.stdout


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--go", default="go")
    ap.add_argument("--etcd", required=True, help="isolated development endpoint")
    args = ap.parse_args()
    ART.mkdir(parents=True, exist_ok=True)
    results = []
    env = os.environ.copy()
    env["SDNC_TEST_ETCD"] = args.etcd

    def stage(name, command, cwd=None, stage_env=None):
        print("RUN", name, flush=True)
        start = time.monotonic()
        p = run(*command, cwd=cwd, env=stage_env or env, timeout=240)
        (ART / (name+".log")).write_text(p.stdout+p.stderr, encoding="utf-8")
        results.append(dict(name=name, exit_code=p.returncode, seconds=round(time.monotonic()-start, 2)))
        (ART / "results.json").write_text(json.dumps(results, indent=2), encoding="utf-8")
        if p.returncode:
            raise RuntimeError(name+" failed: "+(p.stdout+p.stderr)[-6000:])
        print("PASS", name, flush=True)

    for name, directory in [("agent", AGENT), ("controller", ROOT), ("gateway", WEB / "gateway")]:
        stage(name+"-race", [args.go, "test", "-race", "-count=1", "-json", "./..."], directory)
        stage(name+"-vet", [args.go, "vet", "./..."], directory)
        stage(name+"-build", [args.go, "build", "./..."], directory)

    binary = ART / "sdna-frr-recovery.test"
    build_env = dict(env, CGO_ENABLED="0")
    stage("frr-test-build", [args.go, "test", "-c", "-o", binary, "./internal/agent"], AGENT, build_env)
    name = "sdnc-recovery-"+uuid.uuid4().hex[:12]
    with tempfile.TemporaryDirectory(prefix="sdnc-recovery-") as tmp:
        directory = Path(tmp)
        conf = directory / "frr"
        conf.mkdir(mode=0o777)
        os.chmod(directory, 0o755)
        os.chmod(conf, 0o777)
        daemons = checked("docker", "run", "--rm", "--network", "none", "--entrypoint", "cat", IMAGE, "/etc/frr/daemons")
        (conf / "daemons").write_text(daemons.replace("bgpd=no", "bgpd=yes"))
        (conf / "frr.conf").write_text("frr defaults traditional\nhostname recovery-test\nservice integrated-vtysh-config\n")
        (conf / "vtysh.conf").write_text("service integrated-vtysh-config\n")
        os.chmod(conf / "frr.conf", 0o666)
        checked("docker", "run", "-d", "--name", name, "--label", LABEL,
                "--network", "none", "--cap-add", "NET_ADMIN", "--cap-add", "NET_RAW", "--cap-add", "SYS_ADMIN",
                "-v", str(conf)+":/etc/frr", "-v", str(ART)+":/tests:ro", IMAGE)
        try:
            for _ in range(30):
                probe = run("docker", "exec", name, "vtysh", "-c", "show version", timeout=10)
                if probe.returncode == 0:
                    break
                time.sleep(0.5)
            else:
                raise RuntimeError("FRR fixture startup failed: "+checked("docker", "logs", name))
            stage("real-frr-rollback", ["docker", "exec", "-e", "SDNA_REAL_FRR_RECOVERY=1", name,
                  "/tests/sdna-frr-recovery.test", "-test.v", "-test.run", "^TestRealFRRRollbackRetry$", "-test.timeout", "60s"])
        finally:
            owner = checked("docker", "inspect", "--format", '{{index .Config.Labels "sdn.reliability"}}', name).strip()
            if owner != "rollback-test":
                raise RuntimeError("refusing cleanup: unexpected fixture label")
            checked("docker", "rm", "-f", name)
    summary = dict(status="PASS", stages=len(results), results="results.json",
                   scope="Agent/Controller/Gateway race, vet, build; real etcd response faults; isolated real FRR rollback retry",
                   live_lab_modified=False)
    (ART / "summary.json").write_text(json.dumps(summary, indent=2), encoding="utf-8")
    print(json.dumps(summary), flush=True)


if __name__ == "__main__":
    main()
