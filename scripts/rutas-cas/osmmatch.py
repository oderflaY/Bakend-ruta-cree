import json, math, sys, heapq, numpy as np
from scipy.spatial import cKDTree
from scipy.sparse import coo_matrix
from scipy.sparse.csgraph import dijkstra as sp_dijkstra
sys.path.insert(0, sys.argv[1]); import pagetf
SP = sys.argv[1]
LAT0 = 25.53; KX, KY = math.cos(math.radians(LAT0)) * 111320, 110540
xy = lambda lon, lat: (lon * KX, lat * KY)
ll = lambda x, y: (x / KX, y / KY)

# ---- road graph, edges split every <= 20 m so every stretch of street has candidates ----
osm = json.load(open(f'{SP}/osm-gp.json'))
nd = {e['id']: xy(e['lon'], e['lat']) for e in osm['elements'] if e['type'] == 'node'}
coords, index, adj = [], {}, []
def node(key, p):
    if key not in index:
        index[key] = len(coords); coords.append(p); adj.append([])
    return index[key]
# The highlighter follows the main roads: side streets cost more, so a trace a few metres off
# does not hop from block to block.
CLASS_COST = {'motorway': 1.0, 'trunk': 1.0, 'primary': 1.0, 'secondary': 1.05, 'tertiary': 1.2,
              'motorway_link': 1.1, 'trunk_link': 1.1, 'primary_link': 1.1, 'secondary_link': 1.15, 'tertiary_link': 1.25,
              'unclassified': 1.5, 'residential': 1.8, 'living_street': 2.5}
def link(a, b, k=1.0):
    w = math.dist(coords[a], coords[b]) * k; adj[a].append((b, w)); adj[b].append((a, w))
SERVICE_OK = set()
for w in osm['elements']:
    if w['type'] != 'way': continue
    if w.get('tags', {}).get('highway') == 'service': continue  # parking aisles, driveways
    ids = [n for n in w['nodes'] if n in nd]
    cls = CLASS_COST.get(w.get('tags', {}).get('highway'), 1.8)
    for a, b in zip(ids, ids[1:]):
        pa, pb = nd[a], nd[b]; L = math.dist(pa, pb); n = max(1, math.ceil(L / 20))
        prev = node(a, pa)
        for k in range(1, n + 1):
            cur = node(b, pb) if k == n else node((a, b, k), (pa[0] + (pb[0]-pa[0])*k/n, pa[1] + (pb[1]-pa[1])*k/n))
            link(prev, cur, cls); prev = cur
coords = np.array(coords); tree = cKDTree(coords)
node_cls = np.full(len(coords), 9.0)
for u in range(len(adj)):
    for v, w in adj[u]:
        L = math.dist(coords[u], coords[v])
        if L > 0: node_cls[u] = min(node_cls[u], w / L)

def waypoints(P, spacing, radius):
    """Sparse points along the trace, each moved to the best nearby road: main roads win over closer
    side streets (the highlighter is drawn over the main roads, a little off)."""
    out = []
    for s in resample(P, spacing):
        d, i = tree.query(s, k=400, distance_upper_bound=radius)
        best, bc = None, 1e18
        for dd, j in zip(np.atleast_1d(d), np.atleast_1d(i)):
            if not np.isfinite(dd): continue
            c = dd + 250 * (node_cls[int(j)] - 1)
            if c < bc: best, bc = int(j), c
        if best is None: best = int(tree.query(s)[1])
        if not out or best != out[-1]: out.append(best)
    return out

def snake(P, spacing, radius):
    wps = waypoints(P, spacing, radius)
    nodes = [wps[0]]
    for a, b in zip(wps, wps[1:]):
        nodes += shortest(a, b)[1:]
    return nodes
print('graph nodes', len(coords), file=sys.stderr)

def local_dijkstra(src, limit):
    dist = {src: 0.0}; prev = {src: -1}; pq = [(0.0, src)]
    while pq:
        d, u = heapq.heappop(pq)
        if d > dist.get(u, 1e18) or d > limit: continue
        for v, w in adj[u]:
            nd_ = d + w
            if nd_ < dist.get(v, 1e18) and nd_ <= limit:
                dist[v] = nd_; prev[v] = u; heapq.heappush(pq, (nd_, v))
    return dist, prev

def path_from(prev, v):
    out = []
    while v != -1: out.append(v); v = prev[v]
    return out[::-1]

def resample(P, step):
    out = [P[0]]; acc = 0.0
    for a, b in zip(P, P[1:]):
        L = math.dist(a, b)
        if L == 0: continue
        t = step - acc
        while t <= L:
            out.append((a[0] + (b[0]-a[0])*t/L, a[1] + (b[1]-a[1])*t/L)); t += step
        acc = (acc + L) % step
    if math.dist(out[-1], P[-1]) > 1: out.append(P[-1])
    return out

def viterbi(P, sigma, radius, step=30.0, beta=40.0, K=60, beam=12):
    """P: trace in metres. Returns the list of graph nodes of the most likely drive along the streets."""
    S = resample(P, step)
    cands = []
    for s in S:
        # Many nodes, thinned to one every ~15 m, so a main road a bit farther still competes.
        d, i = tree.query(s, k=K * 4, distance_upper_bound=radius)
        c, kept = [], []
        for dd, j in zip(np.atleast_1d(d), np.atleast_1d(i)):
            if not np.isfinite(dd): continue
            p = coords[int(j)]
            if all(math.dist(p, q) > 15 for q in kept):
                kept.append(p); c.append((int(j), float(dd)))
            if len(c) >= K: break
        if not c:
            d, i = tree.query(s, k=1); c = [(int(i), float(d))]
        cands.append(c)
    cost = [{j: (dd / sigma) ** 2 / 2 for j, dd in cands[0]}]; back = [{}]; paths = [{}]
    for t in range(1, len(S)):
        straight = math.dist(S[t-1], S[t]); limit = straight * 3 + 2 * radius
        cur, bk, pth = {}, {}, {}
        for i, ci in sorted(cost[-1].items(), key=lambda kv: kv[1])[:beam]:
            dist, prev = local_dijkstra(i, limit)
            for j, dd in cands[t]:
                if j not in dist: continue
                c = ci + abs(dist[j] - straight) / beta + (dd / sigma) ** 2 / 2
                if c < cur.get(j, 1e18): cur[j], bk[j], pth[j] = c, i, path_from(prev, j)
        if not cur:  # the trace jumps: restart from the nearest candidates
            print('  restart at sample', t, 'of', len(S), [round(v) for v in S[t]], file=sys.stderr)
            best = min(cost[-1], key=cost[-1].get)
            dist, prev = local_dijkstra(best, 1e9 if False else 5000)
            for j, dd in cands[t]:
                if j in dist: cur[j], bk[j], pth[j] = cost[-1][best] + 50, best, path_from(prev, j)
            if not cur:
                cur = {j: (dd / sigma) ** 2 / 2 for j, dd in cands[t]}; bk = {j: None for j in cur}; pth = {j: [j] for j in cur}
        cost.append(cur); back.append(bk); paths.append(pth)
    j = min(cost[-1], key=cost[-1].get); out = []
    for t in range(len(S) - 1, 0, -1):
        seg = paths[t][j]; out = seg[1:] + out if out else seg
        if back[t][j] is None: out = [j] + out; j = min(cost[t-1], key=cost[t-1].get); continue
        j = back[t][j]
    return [j] + out if not out or out[0] != j else out

def shortest(a, b):
    dist, prev = local_dijkstra(a, 60000)
    return path_from(prev, b) if b in dist else [a, b]

def nearest(p): return int(tree.query(p)[1])

def clean(nodes, max_loop=600):
    """Drops detours: when the line comes back to a node it passed less than max_loop metres ago
    (a spur into a side street and back, a loop around a block), the detour is cut."""
    out, seen, along = [], {}, [0.0]
    for n in nodes:
        if out and out[-1] == n: continue
        if n in seen and along[-1] - along[seen[n]] < max_loop:
            k = seen[n]
            for m in out[k + 1:]: seen.pop(m, None)
            del out[k + 1:]; del along[k + 1:]
            continue
        if out: along.append(along[-1] + math.dist(coords[out[-1]], coords[n]))
        seen[n] = len(out); out.append(n)
    return out

if __name__ == '__main__':
    # import the scan tracing from cas.py without running it
    src = open(f'{SP}/cas.py').read().split('SPEC = json.load')[0]
    sys.argv = [sys.argv[0], SP]; ns = {}; exec(src, ns)
    spec = json.load(open(f'{SP}/cas-spec.json'))
    out = {}
    for rid, r in spec.items():
        res = {}
        for side in ('ida', 'vuelta'):
            if side not in r: continue
            nodes, via = [], None
            for leg in r[side]:
                if leg[0] == 'link':
                    via = leg[1] if len(leg) > 1 else []
                    continue
                _, page, anchors = leg
                tr = [xy(lon, lat) for lon, lat in ns['trace'](page, anchors)]
                rural = page in (2, 3)
                seg = snake(tr, spacing=1000 if rural else 300, radius=600 if rural else 120)
                if nodes:
                    pts = [nodes[-1]]
                    if via:
                        p2g, _ = pagetf.make(*pagetf.PAGES[via[0]])
                        pts += [nearest(xy(lon, lat)) for lat, lon in (p2g(x, y) for x, y in via[1])]
                    pts.append(seg[0]); bridge = []
                    for a, b in zip(pts, pts[1:]): bridge += shortest(a, b)[1:]
                    nodes += bridge[:-1]
                nodes += seg; via = None
            res[side] = clean(nodes)
        if 'vuelta' not in res: res['vuelta'] = res['ida'][::-1]
        out[rid] = {s: [ll(*coords[n]) for n in res[s]] for s in ('ida', 'vuelta')}
        L = lambda s: sum(math.dist(coords[a], coords[b]) for a, b in zip(res[s], res[s][1:])) / 1000
        print(rid, 'ida km', round(L('ida'), 1), 'vuelta km', round(L('vuelta'), 1), file=sys.stderr)
    json.dump(out, open(f'{SP}/cas-raw.json', 'w'))
