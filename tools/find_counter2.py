import sys; sys.path.insert(0,"tools")
from decode_stream import decode
from collections import defaultdict
lo,hi=int(sys.argv[2]),int(sys.argv[3])
series=defaultdict(list)
for ts,d,op,f in decode(sys.argv[1], ""):
    for i in range(len(f)):
        for w in (1,2,4):
            if i+w>len(f): continue
            v=int.from_bytes(f[i:i+w],"little")
            if lo<=v<=hi: series[(d,op,len(f),i,w)].append((ts[11:23],v))
for k,s in series.items():
    vals=[v for _,v in s]
    if len(set(vals))>=3 and all(b>=a for a,b in zip(vals,vals[1:])) and max(vals)-min(vals)>=3:
        print(k,vals,s[0][0],s[-1][0])
