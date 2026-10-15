import json, sys, math, numpy as np
from scipy.spatial import cKDTree
osm = json.load(open(sys.argv[1]))
nodes = {e['id']: (e['lon'], e['lat']) for e in osm['elements'] if e['type'] == 'node'}
LAT0 = 25.53; KX, KY = math.cos(math.radians(LAT0)) * 111320, 110540
pts = []
for w in osm['elements']:
    if w['type'] != 'way': continue
    c = [nodes[n] for n in w['nodes'] if n in nodes]
    for a, b in zip(c, c[1:]):
        L = math.hypot((b[0]-a[0])*KX, (b[1]-a[1])*KY); n = max(1, int(L / 3))
        pts += [((a[0]+(b[0]-a[0])*k/n)*KX, (a[1]+(b[1]-a[1])*k/n)*KY) for k in range(n)]
tree = cKDTree(pts)
for f in sys.argv[2:]:
    line = json.load(open(f))['features'][0]['geometry']['coordinates']
    seg = []
    for a, b in zip(line, line[1:]):
        L = math.hypot((b[0]-a[0])*KX, (b[1]-a[1])*KY); n = max(1, int(L / 5))
        seg += [((a[0]+(b[0]-a[0])*k/n)*KX, (a[1]+(b[1]-a[1])*k/n)*KY) for k in range(n)]
    d, _ = tree.query(seg)
    print(f.split('/')[-1], 'muestras', len(d), 'fuera de calle (>15 m):', f'{(d > 15).mean()*100:.1f}%', ' >40 m:', f'{(d > 40).mean()*100:.1f}%', ' máx', round(d.max()), 'm')
