#!/usr/bin/env python3
"""Diff apiserver_request_duration_seconds (and response sizes) buckets for
resource=pods between two /metrics snapshots."""
import re, sys

def load(p):
    h = {}
    sizes = {}
    reqs = {}
    for line in open(p):
        m = re.match(r'apiserver_request_duration_seconds_bucket\{([^}]*)\} (\d+)$', line.strip())
        if m and 'resource="pods"' in m.group(1) and 'subresource=""' in m.group(1):
            verb = re.search(r'verb="([^"]+)"', m.group(1)).group(1)
            le = re.search(r'le="([^"]+)"', m.group(1)).group(1)
            h[(verb, le)] = int(m.group(2))
        m = re.match(r'apiserver_response_sizes_sum\{([^}]*)\} (\d+(?:\.\d+)?)$', line.strip())
        if m and 'resource="pods"' in m.group(1):
            verb = re.search(r'verb="([^"]+)"', m.group(1)).group(1)
            sizes[verb] = float(m.group(2))
        m = re.match(r'apiserver_request_total\{([^}]*)\} (\d+)$', line.strip())
        if m and 'resource="pods"' in m.group(1) and 'client=' in m.group(1):
            cl = re.search(r'client="([^"]+)"', m.group(1)).group(1)
            verb = re.search(r'verb="([^"]+)"', m.group(1)).group(1)
            reqs[(cl, verb)] = int(m.group(2))
    return h, sizes, reqs

a, sa, ra = load(sys.argv[1])
b, sb, rb = load(sys.argv[2])
print("request_duration buckets delta (verb, le) count:")
shown = {}
for k in sorted(set(a) | set(b), key=lambda x: (x[0], float(x[1]))):
    d = b.get(k, 0) - a.get(k, 0)
    if d:
        shown.setdefault(k[0], []).append((k[1], d))
for verb, buckets in shown.items():
    print(f" {verb}: " + ", ".join(f"le={le}:{d}" for le, d in buckets))
print("response_sizes_sum delta:", {k: sb.get(k, 0) - sa.get(k, 0) for k in set(sa) | set(sb)})
print("request_total delta by client:")
for k in sorted(set(ra) | set(rb)):
    d = rb.get(k, 0) - ra.get(k, 0)
    if d:
        print(" ", k, d)
