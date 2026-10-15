import json, sys, re, math, unicodedata
def norm(s): return unicodedata.normalize('NFKD', s).encode('ascii','ignore').decode().lower()
osm = json.load(open(sys.argv[1]))
ways = [w for w in osm['elements'] if w['type']=='way']
def named(rx):
    r = re.compile(rx)
    return [w for w in ways if r.search(norm(w.get('tags',{}).get('name','')))]
def inter(a, b):
    A = [(p['lat'],p['lon']) for w in named(a) for p in w['geometry']]
    B = [(p['lat'],p['lon']) for w in named(b) for p in w['geometry']]
    best=None
    for p in A:
        for q in B:
            d=(p[0]-q[0])**2+((p[1]-q[1])*0.914)**2
            if best is None or d<best[0]: best=(d,p,q)
    return best
cmd=sys.argv[2]
if cmd=='names':
    names=sorted({w['tags'].get('name','') for w in named(sys.argv[3])})
    print(names)
elif cmd=='inter':
    for pair in sys.argv[3:]:
        a,b=pair.split(' & ')
        r=inter(a,b)
        print(pair, None if r is None else (round(math.sqrt(r[0])*111000), r[1]))
