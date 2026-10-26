#!/usr/bin/env python3
"""Replace example node aliases using `sdnc nodes` JSON, never configure devices."""
import argparse
import json
from pathlib import Path

parser = argparse.ArgumentParser()
parser.add_argument("nodes", type=Path)
parser.add_argument("--template", type=Path, default=Path(__file__).with_name("evpn.json"))
args = parser.parse_args()
nodes = json.loads(args.nodes.read_text())["items"]
mapping = {}
for node in nodes:
    alias = node.get("name") or node["endpoint"].split(":")[0].split("-")[-1]
    mapping[alias] = node["nodeId"]
config = json.loads(args.template.read_text())
def rewrite(value):
    if isinstance(value, dict):
        for key, item in value.items():
            if key == "nodeId":
                if item not in mapping:
                    raise ValueError(f"Missing node alias {item}; expected endpoint leaf1:9090 etc.")
                value[key] = mapping[item]
            else:
                rewrite(item)
    elif isinstance(value, list):
        for item in value:
            rewrite(item)
rewrite(config)
print(json.dumps(config, indent=2))
