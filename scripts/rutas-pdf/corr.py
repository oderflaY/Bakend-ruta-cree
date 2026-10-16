# corr.py osm.json spec.json r.json m.json
# Routes ida/vuelta over the OSM street graph through the spec's ordered waypoints. Each edge costs
# length x road class x (1 + ((distance to the band drawn on the plan - FREE)/SIG)^2), so between two
# waypoints the path hugs the drawn line instead of taking the shortest way; going against a one-way
# costs extra (but is allowed: the DIF plan is the authority). Waypoints marked "d" are joined by a
# straight line (streets missing in OSM).
import json, sys, math, re, unicodedata, numpy as np
from scipy.spatial import cKDTree
from scipy.sparse import csr_matrix
from scipy.sparse.csgraph import dijkstra
osm = json.load(open(sys.argv[1])); spec = json.load(open(sys.argv[2])); pdf = json.load(open(sys.argv[3])); tf = json.load(open(spec['tf']))
FREE, SIG = spec.get('free', 45.0), spec.get('sig', 35.0)
A, t = np.array(tf['A']), np.array(tf['t'])
KX, KY = 101700.0, 110540.0
def norm(s): return unicodedata.normalize('NFKD', s).encode('ascii', 'ignore').decode().lower()
def geo_pt(p):  # pdf pt -> (lon, lat)
    q = A @ np.array([p[0], -p[1]]) + t
    return (tf['lon0'] + q[0]/tf['kx'], tf['lat0'] + q[1]/tf['ky'])
def px_geo(px, py): return geo_pt((px*72/110, py*72/110))
def xy(lon, lat): return np.array([lon*KX, lat*KY])
CLS = {'motorway': 1.0, 'trunk': 1.0, 'primary': 1.0, 'secondary': 1.1, 'tertiary': 1.2, 'motorway_link': 1.3, 'trunk_link': 1.3,
       'primary_link': 1.3, 'secondary_link': 1.3, 'tertiary_link': 1.3, 'unclassified': 1.5, 'residential': 1.5, 'living_street': 2.5, 'service': 3.0}
# graph
key, coords, names = {}, [], []
def node(lon, lat):
    k = (round(lon, 7), round(lat, 7))
    if k not in key: key[k] = len(coords); coords.append((lon, lat)); names.append(set())
    return key[k]
E = []  # u, v, len, cls, dirpenalty
for w in osm['elements']:
    tg = w.get('tags', {}); c = CLS.get(tg.get('highway'))
    if c is None: continue
    ow = tg.get('oneway', 'no'); nm = norm(tg.get('name', ''))
    ids = [node(p['lon'], p['lat']) for p in w['geometry']]
    for i in ids: names[i].add(nm)
    for a, b in zip(ids, ids[1:]):
        if a == b: continue
        L = float(np.linalg.norm(xy(*coords[a]) - xy(*coords[b])))
        fw, bw = 1.0, 1.0
        if ow in ('yes', 'true', '1'): bw = 4.0
        elif ow == '-1': fw = 4.0
        E.append((a, b, L, c, fw)); E.append((b, a, L, c, bw))
N = len(coords); C = np.array([xy(*p) for p in coords])
U = np.array([e[0] for e in E]); V = np.array([e[1] for e in E]); L = np.array([e[2] for e in E]); CL = np.array([e[3] for e in E]); DP = np.array([e[4] for e in E])
tree = cKDTree(C)
# bands drawn on the plan, per direction, as dense points
def bands(polys):
    pts = []
    for poly in polys:
        P = np.array(poly); c = P.mean(0)
        U_ = np.unique(np.round(P, 3), axis=0)
        w_, v_ = np.linalg.eigh(np.cov((U_ - c).T)); ax = v_[:, -1]; pr = (U_ - c) @ ax
        a, b = xy(*geo_pt(c + ax*pr.min())), xy(*geo_pt(c + ax*pr.max()))
        n = max(2, int(np.linalg.norm(b - a) / 5))
        pts += [a + (b - a)*k/n for k in range(n + 1)]
    return cKDTree(np.array(pts))
BT = {k: bands(pdf['route'][k]) for k in ('ida', 'vuelta')}
def edge_dist(bt):
    d = np.zeros(len(U))
    for f in (0.0, 0.5, 1.0):
        d += bt.query(C[U]*(1-f) + C[V]*f)[0]
    return d/3
def wp_xy(w):
    if 'geo' in w: return xy(*w['geo'])
    return xy(*px_geo(*w['p']))
def snap(w):
    p = wp_xy(w)
    if 's' in w:
        r = re.compile(w['s']); d, idx = tree.query(p, k=400, distance_upper_bound=250)
        for dd, i in zip(d, idx):
            if np.isfinite(dd) and any(r.search(n) for n in names[i]): return int(i)
        print("  !! no street", w['s'], "near", w)
    d, i = tree.query(p); return int(i)
def run(k):
    W = spec[k]
    # guide for this direction: the drawn band plus the straight waypoint polyline (where the plan has no band)
    wpts = [wp_xy(w) for w in W]
    gl = []
    for a, b in zip(wpts, wpts[1:]):
        n = max(2, int(np.linalg.norm(b - a)/5)); gl += [a + (b - a)*j/n for j in range(n + 1)]
    dband = edge_dist(BT[k]); dline = edge_dist(cKDTree(np.array(gl)))
    dd = np.minimum(dband, dline + 15)  # the waypoint line is a rougher guide than the drawing
    cost = L * CL * DP * (1 + (np.maximum(0, dd - FREE)/SIG)**2)
    g = csr_matrix((cost, (U, V)), shape=(N, N))
    out = []; prev = None; prevxy = None
    for i, w in enumerate(W):
        if i == 0:
            prev = snap(w) if not w.get('d') else None; prevxy = wp_xy(w); out.append(tuple(coords[prev]) if prev is not None else None); continue
        if w.get('d'):
            p = wp_xy(w); out.append((p[0]/KX, p[1]/KY)); prevxy = p; prev = None; continue
        tgt = snap(w)
        if prev is None:  # coming from a straight leg: start at the nearest node
            prev = int(tree.query(prevxy)[1])
        dist, pred = dijkstra(g, indices=prev, return_predecessors=True, limit=200000)
        if not np.isfinite(dist[tgt]): print("  !! no path to", w); continue
        path = []; x = tgt
        while x != prev and x >= 0: path.append(x); x = pred[x]
        path = path[::-1]
        straight = float(np.linalg.norm(C[tgt] - C[prev])); plen = sum(float(np.linalg.norm(C[a]-C[b])) for a, b in zip([prev]+path, path))
        if plen > 1.6*straight + 200: print(f"  long leg -> {w}: {round(plen)} m vs {round(straight)} m")
        out += [tuple(coords[x]) for x in path]; prev = tgt; prevxy = C[tgt]
    out = [p for p in out if p is not None]
    ded = [out[0]]
    for p in out[1:]:
        if p != ded[-1]: ded.append(p)
    km = sum(math.hypot((a[0]-b[0])*KX, (a[1]-b[1])*KY) for a, b in zip(ded, ded[1:]))/1000
    print(k, "points", len(ded), "km", round(km, 1))
    return {'raw': [list(map(float, (q[0]/KX, q[1]/KY))) for q in wpts], 'matched': [list(p) for p in ded]}
json.dump({k: run(k) for k in ('ida', 'vuelta')}, open(sys.argv[4], 'w'))
