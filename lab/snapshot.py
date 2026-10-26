#!/usr/bin/env python3
"""Record final non-secret evidence from the owned integration lab."""
import datetime
import json
from pathlib import Path
import run as lab

lab.TOKENS=json.loads((lab.STATE/"tokens.json").read_text())
lab.save("nodes-final.json",lab.request("/nodes"))
lab.save("desired-final.json",lab.request("/desired"))
lab.save("jobs-final.json",lab.request("/jobs"))
lab.save("topology-final-cache.json",lab.request("/topology"))
before=json.loads((lab.ART/"protected-before.json").read_text())
after=lab.protected()
lab.save("protected-after.json",after)
assert before==after,"an existing protected container changed"
records=lab.docker("ps","-a","--filter","label="+lab.LABEL,"--format","{{.Names}}|{{.Status}}").stdout
containers=[dict(name=line.split("|",1)[0],status=line.split("|",1)[1]) for line in records.splitlines()]
assert len(containers)==10 and all(c["status"].startswith("Up") for c in containers),containers
env=dict(completed_at_utc=datetime.datetime.now(datetime.timezone.utc).isoformat(),url="http://10.8.126.103:18080",containers=containers,images={})
for image in [lab.FRR,"redis:7.4-alpine","quay.io/coreos/etcd:v3.6.7"]:
    env["images"][image]=lab.docker("image","inspect","--format","{{index .RepoDigests 0}}",image).stdout.strip()
env["go"]=lab.run(Path.home()/".cache/sdn-agent-tools/go/bin/go","version").stdout.strip()
env["node"]=lab.run(Path.home()/".cache/sdn-web-tools/node_modules/node/bin/node","--version").stdout.strip()
lab.save("integration-environment.json",env)
source=json.loads((lab.ART/"regression-results.json").read_text())
integration=json.loads((lab.ART/"integration-results.json").read_text())
browser=json.loads((lab.ART/"browser-results.json").read_text())
assert all(x["exit_code"]==0 for x in source)
assert all(x["status"]=="PASS" for x in integration+browser)
summary=dict(source_stages=len(source),network_checks=len(integration),browser_checks=len(browser),status="PASS",environment="integration-environment.json")
lab.save("validation-summary.json",summary)
print(json.dumps(summary))
