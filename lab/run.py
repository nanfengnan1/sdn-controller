#!/usr/bin/env python3
"""Isolated real EVPN integration, configured only through the Web HTTP API.

Only exact label sdn.integration=sdnc-it is owned by this script. Existing
sevpn-* and sdna-e2e-* labs are never changed. Keep the successful lab running.
"""
import argparse
import copy
import json
import os
from pathlib import Path
import re
import secrets
import subprocess
import time
import urllib.error
import urllib.request
import uuid

ROOT=Path(__file__).resolve().parents[1]
AGENT=ROOT.parent/"sdn-agent"
WEB=ROOT.parent/"sdn-web"
STATE=ROOT/"lab/state"
ART=ROOT/"artifacts"
PREFIX="sdnc-it"
LABEL="sdn.integration="+PREFIX
NETWORK=PREFIX+"-mgmt"
FRR="quay.io/frrouting/frr:10.0.0"
BASE="http://127.0.0.1:18080/api/v1"
RESULTS=[]
RESULT_FILE="integration-results.json"
TOKENS={}

def run(*args,check=True,timeout=120,**kw):
    p=subprocess.run([str(x) for x in args],text=True,capture_output=True,timeout=timeout,**kw)
    if check and p.returncode:
        # Arguments may contain credentials. Report output only, never argv.
        raise RuntimeError(f"process failed ({p.returncode})\n{p.stdout}\n{p.stderr}")
    return p
def docker(*args,**kw): return run("docker",*args,**kw)
def name(n): return PREFIX+"-"+n
def exe(n,*args,**kw): return docker("exec",name(n),*args,**kw)
def save(filename,value):
    ART.mkdir(exist_ok=True)
    (ART/filename).write_text(value if isinstance(value,str) else json.dumps(value,indent=2),encoding="utf-8")
def passed(check):
    RESULTS.append(dict(name=check,status="PASS"))
    save(RESULT_FILE,RESULTS)
    print("PASS",check,flush=True)
def wait(fn,message,seconds=100):
    deadline=time.monotonic()+seconds;last=""
    while time.monotonic()<deadline:
        try:
            value=fn()
            if value:return value
        except Exception as e:last=str(e)
        time.sleep(1)
    raise RuntimeError("timeout: "+message+"\n"+last[-2500:])
def request(path,method="GET",body=None,role="admin",key=None,expected=200,raw=False):
    headers={}
    if role:headers["Authorization"]="Bearer "+TOKENS[role]
    data=None
    if body is not None:data=json.dumps(body).encode();headers["Content-Type"]="application/json"
    if key:headers["Idempotency-Key"]=key
    q=urllib.request.Request(BASE+path,data=data,headers=headers,method=method)
    try:
        with urllib.request.urlopen(q,timeout=40) as r:status=r.status;data=r.read().decode()
    except urllib.error.HTTPError as e:status=e.code;data=e.read().decode()
    if status!=expected:raise RuntimeError(f"HTTP {method} {path}: expected {expected}, got {status}: {data[:4000]}")
    return data if raw else json.loads(data)
def container(n,image,*cmd,extra=()):
    return docker("run","-d","--name",name(n),"--label",LABEL,"--network",NETWORK,"--network-alias",n,*extra,image,*cmd).stdout.strip()
def protected():
    ids=docker("ps","-aq","--filter","name=sevpn-","--filter","name=sdna-e2e-").stdout.split();out={}
    if ids:
        records=docker("inspect","--format","{{.Name}}|{{.Id}}|{{.State.StartedAt}}|{{.State.Running}}",*ids).stdout
        for line in records.splitlines():
            n,ident,started,running=line.split("|");n=n.lstrip("/")
            if n.startswith(("sevpn-","sdna-e2e-")):out[n]=dict(id=ident,started=started,running=running=="true")
    return out
def cleanup():
    ids=docker("ps","-aq","--filter","label="+LABEL).stdout.split()
    if ids:docker("rm","-f",*ids)
    net=docker("network","inspect",NETWORK,check=False)
    if net.returncode==0 and json.loads(net.stdout)[0].get("Labels",{}).get("sdn.integration")==PREFIX:docker("network","rm",NETWORK)
def wire(a,an,b,bn,i):
    pa=docker("inspect","-f","{{.State.Pid}}",name(a)).stdout.strip()
    pb=docker("inspect","-f","{{.State.Pid}}",name(b)).stdout.strip()
    x,y=f"sdnci{i}a",f"sdnci{i}b"
    exe("helper","ip","link","add",x,"type","veth","peer","name",y)
    for endpoint,pid,ifname in [(x,pa,an),(y,pb,bn)]:
        exe("helper","ip","link","set",endpoint,"netns",pid)
        exe("helper","nsenter","-t",pid,"-n","ip","link","set",endpoint,"name",ifname)
def start_agent(n):
    exe(n,"sh","-c",f"/agent/bin/sdn-agent -insecure -controller controller:19090 -endpoint {n}:9090 -redis redis:6379 -state-dir /node/agent > /node/agent.log 2>&1 & echo $! > /node/agent.pid")
def setup():
    global TOKENS
    if docker("ps","-aq","--filter","label="+LABEL).stdout.strip():raise RuntimeError("Owned lab already exists; use explicit --cleanup first")
    STATE.mkdir(parents=True,exist_ok=True);ART.mkdir(exist_ok=True)
    (STATE/"go.mod").write_text("module integration-runtime-state\n\ngo 1.25.0\n")
    TOKENS={r:secrets.token_hex(24) for r in ["admin","viewer","agent"]}
    (STATE/"tokens.json").write_text(json.dumps(TOKENS));os.chmod(STATE/"tokens.json",0o600)
    (STATE/"controller.env").write_text("\n".join("SDN_"+r.upper()+"_TOKEN="+v for r,v in TOKENS.items())+"\n")
    (STATE/"agent.env").write_text("SDN_TOKEN="+TOKENS["agent"]+"\n")
    (STATE/"gateway.env").write_text("SDN_ADMIN_TOKEN="+TOKENS["admin"]+"\nSDN_VIEWER_TOKEN="+TOKENS["viewer"]+"\n")
    for f in ["controller.env","agent.env","gateway.env"]:os.chmod(STATE/f,0o600)
    save("protected-before.json",protected())
    docker("network","create","--label",LABEL,NETWORK)
    stamp=str(time.time_ns())
    for n in ["etcd","redis"]:(STATE/(n+"-"+stamp)).mkdir()
    container("etcd","quay.io/coreos/etcd:v3.6.7","/usr/local/bin/etcd","--name","sdnc-it","--data-dir","/etcd-data","--listen-client-urls","http://0.0.0.0:2379","--advertise-client-urls","http://etcd:2379",extra=("-v",f"{STATE/('etcd-'+stamp)}:/etcd-data","-p","127.0.0.1:12380:2379"))
    container("redis","redis:7.4-alpine","redis-server","--appendonly","yes","--appendfsync","always",extra=("-v",f"{STATE/('redis-'+stamp)}:/data"))
    wait(lambda:exe("etcd","etcdctl","--endpoints=http://127.0.0.1:2379","endpoint","health",check=False).returncode==0,"etcd")
    container("controller",FRR,"/controller/bin/sdn-controller","-listen",":19090","-etcd","http://etcd:2379","-etcd-prefix","/sdn/it/controller","-insecure",extra=("--entrypoint","","--env-file",str(STATE/"controller.env"),"-v",f"{ROOT}:/controller:ro","-p","127.0.0.1:19090:19090"))
    container("gateway",FRR,"/web/bin/sdn-web-gateway","-listen","0.0.0.0:18080","-controller","controller:19090","-static-dir","/web/ui/apps/web-ele/dist","-insecure",extra=("--entrypoint","","--env-file",str(STATE/"gateway.env"),"-v",f"{WEB}:/web:ro","-p","18080:18080"))
    wait(lambda:request("/desired"),"Web Gateway/Controller")
    container("helper",FRR,"sleep","infinity",extra=("--privileged","--pid=host","--entrypoint",""))
    daemons=docker("run","--rm","--entrypoint","cat",FRR,"/etc/frr/daemons").stdout.replace("bgpd=no","bgpd=yes").replace("ospfd=no","ospfd=yes")
    for n in ["spine","leaf1","leaf2"]:
        d=STATE/(n+"-"+stamp);d.mkdir();conf=d/"frr";conf.mkdir();os.chmod(conf,0o777)
        (conf/"daemons").write_text(daemons)
        (conf/"frr.conf").write_text(f"frr defaults traditional\nhostname {n}\nlog stdout informational\nservice integrated-vtysh-config\n");os.chmod(conf/"frr.conf",0o666)
        (conf/"vtysh.conf").write_text("service integrated-vtysh-config\n")
        container(n,FRR,extra=("--privileged","--env-file",str(STATE/"agent.env"),"-v",f"{AGENT}:/agent:ro","-v",f"{d}:/node","-v",f"{conf}:/etc/frr"))
    for n in ["pc1","pc2"]:container(n,FRR,"sleep","infinity",extra=("--privileged","--entrypoint",""))
    wire("spine","swp1","leaf1","uplink",1);wire("spine","swp2","leaf2","uplink",2)
    wire("leaf1","host10","pc1","eth1",3);wire("leaf2","host20","pc2","eth1",4)
    for i in [1,2]:
        n=f"pc{i}";seg=i*10
        exe(n,"ip","link","set","eth1","address",f"02:00:00:{seg}:00:{10+i}","mtu","1500","up")
        exe(n,"ip","address","add",f"192.168.{seg}.{10+i}/24","dev","eth1")
        exe(n,"ip","route","add","192.168.0.0/16","via",f"192.168.{seg}.254","dev","eth1")
        exe(n,"sysctl","-w","net.ipv4.conf.all.rp_filter=0")
    for n in ["spine","leaf1","leaf2"]:start_agent(n)
    def agents_ready():
        items=request("/nodes")["items"]
        return len(items)==3 and all(n["state"]=="RUNNING" for n in items)
    wait(agents_ready,"three registered and serving nodes",40)
    nodes=request("/nodes");save("nodes-initial.json",nodes)
    for n in nodes["items"]:request("/nodes/"+n["nodeId"]+"/adopt","POST",dict(expectedRevision="0",allowExisting=False))
    config=json.loads(run("python3",ROOT/"examples/render_lab.py",ART/"nodes-initial.json").stdout)
    (STATE/"evpn.json").write_text(json.dumps(config,indent=2))
    passed("three real Agents registered and explicitly adopted via HTTP")
    return config
def candidate(config=None):
    current=request("/desired")
    c=request("/changes","POST",dict(baseRevision=current["revision"]),key=str(uuid.uuid4()),expected=201)
    if config is not None:c=request("/changes/"+c["id"],"PUT",dict(editRevision=c["editRevision"],configuration=config))
    plan=request("/changes/"+c["id"]+"/plan","POST")
    return c,plan
def commit(c,p,expected_state="SUCCEEDED"):
    key=str(uuid.uuid4());body=dict(planId=p["id"])
    j=request("/changes/"+c["id"]+"/commit","POST",body,key=key,expected=202)
    terminal=["SUCCEEDED","FAILED","PARTIAL","ROLLED_BACK","COMPENSATION_FAILED"]
    def done():
        out=request("/jobs/"+j["id"])
        return out if out["state"] in terminal else None
    result=wait(done,"job "+j["id"],240)
    save("job-"+j["id"]+".json",result)
    if result["state"]!=expected_state:
        brief={k:result.get(k) for k in ["id","state","error","nodes"]}
        raise RuntimeError("unexpected job result (full record saved): "+json.dumps(brief)[:3500])
    return result,dict(change=c["id"],body=body,key=key)
def submit(config,expected_state="SUCCEEDED"):return commit(*candidate(config),expected_state=expected_state)
def forwarding():
    def established():
        s=json.loads(exe("spine","vtysh","-c","show bgp l2vpn evpn summary json").stdout)
        peers=s.get("peers",s.get("l2VpnEvpn",{}).get("peers",{}))
        return len(peers)==2 and all(p.get("state")=="Established" for p in peers.values())
    wait(established,"two BGP EVPN sessions")
    passed("both Spine/Leaf BGP EVPN sessions Established")
    for i in [1,2]:wait(lambda i=i:exe(f"pc{i}","ping","-c","1","-W","1",f"192.168.{i*10}.254",check=False).returncode==0,"host learning",30)
    for a,b in [("pc1","192.168.20.12"),("pc2","192.168.10.11")]:
        wait(lambda:exe(a,"ping","-c","2","-W","1",b,check=False).returncode==0,"cross-leaf ping")
        save(a+"-ping.txt",exe(a,"ping","-c","3","-W","1",b).stdout)
    passed("bidirectional PC traffic across L2VNI 10010 / L3VNI 50000 / L2VNI 10020")
    routes=exe("spine","vtysh","-c","show bgp l2vpn evpn").stdout
    assert all(x in routes for x in ["[2]:","[3]:","192.168.10.11","192.168.20.12"]),routes
    save("spine-evpn.txt",routes)
    save("spine-evpn.json",exe("spine","vtysh","-c","show bgp l2vpn evpn json").stdout)
    passed("Spine learns both host Type-2 MAC/IP and Type-3 IMET")
    for n in ["leaf1","leaf2"]:
        v=exe(n,"vtysh","-c","show evpn vni").stdout;assert "50000" in v and "L3" in v;save(n+"-vni.txt",v)
    passed("Zebra identifies 50000 as L3VNI on both Leafs")
def verify(config,initial,replay):
    request("/nodes",role=None,expected=401)
    request("/changes","POST",dict(baseRevision="0"),role="viewer",key=str(uuid.uuid4()),expected=403)
    assert len(request("/nodes",role="viewer")["items"])==3
    passed("HTTP unauthenticated 401 and viewer read/write separation")
    again=request("/changes/"+replay["change"]+"/commit","POST",replay["body"],key=replay["key"],expected=202)
    assert again["id"]==initial["id"]
    passed("duplicate Commit returns original persisted Job")
    events=request("/jobs/"+initial["id"]+"/events?afterSequence=0",raw=True)
    seq=[int(s[4:]) for s in events.splitlines() if s.startswith("id: ")]
    assert seq and seq==sorted(set(seq)) and seq[-1]==int(initial["sequence"])
    save("job-events.sse",events)
    passed("SSE replays ordered events and closes at terminal state")
    top=request("/topology?refresh=true");save("topology.json",top)
    assert {2,3}.issubset({r["type"] for r in top["evpnRoutes"]})
    assert any(r.get("ip")=="192.168.10.11" for r in top["evpnRoutes"])
    assert any(n["kind"]=="host" for n in top["nodes"])
    passed("Controller normalizes real Type-2/3 into observed VTEP/host topology")
    ca,pa=candidate(config);cb,pb=candidate(config)
    no_op,_=commit(ca,pa);assert not no_op["nodes"]
    request("/changes/"+cb["id"]+"/commit","POST",dict(planId=pb["id"]),key=str(uuid.uuid4()),expected=409)
    passed("two candidates conflict by revision; noop does not reapply nodes")
    temporary=copy.deepcopy(config);leaf=next(n["nodeId"] for n in config["nodes"] if n["name"]=="leaf1")
    temporary["tenants"].append(dict(id="test-tenant",name="TENANT-T",fabricId="lab",l3vni=50100,importRts=["65000:99"],exportRts=["65000:99"],placements=[dict(nodeId=leaf,tableId=1099,routerMac="02:00:00:00:fe:11",bridge="br50100",vxlan="vx50100",rd="10.255.0.11:50100")]))
    temporary["networks"].append(dict(id="test-net",tenantId="test-tenant",l2vni=10099,importRts=["65000:10099"],exportRts=["65000:10099"],placements=[dict(nodeId=leaf,bridge="br99",vxlan="vx10099",mac="02:00:00:00:00:99",gateways=["192.168.99.254/24"],ports=[],rd="10.255.0.11:99")]))
    submit(temporary)
    assert exe("leaf1","ip","link","show","TENANT-T",check=False).returncode==0
    submit(config)
    for n in ["TENANT-T","br50100","vx50100","br99","vx10099"]:assert exe("leaf1","ip","link","show",n,check=False).returncode!=0
    running=exe("leaf1","vtysh","-c","show running-config").stdout
    assert all(value not in running for value in ["TENANT-T","50100","10099","65000:99"]),running
    passed("HTTP resource create/delete removes temporary VRF/BD/VXLAN and FRR references")
    broken=copy.deepcopy(config);n=next(n for n in broken["nodes"] if n["nodeId"]==leaf)
    next(i for i in n["interfaces"] if i["name"]=="uplink")["mtu"]=8900
    n["interfaces"].append(dict(name="missing0",mtu=1500))
    recovery_started=time.monotonic()
    failure,_=submit(broken,"FAILED")
    assert any(n["state"]=="FAILED_ROLLED_BACK" for n in failure["nodes"]),failure
    assert json.loads(exe("leaf1","ip","-j","link","show","uplink").stdout)[0]["mtu"]==9000
    submit(config)
    passed("real Agent failure rolls MTU back; Controller records failure and accepts explicit corrected target")
    # Changing an OSPF-facing MTU can reset adjacency. Configuration rollback
    # is not a hitless forwarding guarantee; establish convergence before the
    # independent management-process restart check.
    wait(lambda:exe("pc1","ping","-c","3","-W","1","192.168.20.12",check=False).returncode==0,"forwarding reconvergence after MTU rollback",120)
    wait(lambda:exe("pc2","ping","-c","3","-W","1","192.168.10.11",check=False).returncode==0,"reverse forwarding after rollback",40)
    save("rollback-reconvergence.json",dict(seconds=round(time.monotonic()-recovery_started,2),semantics="configuration rollback followed by independent protocol/traffic convergence; not hitless"))
    passed("forwarding reconverges after OSPF-facing MTU rollback before management restart test")
    before=request("/desired");nodes=request("/nodes")
    docker("restart",name("controller"));wait(lambda:request("/desired"),"Controller restart",40)
    assert request("/desired")==before
    assert request("/jobs/"+initial["id"])["state"]=="SUCCEEDED"
    assert {n["nodeId"] for n in request("/nodes")["items"]}=={n["nodeId"] for n in nodes["items"]}
    docker("restart",name("gateway"));wait(lambda:request("/desired"),"Gateway restart",30)
    assert request("/jobs/"+initial["id"])["state"]=="SUCCEEDED"
    assert exe("pc1","ping","-c","3","-W","1","192.168.20.12").returncode==0
    passed("Controller and Gateway restart preserve etcd configuration, identity, Job and forwarding")
    assert protected()==json.loads((ART/"protected-before.json").read_text())
    passed("existing sevpn-* and sdna-e2e-* container identities and start times unchanged")
    save("topology.json",request("/topology?refresh=true"))
    save("nodes-final.json",request("/nodes"));save("desired-final.json",request("/desired"))
    for n in ["spine","leaf1","leaf2"]:save(n+"-running.conf",exe(n,"vtysh","-c","show running-config").stdout)

def disconnect_verify():
    before=request("/desired")
    docker("stop",name("controller"))
    try:
        def down():
            state=json.loads(exe("leaf1","/agent/bin/sdnctl","-insecure","-addr","127.0.0.1:9090","state").stdout)
            return state.get("node_state")=="DOWN"
        wait(down,"Agent reports management DOWN",20)
        for pc,dst in [("pc1","192.168.20.12"),("pc2","192.168.10.11")]:
            output=exe(pc,"ping","-c","3","-W","1",dst).stdout
            assert re.search(r"\b0% packet loss",output),output
            save(pc+"-controller-down-ping.txt",output)
    finally:
        docker("start",name("controller"))
    wait(lambda:request("/desired"),"Controller restored",40)
    wait(lambda:all(n["state"]=="RUNNING" for n in request("/nodes")["items"]),"management RUNNING",30)
    assert request("/desired")==before
    passed("Controller stopped: Agent reports DOWN, both traffic directions have zero loss, management recovers")

def main():
    global TOKENS,RESULTS,RESULT_FILE
    p=argparse.ArgumentParser();p.add_argument("--cleanup",action="store_true");p.add_argument("--verify-only",action="store_true");p.add_argument("--disconnect-check",action="store_true");a=p.parse_args()
    if a.cleanup:cleanup();return
    if a.verify_only:
        RESULT_FILE="forwarding-results.json"
        TOKENS=json.loads((STATE/"tokens.json").read_text());forwarding();return
    if a.disconnect_check:
        TOKENS=json.loads((STATE/"tokens.json").read_text())
        RESULTS=json.loads((ART/"integration-results.json").read_text())
        disconnect_verify();return
    config=setup();initial,replay=submit(config);save("initial-job.json",initial)
    passed("HTTP Gateway -> Controller -> Agent provisions complete Linux/FRR EVPN fabric")
    forwarding();verify(config,initial,replay);disconnect_verify()
    print("Integration complete; demo remains running at http://10.8.126.103:18080",flush=True)
if __name__=="__main__":
    try:main()
    except Exception as e:
        RESULTS.append(dict(name="integration execution",status="FAIL",error=str(e)))
        save(RESULT_FILE,RESULTS)
        raise
