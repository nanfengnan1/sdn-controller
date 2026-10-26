#!/usr/bin/env python3
"""Run source-level checks without modifying any existing network lab."""
import argparse
import json
import os
from pathlib import Path
import subprocess
import time

ROOT = Path(__file__).resolve().parents[1]
ART = ROOT / "artifacts"
ART.mkdir(exist_ok=True)
env = dict(os.environ)
env["PATH"] = ":".join([str(Path.home()/".cache/sdn-agent-tools/go/bin"), str(Path.home()/".cache/sdn-web-tools/node_modules/node/bin"), str(Path.home()/".cache/sdn-web-tools/node_modules/.bin"), env["PATH"]])
stages = [
    ("agent-race", ROOT.parent/"sdn-agent", ["go","test","-race","-count=1","./..."]),
    ("controller-race", ROOT, ["go","test","-race","-count=1","./..."]),
    ("controller-vet", ROOT, ["go","vet","./..."]),
    ("controller-build", ROOT, ["go","build","./..."]),
    ("gateway-race", ROOT.parent/"sdn-web/gateway", ["go","test","-race","-count=1","./..."]),
    ("gateway-vet", ROOT.parent/"sdn-web/gateway", ["go","vet","./..."]),
    ("gateway-build", ROOT.parent/"sdn-web/gateway", ["go","build","./..."]),
    ("ui-unit", ROOT.parent/"sdn-web/ui", ["pnpm","test"]),
    ("ui-typecheck", ROOT.parent/"sdn-web/ui", ["pnpm","typecheck"]),
    ("ui-build", ROOT.parent/"sdn-web/ui", ["pnpm","build"]),
]
if os.environ.get("SDNC_TEST_ETCD"):
    stages.append(("etcd-real",ROOT,["go","test","-race","-count=1","-v","./internal/repository"]))
parser=argparse.ArgumentParser()
parser.add_argument("--only",nargs="+",help="rerun only affected stages and retain previous unrelated results")
args=parser.parse_args()
results=[]
if args.only:
    prior=ART/"regression-results.json"
    if prior.exists():results=json.loads(prior.read_text())
    selected=set(args.only)
    if selected-set(s[0] for s in stages):raise SystemExit("unknown stage name")
    stages=[s for s in stages if s[0] in selected]
for name,cwd,cmd in stages:
    start=time.monotonic()
    with (ART/(name+".log")).open("w") as log:
        p=subprocess.run(cmd,cwd=cwd,env=env,stdout=log,stderr=subprocess.STDOUT)
    result=dict(name=name,exit_code=p.returncode,seconds=round(time.monotonic()-start,2),log=name+".log")
    results=[r for r in results if r["name"]!=name]
    results.append(result)
    (ART/"regression-results.json").write_text(json.dumps(results,indent=2))
    print(("PASS" if p.returncode==0 else "FAIL"),name,flush=True)
    if p.returncode:
        print((ART/(name+".log")).read_text()[-7000:])
        raise SystemExit(p.returncode)
