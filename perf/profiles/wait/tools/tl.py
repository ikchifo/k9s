import json,sys
m=json.load(open(sys.argv[1])); anc=sys.argv[2]; a=float(sys.argv[3]); b=float(sys.argv[4])
t0=[x for x in m if x['what'].startswith(anc)][-1]['wall_ns']
for x in m:
  d=(x['wall_ns']-t0)/1e6
  if a<d<b and (len(sys.argv)<6 or not x['what'].startswith('row')): print('%8.1f %s'%(d,x['what'][:110]))
