#!/usr/bin/env python3
"""Measure the WatchList (sendInitialEvents=true) initial burst that k9s's
dynamic informer consumes: bytes and events until initial-events-end bookmark."""
import http.client, json, sys, time

path = '/api/v1/pods?allowWatchBookmarks=true&resourceVersionMatch=NotOlderThan&sendInitialEvents=true&watch=true'
c = http.client.HTTPConnection('127.0.0.1', 18001, timeout=60)
t0 = time.time()
c.request('GET', path, headers={'Accept': 'application/json'})
r = c.getresponse()
print('status', r.status, 'content-length hdr:', r.getheader('Content-Length'))
n_events = 0
bytes_read = 0
dec = json.JSONDecoder()
buf = ''
first_event_at = None
end_at = None
while True:
    chunk = r.read(1 << 16)
    if not chunk:
        break
    bytes_read += len(chunk)
    if first_event_at is None:
        first_event_at = time.time() - t0
    buf += chunk.decode('utf-8', 'replace')
    # parse complete JSON objects
    while True:
        s = buf.lstrip()
        off = len(buf) - len(s)
        try:
            obj, idx = dec.raw_decode(s)
        except json.JSONDecodeError:
            break
        buf = s[idx:]
        off = 0
        n_events += 1
        typ = obj.get('type')
        ann = (obj.get('object') or {}).get('metadata', {}).get('annotations', {}) or {}
        if typ == 'BOOKMARK' and ann.get('k8s.io/initial-events-end') == 'true':
            end_at = time.time() - t0
            print(json.dumps({'bytes': bytes_read, 'events': n_events,
                              'first_event_s': round(first_event_at, 3),
                              'initial_events_end_s': round(end_at, 3)}))
            c.close()
            sys.exit(0)
print('stream ended without bookmark', bytes_read, n_events)
