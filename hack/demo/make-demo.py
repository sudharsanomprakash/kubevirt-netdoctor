#!/usr/bin/env python3
"""Builds docs/assets/demo-diagnose.cast from a golden test output.

The demo replays the real output of the "ovs port VLAN tag mismatch (text)"
scenario in internal/cli (testdata/golden/diagnose-ovs-vlan-mismatch.txt), so
it always matches what the CLI prints. Render it with:

  hack/demo/make-demo.py
  npx svg-term-cli@2.1.1 --in docs/assets/demo-diagnose.cast \
      --out docs/assets/demo-diagnose.svg --window --padding 14
"""
import json
import math
import pathlib

root = pathlib.Path(__file__).resolve().parents[2]
out = (root / "testdata/golden/diagnose-ovs-vlan-mismatch.txt").read_text().splitlines()
cmd = "kubectl netdoctor diagnose vm/db-03 -n demo --host-snapshot worker-2.json"
width = 110
prompt = "\x1b[1;32m$\x1b[0m "

events, t = [[0.5, "o", prompt]], 0.5
for ch in cmd:
    t += 0.045
    events.append([round(t, 3), "o", ch])
t += 0.6
events.append([round(t, 3), "o", "\r\n"])
for line in out:
    t += 0.06
    events.append([round(t, 3), "o", line + "\r\n"])
t += 0.3
events.append([round(t, 3), "o", prompt])
t += 4.0
events.append([round(t, 3), "o", ""])

rows = sum(max(1, math.ceil(len(l) / width)) for l in out) + 3
header = {"version": 2, "width": width, "height": rows, "title": "kubectl netdoctor diagnose"}
dest = root / "docs/assets/demo-diagnose.cast"
dest.write_text(json.dumps(header) + "\n" + "\n".join(json.dumps(e) for e in events) + "\n")
print(f"wrote {dest}")
