#!/usr/bin/env python3
"""Time the raw WatchList initial-events burst (no JSON parsing): bytes + wall
time until the initial-events-end bookmark appears on the wire."""
import http.client, sys, time

runs = int(sys.argv[1]) if len(sys.argv) > 1 else 3
path = '/api/v1/pods?allowWatchBookmarks=true&resourceVersionMatch=NotOlderThan&sendInitialEvents=true&watch=true'
MARK = b'k8s.io/initial-events-end'
for i in range(runs):
    c = http.client.HTTPConnection('127.0.0.1', 18001, timeout=120)
    t0 = time.time()
    c.request('GET', path, headers={'Accept': 'application/json'})
    r = c.getresponse()
    assert r.status == 200, r.status
    n = 0
    buf_tail = b''
    while True:
        chunk = r.read(1 << 20)
        if not chunk:
            break
        n += len(chunk)
        if MARK in chunk or MARK in buf_tail:
            dt = time.time() - t0
            print(f'run{i}: initial_events_done={dt:.3f}s bytes={n}')
            break
        buf_tail = chunk[-64:]
    c.close()
