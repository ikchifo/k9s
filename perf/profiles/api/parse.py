#!/usr/bin/env python3
"""Parse klog v=9 client-go request logs + driver event files into per-action
API traffic tables. Usage: parse.py <klog> <events> <out-prefix> [anchor]"""
import re, sys, datetime, collections, json

KLOG = sys.argv[1]
EVENTS = sys.argv[2]
OUT = sys.argv[3]

# klog line: I1015 18:23:11.089321  89643 file.go:123] ...
HDR = re.compile(r'^([IWEF])(\d{4}) (\d{2}:\d{2}:\d{2}\.\d{6})\s+(\d+) (\S+):\d+\] (.*)$')

def klog_time_to_epoch(mdate, mtime, year=2026):
    dt = datetime.datetime.strptime(f"{year}{mdate} {mtime}", "%Y%m%d %H:%M:%S.%f")
    return dt.timestamp()

entries = []  # (epoch, kind, dict)
cur = None
with open(KLOG) as f:
    for line in f:
        m = HDR.match(line)
        if not m:
            if cur is not None:
                cur['text'] += line
            continue
        epoch = klog_time_to_epoch(m.group(2), m.group(3))
        text = m.group(6)
        if cur is not None:
            entries.append(cur)
        cur = {'epoch': epoch, 'text': text}
    if cur is not None:
        entries.append(cur)

reqs = []
for e in entries:
    t = e['text']
    if t.startswith('"Request"'):
        m = re.search(r'-X([A-Z]+)\b', t)
        u = re.search(r"'(https[^']+)'", t)
        acc = re.search(r'-H "Accept: ([^"]+)"', t)
        reqs.append({'epoch': e['epoch'], 'verb': m.group(1) if m else '?',
                     'url': u.group(1) if u else '?',
                     'accept': acc.group(1) if acc else '?', 'text': t})
    elif t.startswith('"Response"'):
        verb = re.search(r'verb="([A-Z]+)"', t)
        url = re.search(r'url="([^"]+)"', t)
        status = re.search(r'status="([^"]+)"', t)
        ms = re.search(r'milliseconds=(\d+)', t)
        spm = re.search(r'serverProcessingMilliseconds=(\d+)', t)
        cl = re.search(r'Content-Length: (\d+)', t)
        reqs.append({'epoch': e['epoch'], 'kind': 'resp', 'text': t,
                     'verb': verb.group(1) if verb else '?',
                     'url': url.group(1) if url else '?',
                     'status': status.group(1) if status else '?',
                     'ms': int(ms.group(1)) if ms else None,
                     'spm': int(spm.group(1)) if spm else None,
                     'bytes': int(cl.group(1)) if cl else None})

# pair requests to responses: response follows request; use per-verb/URL queue
pending = collections.defaultdict(collections.deque)
pairs = []
for r in reqs:
    if r.get('kind') == 'resp':
        key = (r['verb'], r['url'])
        q = pending.get(key)
        if q:
            pr = q.popleft()
            pairs.append({**pr, **{k: r[k] for k in ('status', 'ms', 'spm', 'bytes')},
                          'resp_epoch': r['epoch']})
        else:
            pairs.append({**r, 'accept': '?', 'resp_epoch': r['epoch']})
    else:
        pending[(r['verb'], r['url'])].append(r)

# events
events = []
with open(EVENTS) as f:
    for line in f:
        parts = line.split()
        if len(parts) >= 2:
            events.append((' '.join(parts[:-1]), float(parts[-1])))
t0 = events[0][1] if events else 0

# windows: [start_event, end_event) label
wins = [('startup', 'launch', 'full_pods_table'),
        ('idle20', 'idle_start', 'idle_end'),
        ('switch_deploy', 'deploy_table_prev', 'deploy_table'),
        ('switch_pods', 'pods_table_prev', 'pods_table')]
# simpler: define windows by consecutive events
names = [e[0] for e in events]
def win(a, b):
    ia = [i for i, e in enumerate(events) if e[0].startswith(a)]
    ib = [i for i, e in enumerate(events) if e[0].startswith(b)]
    return (events[ia[-1]][1], events[ib[-1]][1])

windows = []
windows.append(('startup', *win('launch', 'full_pods_table')))
windows.append(('idle20', *win('idle_start', 'idle_end')))
# deploy switch: from idle_end to deploy_table (action sent right at idle_end)
windows.append(('switch_deploy', *win('idle_end', 'deploy_table')))
# pods switch: from (deploy_table+settle) to pods_table — approximate deploy settle 2s
if 'deploy_table' in names and 'pods_table' in names:
    dt = dict(events)['deploy_table']
    pt = dict(events)['pods_table']
    windows.append(('switch_pods', dt + 2.0, pt))
if 'ctx_switch_done' in names:
    ct = dict(events)['ctx_other_table']
    windows.append(('ctx_switch', ct + 3.0, dict(events)['ctx_switch_done']))

def classify(url):
    from urllib.parse import urlparse, parse_qs
    p = urlparse(url)
    q = parse_qs(p.query)
    path = p.path
    if path in ('/api', '/apis', '/api/v1', '/openapi/v2', '/openapi/v3', '/version'):
        return 'discovery:' + path
    if path.endswith('/selfSubjectAccessReviews'):
        return 'SelfSubjectAccessReview'
    if path.endswith('/selfsubjectrulesreviews') or 'subjectaccessreviews' in path:
        return 'auth'
    if 'watch' in path:
        return 'watch:' + path.split('/')[-1]
    if path.endswith('/pods'):
        return 'LIST pods'
    if path.endswith('/deployments'):
        return 'LIST deployments'
    if '/namespaces' in path:
        return 'namespaces'
    return path

report = {'t0': t0, 'windows': {}}
for name, ws, we in windows:
    rows = [r for r in pairs if ws <= r['epoch'] <= we]
    agg = collections.OrderedDict()
    for r in rows:
        key = (r['verb'], classify(r['url']),
               'watch' if 'watch=true' in r['url'] else ('sendInitialEvents' if 'sendInitialEvents=true' in r['url'] else 'plain'))
        a = agg.setdefault(key, {'n': 0, 'ms': 0, 'bytes': 0, 'spm': 0, 'max_ms': 0,
                                  'statuses': set(), 'accepts': set()})
        a['n'] += 1
        a['ms'] += r.get('ms') or 0
        a['spm'] += r.get('spm') or 0
        a['max_ms'] = max(a['max_ms'], r.get('ms') or 0)
        a['bytes'] += r.get('bytes') or 0
        a['statuses'].add(r.get('status', '?'))
        if r.get('accept', '?') != '?':
            a['accepts'].add(r['accept'].split(',')[0][:60])
    report['windows'][name] = {
        'span': [ws - t0, we - t0], 'total_reqs': len(rows),
        'by_kind': {f"{k[0]} {k[1]} [{k[2]}]": {'n': v['n'], 'ms_total': v['ms'],
                     'ms_max': v['max_ms'], 'server_ms_total': v['spm'],
                     'bytes': v['bytes'], 'status': sorted(v['statuses']),
                     'accept': sorted(v['accepts'])}
                    for k, v in sorted(agg.items())}}

with open(OUT + '.json', 'w') as f:
    json.dump(report, f, indent=1, default=str)

# human table
lines = []
for name, ws, we in windows:
    lines.append(f"\n=== {name} (t={ws-t0:.1f}s..{we-t0:.1f}s) ===")
    w = report['windows'].get(name)
    if not w:
        continue
    lines.append(f"total requests: {w['total_reqs']}")
    for k, v in w['by_kind'].items():
        lines.append(f"  {k}: n={v['n']} ms_total={v['ms_total']} ms_max={v['ms_max']} "
                      f"server_ms={v['server_ms_total']} bytes={v['bytes']} status={v['status']} accept={v['accept']}")
print('\n'.join(lines))
