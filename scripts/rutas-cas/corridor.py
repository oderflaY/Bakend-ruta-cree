"""Ajuste por corredor: la ruta va por las calles de OSM, pero solo dentro de una franja alrededor del
marcador amarillo, y entre dos calles de la franja prefiere la más cercana al marcador y la de mayor
jerarquía. El trazo se parte en tramos cortos (checkpoints), así una ruta que pasa dos veces por la
misma avenida o hace una vuelta en U no se "corta camino".

    python corridor.py <carpeta con osm-gp.json, cas.py, cas-spec.json, rec/> salida.json
"""
import json, math, sys, numpy as np
from scipy.spatial import cKDTree
from scipy.sparse import csr_matrix
from scipy.sparse.csgraph import dijkstra

SP, OUT = sys.argv[1], sys.argv[2]
sys.argv = [sys.argv[0], SP]
import osmmatch as om  # grafo de calles (nodos cada <= 20 m), xy/ll, pagetf
pagetf = om.pagetf
coords = om.coords
N = len(coords)

# Aristas como arreglos: u, v, longitud, clase (1 = avenida principal … 2.5 = privada).
U, V, L, C = [], [], [], []
for u in range(N):
    for v, w in om.adj[u]:
        if u < v:
            d = math.dist(coords[u], coords[v])
            U.append(u); V.append(v); L.append(d); C.append(w / d if d else 1.0)
U, V, L, C = map(np.array, (U, V, L, C))
MID = (coords[U] + coords[V]) / 2
mid_tree = cKDTree(MID)

# Costo de cada arista: longitud × clase**EXP (avenidas baratas, privadas caras) × (1 + DW·(d/franja)²),
# más 20·(distancia fuera del amarillo / YS)² por metro: salirse del marcador cuesta mucho.
import os
YS, EXP, DW, YK = float(os.environ.get("YS", 30)), 4.0, 0.3, float(os.environ.get("YK", 200))
# Franja (m) alrededor del trazo por página: a menor zoom el marcador cubre más terreno.
# El marcador queda corrido hasta ~100 m de la avenida que marca (más en las páginas de carretera).
LINK_EXP = float(os.environ.get("LINK_EXP", 6))
TOL = {k: float(v) for k, v in zip((1, 4, 5, 2, 3), os.environ.get('TOL', '150,150,70,5000,5000').split(','))}
# En ciudad (páginas 1, 4 y 5) la ruta va por la calle MÁS CERCANA al trazo, sea avenida o calle de
# colonia (las rutas CAS recogen gente en su casa): SIG = a cuántos metros del trazo una calle ya cuesta
# el doble, y la jerarquía casi no pesa. En carretera (2 y 3) sigue mandando la jerarquía.
SIG = {k: float(v) for k, v in zip((1, 4, 5), os.environ.get('SIG', '45,45,40').split(','))}
PEXP = {k: float(os.environ.get('PEXP', 3)) for k in (1, 4, 5)}
FREE = float(os.environ.get('FREE', 90))  # calles a menos de FREE m del trazo no pagan por distancia
BAND = {1: 400, 4: 400, 2: 800, 3: 800, 5: 250}
CHUNK = {1: 700, 4: 700, 2: 2500, 3: 2500, 5: 500}

from scipy.ndimage import distance_transform_edt
_ydist = {}
def yellow_dist(page):
    """Distancia (m) de cada arista al área marcada en amarillo de esa página (0 dentro)."""
    if page in _ydist: return _ydist[page]
    mask = np.load(f'{SP}/rec/y-{page}.npy')
    lat_c, lon_c, z = pagetf.PAGES[page]
    mpp = pagetf.S13 * 2 ** (13 - z)
    edt = distance_transform_edt(~mask) * mpp
    kx, ky = math.cos(math.radians(lat_c)) * 111320, 110540
    lon, lat = MID[:, 0] / om.KX, MID[:, 1] / om.KY
    px = np.round(pagetf.CX + (lon - lon_c) * kx / mpp).astype(int)
    py = np.round(pagetf.CY - (lat - lat_c) * ky / mpp).astype(int)
    inside = (px >= 0) & (py >= 0) & (px < mask.shape[1]) & (py < mask.shape[0])
    d = np.full(len(MID), 5000.0)
    d[inside] = edt[py[inside], px[inside]]
    _ydist[page] = d
    return d

def resample(P, step):
    return om.resample(P, step)

def route(src, targets, extra, cost_mask, cost):
    """Camino de menor costo de src a alguno de targets (costo del camino + extra del destino)."""
    idx = np.nonzero(cost_mask)[0]
    nodes = np.unique(np.concatenate([U[idx], V[idx], [src], targets]))
    remap = -np.ones(N, int); remap[nodes] = np.arange(len(nodes))
    a, b, w = remap[U[idx]], remap[V[idx]], cost[idx]
    g = csr_matrix((np.concatenate([w, w]), (np.concatenate([a, b]), np.concatenate([b, a]))), shape=(len(nodes), len(nodes)))
    dist, pred = dijkstra(g, indices=remap[src], return_predecessors=True)
    tot = dist[remap[targets]] + extra
    k = int(np.argmin(tot))
    if not np.isfinite(tot[k]):
        return None
    t = remap[targets[k]]; out = []
    while t >= 0:
        out.append(int(nodes[t])); t = pred[t]
    return out[::-1]

def snap(p, band, local_tree, sigma, hw=3):
    """El nodo para un checkpoint: cerca del marcador y sobre la calle de mayor jerarquía."""
    d, i = om.tree.query(p, k=600, distance_upper_bound=band)
    best, bc = None, 1e18
    for dd, j in zip(np.atleast_1d(d), np.atleast_1d(i)):
        if not np.isfinite(dd): continue
        j = int(j)
        c = (local_tree.query(coords[j])[0] / sigma) ** 2 + hw * (om.node_cls[j] - 1)
        if c < bc: best, bc = j, c
    return best if best is not None else int(om.tree.query(p)[1])

def corridor(P, page):
    """P: trazo del marcador en metros. Devuelve los nodos del recorrido por las calles."""
    band = BAND[page]; sigma = SIG.get(page, band / 3); chunk = CHUNK[page]
    hw = 0.3 if page in SIG else 3
    S = np.array(resample(P, 10.0))
    along = np.concatenate([[0], np.cumsum(np.linalg.norm(np.diff(S, axis=0), axis=1))])
    cuts = list(np.arange(0, along[-1], chunk)) + [along[-1]]
    if len(cuts) > 2 and cuts[-1] - cuts[-2] < chunk / 3: del cuts[-2]
    nodes = []
    prev = None
    yd = yellow_dist(page)
    for k in range(len(cuts) - 1):
        last = k == len(cuts) - 2
        lo, hi = cuts[k] - 2 * band, cuts[k + 1] + 2 * band
        part = S[(along >= lo) & (along <= hi)]
        lt = cKDTree(part)
        if prev is None:
            prev = snap(S[0], band, lt, sigma, hw)
        end = S[min(np.searchsorted(along, cuts[k + 1]), len(S) - 1)]
        # El tramo termina en cualquier calle cerca del fin del tramo: el camino elige cuál, así dos
        # tramos seguidos no saltan entre calles paralelas.
        if last:
            targets = np.array([snap(end, band, lt, sigma, hw)]); extra = np.zeros(1)
        else:
            d, i = om.tree.query(end, k=800, distance_upper_bound=band)
            ok = np.isfinite(d); targets = np.atleast_1d(i)[ok]; extra = (np.maximum(0, np.atleast_1d(d)[ok] - (FREE if page in SIG else 0)) / sigma) ** 2 * 30
            if not len(targets):
                targets = np.array([snap(end, band, lt, sigma, hw)]); extra = np.zeros(1)
        path = [prev] if (len(targets) == 1 and targets[0] == prev) else None
        for grow in (1, 1.6, 2.5, None):
            if path is not None: break
            if grow is None:  # sin conexión en la franja: toda la red, pero alejarse del marcador cuesta
                dmid = lt.query(MID)[0]; m = np.ones(len(U), bool)
                print('   tramo fuera de la franja, página', page, 'km', round(cuts[k] / 1000, 1), file=sys.stderr)
            else:
                cand = mid_tree.query_ball_point(part[::5], band * grow + 15)
                idx = np.unique(np.concatenate([np.array(c, int) for c in cand if c]))
                dmid = np.full(len(U), 1e9); dmid[idx] = lt.query(MID[idx])[0]
                m = dmid <= band * grow
            if page in SIG:
                cost = L * (C ** PEXP[page] * (1 + (np.maximum(0, dmid - FREE) / SIG[page]) ** 2))
            else:
                cost = L * (C ** EXP * (1 + (dmid / band) ** 2 * DW) + YK * (np.maximum(0, yd - TOL[page]) / YS) ** 2)
            path = route(prev, targets, extra, m, cost)
        nodes += path if not nodes else path[1:]
        prev = path[-1]
    return nodes

def link(a, b, via):
    """Une dos trazos por la red de calles (pasando por via), prefiriendo avenidas."""
    pts = [a] + [om.nearest(om.xy(lon, lat)) for lat, lon in via] + [b]
    out = [a]
    for x, y in zip(pts, pts[1:]):
        out += route(x, np.array([y]), np.zeros(1), np.ones(len(U), bool), L * C ** LINK_EXP)[1:]
    return out

src = open(f'{SP}/cas.py').read().split('SPEC = json.load')[0]
ns = {}; exec(src, ns)
spec = json.load(open(f'{SP}/cas-spec.json'))
res_all = {}; TRACES = {}
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
            raw_tr = ns['trace'](page, anchors); TRACES.setdefault(rid, []).append(raw_tr)
            tr = [om.xy(lon, lat) for lon, lat in raw_tr]
            seg = corridor(tr, page)
            if nodes:
                vpts = []
                if via:
                    p2g, _ = pagetf.make(*pagetf.PAGES[via[0]])
                    vpts = [p2g(x, y) for x, y in via[1]]
                nodes += link(nodes[-1], seg[0], vpts)[1:-1]
            nodes += seg; via = None
        res[side] = om.clean(nodes, max_loop=400)
    if 'vuelta' not in res: res['vuelta'] = res['ida'][::-1]
    km = lambda s: sum(math.dist(coords[a], coords[b]) for a, b in zip(res[s], res[s][1:])) / 1000
    print(rid, 'ida km', round(km('ida'), 1), 'vuelta km', round(km('vuelta'), 1), file=sys.stderr)
    res_all[rid] = {s: [om.ll(*coords[n]) for n in res[s]] for s in ('ida', 'vuelta')}
json.dump(res_all, open(OUT, 'w')); json.dump(TRACES, open(OUT + '.traces', 'w'))
