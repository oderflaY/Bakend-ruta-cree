import json, math, sys, time, urllib.request, urllib.error, numpy as np
from scipy.sparse import coo_matrix
from scipy.sparse.csgraph import dijkstra
sys.path.insert(0, sys.argv[1]); import pagetf

def dist(a, b): return math.hypot((a[0]-b[0])*100500, (a[1]-b[1])*110540)

_graphs = {}
def graph(page):
    if page in _graphs: return _graphs[page]
    sk = np.load(f'{sys.argv[1]}/rec/sk-{1 if page == 4 else page}.npy')
    ys, xs = np.nonzero(sk); idx = -np.ones(sk.shape, int); idx[ys, xs] = np.arange(len(ys))
    rows, cols, w = [], [], []
    for dy in (-1, 0, 1):
        for dx in (-1, 0, 1):
            if dx == dy == 0: continue
            y2, x2 = ys + dy, xs + dx
            ok = (y2 >= 0) & (y2 < sk.shape[0]) & (x2 >= 0) & (x2 < sk.shape[1])
            j = np.full(len(ys), -1); j[ok] = idx[y2[ok], x2[ok]]
            m = j >= 0
            rows += np.nonzero(m)[0].tolist(); cols += j[m].tolist(); w += [math.hypot(dx, dy)] * int(m.sum())
    g = coo_matrix((w, (rows, cols)), shape=(len(ys), len(ys))).tocsr()
    _graphs[page] = (g, xs, ys); return _graphs[page]

def trace(page, anchors):
    g, xs, ys = graph(page)
    near = lambda p: int(np.argmin((xs - p[0])**2 + (ys - p[1])**2))
    nodes = [near(a) for a in anchors]
    path = []
    for a, b in zip(nodes, nodes[1:]):
        _, pred = dijkstra(g, indices=a, return_predecessors=True)
        seq, k = [], b
        while k != a and k >= 0: seq.append(k); k = pred[k]
        seq.append(a); seq.reverse()
        path += seq if not path else seq[1:]
    p2g, _ = pagetf.make(*pagetf.PAGES[page])
    pts = [p2g(xs[i], ys[i]) for i in path]
    return [(lon, lat) for lat, lon in pts]

def osrm(url):
    for _ in range(5):
        try:
            return json.load(urllib.request.urlopen(urllib.request.Request(url, headers={'User-Agent': 'rutacree-dev'}), timeout=60))
        except urllib.error.HTTPError as e:
            d = json.loads(e.read() or b'{}')
            if d.get('code') in ('NoMatch', 'NoSegment', 'TooBig'): return d
        except Exception: pass
        time.sleep(3)
    return {}

def densify(L, step):
    out = [L[0]]
    for a, b in zip(L, L[1:]):
        n = max(1, int(dist(a, b) / step))
        out += [(a[0]+(b[0]-a[0])*k/n, a[1]+(b[1]-a[1])*k/n) for k in range(1, n+1)]
    return out

def thin(L, step):
    out = [L[0]]
    for p in L[1:]:
        if dist(p, out[-1]) >= step: out.append(p)
    if out[-1] != L[-1]: out.append(L[-1])
    return out

def match(L, step):
    pts = thin(densify(L, 10), step)
    res = []
    for i in range(0, len(pts) - 1, 8):
        chunk = pts[i:i+10]
        if len(chunk) < 2: break
        coords = ';'.join(f'{x:.6f},{y:.6f}' for x, y in chunk)
        d = osrm(f"https://router.project-osrm.org/match/v1/driving/{coords}?overview=full&geometries=geojson&radiuses={';'.join(['60']*len(chunk))}&gaps=ignore&tidy=true")
        geom = [c for m in d.get('matchings', []) for c in m['geometry']['coordinates']] or [list(p) for p in chunk]
        if i + 10 < len(pts):
            cut = pts[i+8]; j = min(range(len(geom)), key=lambda k: dist(geom[k], cut)); geom = geom[:j+1]
        if res and geom:
            j = min(range(min(len(geom), 25)), key=lambda k: dist(geom[k], res[-1])); geom = geom[j+1:]
        res += [tuple(p) for p in geom]
        time.sleep(0.4)
    return res

def route(a, b):
    d = osrm(f"https://router.project-osrm.org/route/v1/driving/{a[0]:.6f},{a[1]:.6f};{b[0]:.6f},{b[1]:.6f}?overview=full&geometries=geojson")
    return [tuple(p) for p in d['routes'][0]['geometry']['coordinates']] if d.get('routes') else [a, b]

def despike(L, close=15, maxlen=400):
    L = [tuple(p) for p in L]; changed = True
    while changed:
        changed = False; i = 0
        while i < len(L) - 2:
            run = 0.0
            for j in range(i + 1, len(L)):
                run += dist(L[j-1], L[j])
                if run > maxlen: break
                if j > i + 1 and dist(L[i], L[j]) < close and run > 3 * close:
                    del L[i+1:j]; changed = True; break
            i += 1
    out = [L[0]]
    for p in L[1:]:
        if dist(p, out[-1]) > 0.5: out.append(p)
    return out

def route_via(pts):
    coords = ';'.join(f'{x:.6f},{y:.6f}' for x, y in pts)
    d = osrm(f"https://router.project-osrm.org/route/v1/driving/{coords}?overview=full&geometries=geojson")
    return [tuple(p) for p in d['routes'][0]['geometry']['coordinates']] if d.get('routes') else list(pts)

def build(legs):
    """legs: ('trace', page, anchors) follows the highlighter; ('link', [page, [[x, y], ...]]) joins two
    traces by the street network, through the given points of a page if any."""
    line, pending, via = [], False, []
    for leg in legs:
        if leg[0] == 'link':
            pending = True
            if len(leg) > 1:
                page, pts = leg[1]
                p2g, _ = pagetf.make(*pagetf.PAGES[page])
                via = [(lon, lat) for lat, lon in (p2g(x, y) for x, y in pts)]
            continue
        _, page, anchors = leg
        step = {2: 120, 3: 120}.get(page, 60)
        seg = match(trace(page, anchors), step)
        if line:
            gap = route_via([line[-1], *via, seg[0]]) if pending or dist(line[-1], seg[0]) > 80 else []
            via = []
            line += gap[1:] + seg
        else:
            line = seg
        pending = False
    return despike(line)

SPEC = json.load(open(sys.argv[2]))
out = {}
for rid, r in SPEC.items():
    ida = build(r['ida'])
    vuelta = build(r['vuelta']) if 'vuelta' in r else list(reversed(ida))
    L = sum(dist(a, b) for a, b in zip(ida, ida[1:]))
    print(rid, 'ida km', round(L/1000, 1), 'pts', len(ida), 'vuelta km', round(sum(dist(a, b) for a, b in zip(vuelta, vuelta[1:]))/1000, 1))
    out[rid] = {'ida': ida, 'vuelta': vuelta}
json.dump(out, open(sys.argv[3], 'w'))
