#!/usr/bin/env python3
"""Own only the sdn-docker Compose project. Never call another lab's scripts."""
import argparse
import json
import os
from pathlib import Path
import secrets
import subprocess
import sys
import time
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
HERE = ROOT / "docker"
STATE = HERE / "state"
CONFIG = STATE / "config"
ART = ROOT / "artifacts" / "docker"
PROJECT = "sdn-docker"
SERVICES = ("controller", "web", "spine", "leaf1", "leaf2")
LAB_SERVICES = ("pc1", "pc2")
BUILD_SERVICES = ("controller", "spine", "web")
LEGACY_DATABASES = ("etcd", "redis-spine", "redis-leaf1", "redis-leaf2")
COMPOSE = ["docker", "compose", "--project-name", PROJECT, "--env-file", str(HERE / ".env"), "-f", str(HERE / "compose.yaml")]
LINKS = [("spine", "swp1", "leaf1", "uplink"), ("spine", "swp2", "leaf2", "uplink"),
         ("leaf1", "host10", "pc1", "eth0"), ("leaf2", "host20", "pc2", "eth0")]


def run(*args, check=True, timeout=180, **kwargs):
    p = subprocess.run([str(x) for x in args], capture_output=True, text=True, timeout=timeout, **kwargs)
    if check and p.returncode:
        # Do not include argv: some Docker operations may carry credentials.
        raise RuntimeError(p.stdout + p.stderr)
    return p


def compose(*args, **kwargs):
    return run(*COMPOSE, *args, **kwargs)


def ensure_configs():
    """Generate missing bind-mounted config files; preserve user overrides."""
    created = []
    for node in ("controller", "spine", "leaf1", "leaf2"):
        role = "controller" if node == "controller" else "agent"
        target = CONFIG / node / (role + ".conf")
        if target.exists():
            if not target.is_file():
                raise RuntimeError("Expected configuration file: " + str(target))
            # Validate JSON without echoing content, which may contain credentials.
            with target.open() as f:
                value = json.load(f)
            if not isinstance(value, dict):
                raise RuntimeError("Configuration must be a JSON object: " + str(target))
            continue
        template = ROOT.parent / ("sdn-" + role) / "docker" / (role + ".conf")
        with template.open() as f:
            value = json.load(f)
        if role == "controller":
            value["controller"].update(listen=":19090", etcd="http://127.0.0.1:2379",
                                       etcdPrefix="/sdn/docker/controller", insecure=True)
            value["etcd"].update(enabled=True, name=PROJECT, dataDir="/etcd-data")
        else:
            value["agent"].update(listen=":9090", endpoint=node+":9090", controller="controller:19090",
                                  stateDir="/var/lib/sdn-agent", redis="127.0.0.1:6379", frr=True, insecure=True)
            value["redis"].update(enabled=True, bind="127.0.0.1", port=6379,
                                  dataDir="/var/lib/redis", appendOnly=True, appendFsync="always")
        target.parent.mkdir(parents=True, exist_ok=True)
        os.chmod(target.parent, 0o700)
        # O_EXCL also refuses an unexpected dangling symlink at the target.
        with os.fdopen(os.open(target, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), "w") as f:
            json.dump(value, f, indent=2)
            f.write("\n")
        created.append(str(target.relative_to(STATE)))
    return created


def initialize():
    STATE.mkdir(parents=True, exist_ok=True)
    os.chmod(STATE, 0o700)
    envfile = HERE / ".env"
    if not envfile.exists():
        values = {"SDN_"+role.upper()+"_TOKEN": secrets.token_hex(24) for role in ("admin", "viewer", "agent")}
        values.update(SDN_WEB_PORT="28080", SDN_GRPC_PORT="29090", SDN_WEB_BIND="0.0.0.0")
        with os.fdopen(os.open(envfile, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), "w") as f:
            f.write("".join(k+"="+v+"\n" for k, v in values.items()))
    values = dict(line.split("=", 1) for line in envfile.read_text().splitlines() if line and not line.startswith("#"))
    tokens = {role: values["SDN_"+role.upper()+"_TOKEN"] for role in ("admin", "viewer", "agent")}
    (STATE / "tokens.json").write_text(json.dumps(tokens, indent=2))
    os.chmod(STATE / "tokens.json", 0o600)
    (STATE / "go.mod").write_text("module docker-runtime-state\n\ngo 1.25.0\n")
    ensure_configs()
    ART.mkdir(parents=True, exist_ok=True)
    return values, tokens


def owned(node):
    name = PROJECT+"-"+node
    info = json.loads(run("docker", "inspect", name).stdout)[0]
    if info["Config"].get("Labels", {}).get("com.docker.compose.project") != PROJECT:
        raise RuntimeError("Refusing unrelated container: "+name)
    return info


def validate_owned_resources():
    # Check expected names, including collisions with resources that do NOT
    # carry our label. Filtering only by our label would miss such collisions.
    expected = set(SERVICES + LAB_SERVICES + LEGACY_DATABASES + ("helper", "gateway"))
    names = run("docker", "ps", "-a", "--filter", "label=com.docker.compose.project="+PROJECT,
                "--format", "{{.Names}}").stdout.split()
    unexpected = [name for name in names if name not in {PROJECT+"-"+node for node in expected}]
    if unexpected:
        raise RuntimeError("Refusing unknown containers with this project label: " + ", ".join(unexpected))
    for node in expected:
        if run("docker", "inspect", PROJECT+"-"+node, check=False).returncode == 0:
            owned(node)
    for volume in compose("config", "--volumes").stdout.split():
        present = run("docker", "volume", "inspect", PROJECT+"_"+volume, check=False)
        if present.returncode == 0:
            info = json.loads(present.stdout)[0]
            if info.get("Labels", {}).get("com.docker.compose.project") != PROJECT:
                raise RuntimeError("Refusing unrelated volume: "+info["Name"])
    present = run("docker", "network", "inspect", PROJECT+"_management", check=False)
    if present.returncode == 0:
        info = json.loads(present.stdout)[0]
        if info.get("Labels", {}).get("com.docker.compose.project") != PROJECT:
            raise RuntimeError("Refusing unrelated network: "+info["Name"])


def retire_legacy_databases():
    """Remove only stopped, owned obsolete DB containers, keeping all volumes."""
    obsolete = []
    for node in LEGACY_DATABASES:
        present = run("docker", "inspect", PROJECT+"-"+node, check=False)
        if present.returncode:
            continue
        info = owned(node)
        if info["State"]["Running"]:
            raise RuntimeError("Stop the old database container before migration: " + PROJECT+"-"+node)
        obsolete.append(info["Id"])
    if obsolete:
        run("docker", "rm", *obsolete)
        print("Removed stopped legacy database containers; existing database volumes were retained.")


def exe(node, *args, **kwargs):
    owned(node)
    return run("docker", "exec", PROJECT+"-"+node, *args, **kwargs)


def running(node):
    present = run("docker", "inspect", PROJECT+"-"+node, check=False)
    if present.returncode:
        return False
    return bool(owned(node)["State"]["Running"])


def stop_lab_hosts():
    active = [owned(node)["Id"] for node in LAB_SERVICES if running(node)]
    if active:
        run("docker", "stop", "--time", "10", *active, timeout=40)


def retire_legacy_gateway():
    if run("docker", "inspect", PROJECT+"-gateway", check=False).returncode:
        return
    old = owned("gateway")
    if old["State"]["Running"]:
        run("docker", "stop", "--time", "30", old["Id"], timeout=45)
    run("docker", "rm", old["Id"])
    print("Retired the owned standalone Gateway; Web now contains both processes.")


def unrelated_snapshot():
    ids = run("docker", "ps", "-aq").stdout.split()
    out = {}
    if not ids:
        return out
    # Read only identity/state metadata, never dump unrelated containers' env.
    # This also keeps failure output safe if a container disappears mid-snapshot.
    fmt = ('{"name":{{json .Name}},"id":{{json .Id}},"started":{{json .State.StartedAt}},'
           '"running":{{json .State.Running}},"project":{{json (index .Config.Labels "com.docker.compose.project")}}}')
    for line in run("docker", "inspect", "--format", fmt, *ids).stdout.splitlines():
        info = json.loads(line)
        if info.get("project") == PROJECT:
            continue
        out[info["name"].lstrip("/")] = {key: info[key] for key in ("id", "started", "running")}
    return out


def api(path, method="GET", body=None):
    values, tokens = initialize()
    data = None if body is None else json.dumps(body).encode()
    q = urllib.request.Request("http://127.0.0.1:"+values["SDN_WEB_PORT"]+"/api/v1"+path,
        method=method, data=data, headers={"Authorization": "Bearer "+tokens["admin"], "Content-Type": "application/json"})
    with urllib.request.urlopen(q, timeout=15) as r:
        return json.load(r)


def wait_ready():
    deadline = time.monotonic()+150
    last = ""
    while time.monotonic() < deadline:
        try:
            nodes = api("/nodes")["items"]
            if len(nodes) == 3 and all(n["state"] == "RUNNING" for n in nodes):
                return nodes
        except Exception as e:
            last = str(e)
        time.sleep(1)
    raise RuntimeError("services not ready: "+last)


def wire():
    # Check both ends before doing anything; an incomplete pair requires an
    # explicit reset, never delete an existing device to make a script pass.
    missing = []
    links = LINKS if all(running(node) for node in LAB_SERVICES) else LINKS[:2]
    for a, ap, b, bp in links:
        ae = exe(a, "ip", "link", "show", "dev", ap, check=False).returncode == 0
        be = exe(b, "ip", "link", "show", "dev", bp, check=False).returncode == 0
        if ae != be:
            raise RuntimeError("incomplete cable "+a+":"+ap+"; reset only this project")
        if not ae:
            missing.append((a, ap, b, bp))
    if missing:
        compose("--profile", "tools", "up", "-d", "--no-build", "helper")
        try:
            for a, ap, b, bp in missing:
                pa, pb = owned(a)["State"]["Pid"], owned(b)["State"]["Pid"]
                if not pa or not pb:
                    raise RuntimeError("target node is stopped")
                exe("helper", "ip", "link", "add", ap, "netns", str(pa), "type", "veth", "peer", "name", bp, "netns", str(pb))
        finally:
            owned("helper")
            compose("--profile", "tools", "rm", "-sf", "helper")


def render_example(nodes):
    (STATE / "nodes.json").write_text(json.dumps({"items": nodes}, indent=2))
    example = run(sys.executable, ROOT / "examples/render_lab.py", STATE / "nodes.json").stdout
    (STATE / "evpn.json").write_text(example)


def hosts():
    """Explicit opt-in PC configuration; up/reset never configure addresses."""
    for index in (1, 2):
        node, seg = "pc"+str(index), index*10
        exe(node, "ip", "link", "set", "lo", "up")
        for key in ("net.ipv4.conf.all.rp_filter", "net.ipv4.conf.default.rp_filter"):
            exe(node, "sysctl", "-w", key+"=0")
        exe(node, "sysctl", "-w", "net.ipv6.conf.all.disable_ipv6=1")
        exe(node, "ip", "link", "set", "eth0", "address", f"02:00:00:{seg}:00:{10+index}", "mtu", "1500", "up")
        exe(node, "ip", "address", "replace", f"192.168.{seg}.{10+index}/24", "dev", "eth0")
        exe(node, "ip", "route", "replace", "default", "via", f"192.168.{seg}.254", "dev", "eth0")
    print("Configured only sdn-docker-pc1/pc2 reference host addresses and default routes.")


def up(lab=False):
    initialize()
    validate_owned_resources()
    retire_legacy_databases()
    retire_legacy_gateway()
    if not lab:
        stop_lab_hosts()
    if not (ART / "unrelated-before.json").exists():
        (ART / "unrelated-before.json").write_text(json.dumps(unrelated_snapshot(), indent=2))
    selected = SERVICES + (LAB_SERVICES if lab else ())
    profiles = ("--profile", "lab") if lab else ()
    compose(*profiles, "up", "-d", "--no-build", "--wait", "--wait-timeout", "150", *selected, timeout=200)
    wire()
    nodes = wait_ready()
    render_example(nodes)
    print("Ready: "+str(len(selected))+" containers (5 core"+(" + 2 test PCs" if lab else "")+"); local databases, bundled Web and "+str(4 if lab else 2)+" cables; no business configuration was provisioned.")


def bundled_check():
    """Read-only verification of process co-location and config/volume mounts."""
    validate_owned_resources()
    names = run("docker", "ps", "--filter", "label=com.docker.compose.project="+PROJECT,
                "--format", "{{.Names}}").stdout.split()
    pcs = [node for node in LAB_SERVICES if running(node)]
    if pcs and len(pcs) != len(LAB_SERVICES):
        raise RuntimeError("Incomplete optional PC pair; use lab-up or stop both PCs")
    expected = {PROJECT+"-"+node for node in SERVICES+tuple(pcs)}
    if set(names) != expected:
        raise RuntimeError("Expected 5 core containers and optional PC pair; found: " + ", ".join(names))
    for node in LEGACY_DATABASES:
        if run("docker", "inspect", PROJECT+"-"+node, check=False).returncode == 0:
            raise RuntimeError("Legacy standalone database container still exists: " + node)
    results = {}
    for node in ("controller", "spine", "leaf1", "leaf2"):
        role = "controller" if node == "controller" else "agent"
        config_path = "/etc/sdn-"+role+"/"+role+".conf"
        host_path = CONFIG / node / (role+".conf")
        config = json.loads(host_path.read_text())
        info = owned(node)
        if info.get("State", {}).get("Health", {}).get("Status") != "healthy":
            raise RuntimeError("Bundled service is not healthy: " + node)
        mounts = {m["Destination"]: m for m in info.get("Mounts", [])}
        mounted = mounts.get(config_path, {})
        if mounted.get("Type") != "bind" or mounted.get("RW", True) or Path(mounted.get("Source", "")).resolve() != host_path.resolve():
            raise RuntimeError("Missing read-only host configuration mount: " + node)
        if exe(node, "cat", config_path).stdout != host_path.read_text():
            raise RuntimeError("Container config differs from host file: " + node)
        if role == "controller":
            port = config["etcd"].get("clientPort", 2379)
            exe(node, "etcdctl", "--endpoints=http://127.0.0.1:"+str(port), "endpoint", "health")
            directory, volume = config["etcd"].get("dataDir", "/etcd-data"), "etcd-data"
        else:
            port = config["redis"].get("port", 6379)
            if exe(node, "redis-cli", "-h", config["redis"].get("bind", "127.0.0.1"), "-p", str(port), "ping").stdout.strip() != "PONG":
                raise RuntimeError("Bundled Redis did not return PONG: " + node)
            exe(node, "vtysh", "-c", "show version")
            directory, volume = config["redis"].get("dataDir", "/var/lib/redis"), "redis-"+node
        if mounts.get(directory, {}).get("Name") != PROJECT+"_"+volume:
            raise RuntimeError("Expected persistent database volume is not mounted: " + node)
        results[node] = {"healthy": True, "config_bind_read_only": True,
                         "database_local": True, "database_volume": PROJECT+"_"+volume}
    web = owned("web")
    if web.get("State", {}).get("Health", {}).get("Status") != "healthy":
        raise RuntimeError("Bundled Web is not healthy")
    exe("web", "python3", "/usr/local/lib/sdn-web/supervisor.py", "--healthcheck")
    report = {"status": "PASS", "containers": sorted(names), "services": results,
              "core_containers": 5, "test_pc_containers": len(pcs), "web_bundled": True}
    ART.mkdir(parents=True, exist_ok=True)
    (ART / "bundled-services.json").write_text(json.dumps(report, indent=2))
    print("PASS bundled services: 5 core containers"+(" + 2 test PCs" if pcs else "")+", local databases, bundled Web, read-only external configs and persistent volumes.")
    return report


def verify_unrelated():
    before = json.loads((ART / "unrelated-before.json").read_text())
    now = unrelated_snapshot()
    changed = [name for name, info in before.items() if now.get(name) != info]
    if changed:
        raise RuntimeError("other container state changed: "+", ".join(changed))
    (ART / "unrelated-after.json").write_text(json.dumps(now, indent=2))


def verify_clean():
    bundled_check()
    nodes = wait_ready()
    desired, jobs = api("/desired"), api("/jobs")
    assert str(desired["revision"]) == "0" and not any(desired["configuration"].values()), desired
    assert not jobs["items"], jobs
    assert all(not n["adopted"] and str(n["agentRevision"]) == "0" for n in nodes), nodes
    for node in ("spine", "leaf1", "leaf2"):
        state = json.loads(exe(node, "sdnctl", "-insecure", "state").stdout)
        assert str(state.get("revision", 0)) == "0", state
        cfg = exe(node, "vtysh", "-c", "show running-config").stdout
        assert not any(s in cfg for s in ("router bgp", "router ospf", "vrf TENANT", "vni 50000")), cfg
        links = json.loads(exe(node, "ip", "-j", "-d", "link", "show").stdout)
        assert not any(l.get("linkinfo", {}).get("info_kind") in ("bridge", "vxlan", "vrf") for l in links), links
        addrs = json.loads(exe(node, "ip", "-j", "addr", "show").stdout)
        assert not any(a["local"].startswith(("10.255.", "10.0.1.", "10.0.2.", "192.168.10.", "192.168.20.")) for link in addrs for a in link["addr_info"]), addrs
    for node in (name for name in LAB_SERVICES if running(name)):
        addresses = json.loads(exe(node, "ip", "-j", "addr", "show", "dev", "eth0").stdout)
        assert not addresses[0]["addr_info"], addresses
    verify_unrelated()
    result = {"status": "PASS", "nodes": nodes, "desired": desired, "jobs": jobs,
              "cables": LINKS if all(running(node) for node in LAB_SERVICES) else LINKS[:2], "manual_test_ready": True}
    (ART / "clean-state.json").write_text(json.dumps(result, indent=2))
    render_example(nodes)
    print("PASS clean services: 3 registered/unadopted nodes, revision 0, no jobs or tenant/protocol configuration.")


def reset(lab=False):
    # Compose --volumes only targets this project's non-external named volumes.
    # Validate all relevant existing resources before destructive cleanup.
    validate_owned_resources()
    compose("--profile", "tools", "--profile", "lab", "down", "--volumes", "--remove-orphans", timeout=420)
    up(lab=lab)
    verify_clean()


def main():
    p = argparse.ArgumentParser()
    p.add_argument("command", choices=["init", "build", "up", "lab-up", "lab-stop", "wire", "hosts", "status", "bundled-check", "clean-check", "reset", "lab-reset"])
    args = p.parse_args()
    initialize()
    if args.command == "init":
        print("Credentials prepared in docker/.env and docker/state/tokens.json (not printed); missing host configs generated under docker/state/config, existing configs preserved.")
    elif args.command == "build":
        # The three Agent services share one image tag/build definition.
        subprocess.run(COMPOSE+["build", *BUILD_SERVICES], check=True)
    elif args.command == "up":
        up()
    elif args.command == "lab-up":
        up(lab=True)
    elif args.command == "lab-stop":
        stop_lab_hosts()
    elif args.command == "wire":
        wire()
    elif args.command == "hosts":
        hosts()
    elif args.command == "status":
        print(compose("ps").stdout)
    elif args.command == "bundled-check":
        bundled_check()
    elif args.command == "clean-check":
        verify_clean()
    else:
        reset(lab=args.command == "lab-reset")


if __name__ == "__main__":
    main()
