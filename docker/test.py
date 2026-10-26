#!/usr/bin/env python3
"""Provision and test only the fresh sdn-docker stack; reset is a separate step.

Topology and addressing follow /home/alexan/vxlan-s-evpn. Never execute that
project's lab.sh or modify its sevpn containers/configuration.
"""
import importlib.util
import json
import time
import manage


def main():
    spec = importlib.util.spec_from_file_location("evpn_acceptance", manage.ROOT / "lab/run.py")
    lab = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(lab)
    values, tokens = manage.initialize()
    lab.PREFIX = manage.PROJECT
    lab.STATE, lab.ART = manage.STATE, manage.ART
    lab.BASE = "http://127.0.0.1:"+values["SDN_WEB_PORT"]+"/api/v1"
    lab.TOKENS = tokens
    lab.protected = manage.unrelated_snapshot

    def exe(node, *args, **kwargs):
        # The source-built image installs public executables in PATH.
        args = tuple("sdnctl" if x == "/agent/bin/sdnctl" else x for x in args)
        return manage.exe(node, *args, **kwargs)

    def docker(*args, **kwargs):
        # Reused checks only restart/stop/start this project's management
        # services. Reject accidental references to any other container.
        if args[0] not in ("restart", "stop", "start") or any(not n.startswith(manage.PROJECT+"-") for n in args[1:]):
            raise RuntimeError("unexpected Docker mutation in acceptance check")
        # The historical acceptance helper restarts Gateway independently.
        # In the default bundle that means restarting its owning Web container.
        args = tuple(manage.PROJECT+"-web" if name == manage.PROJECT+"-gateway" else name for name in args)
        for name in args[1:]:
            manage.owned(name[len(manage.PROJECT)+1:])
        return manage.run("docker", *args, **kwargs)

    lab.exe, lab.docker = exe, docker
    lab.save("protected-before.json", manage.unrelated_snapshot())
    try:
        manage.verify_clean()
        if not all(manage.running(node) for node in manage.LAB_SERVICES):
            raise RuntimeError("Use manage.py lab-up before the EVPN traffic suite; core deployment has no test PCs")
        lab.passed("5 core containers plus 2 test PCs; Controller-local etcd, Agent-local Redis and bundled Web")
        nodes = manage.wait_ready()
        for n in nodes:
            lab.request("/nodes/"+n["nodeId"]+"/adopt", "POST", dict(expectedRevision="0", allowExisting=False))
        lab.passed("three source-built Alpine Agents registered and explicitly adopted via Web Nginx/Gateway")
        config = json.loads((manage.STATE / "evpn.json").read_text())
        manage.hosts()
        initial, replay = lab.submit(config)
        lab.save("initial-job.json", initial)
        lab.passed("source-built Web -> Gateway -> Controller -> Agent provisions reference symmetric IRB topology")
        lab.forwarding()
        lab.verify(config, initial, replay)
        lab.disconnect_verify()
        manage.bundled_check()
        manage.verify_unrelated()
        versions = {}
        for node in ("spine", "leaf1", "leaf2"):
            versions[node] = dict(alpine=exe(node, "cat", "/etc/alpine-release").stdout.strip(),
                                 frr=exe(node, "vtysh", "-c", "show version").stdout,
                                 redis=exe(node, "redis-server", "--version").stdout.strip(),
                                 packages=exe(node, "apk", "info", "-v", "frr", "frr-pythontools").stdout)
        versions["controller"] = dict(etcd=exe("controller", "etcd", "--version").stdout)
        lab.save("environment.json", dict(time=time.strftime("%Y-%m-%dT%H:%M:%S%z"), reference="vxlan-s-evpn",
                                          database_topology="embedded", versions=versions))
        print("PASS functional suite. Leave provisioned for browser tests; then run manage.py reset.", flush=True)
    except Exception as e:
        lab.RESULTS.append(dict(name="docker integration", status="FAIL", error=str(e)))
        lab.save(lab.RESULT_FILE, lab.RESULTS)
        raise


if __name__ == "__main__":
    main()
