import json,sys,re,datetime
d=sys.argv[1]; anc=sys.argv[2]; logf=sys.argv[3] if len(sys.argv)>3 else d+'/klog.log'
m=json.load(open(d+'/marks.json')); t0=[x for x in m if x['what'].startswith(anc)][-1]['wall_ns']/1e9
day=datetime.datetime.fromtimestamp(t0).date()
ev=[]
for l in open(logf):
  r=re.match(r'[IWEF]\d{4} (\d\d:\d\d:\d\d\.\d+) +\d+ (\S+)\] (.*)',l)
  if not r: continue
  t=datetime.datetime.combine(day,datetime.time.fromisoformat(r[1])).timestamp()
  msg=r[3]
  if 'Feature gate' in msg: continue
  msg=re.sub(r'https://[^/]+','',msg); msg=re.sub(r'reflector="[^"]*"','',msg)
  ev.append(((t-t0)*1000,msg[:150]))
for x in m:
  if not x['what'].startswith('row'): ev.append(((x['wall_ns']/1e9-t0)*1000,'MARK '+x['what'][:80]))
a=float(sys.argv[4]) if len(sys.argv)>4 else -1e9; b=float(sys.argv[5]) if len(sys.argv)>5 else 1e9
for t,s in sorted(ev):
  if a<=t<=b: print('%8.1f %s'%(t,s))
