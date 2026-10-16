import json, sys, math, unicodedata, numpy as np
from scipy.spatial import cKDTree
osm = json.load(open(sys.argv[1])); m = json.load(open(sys.argv[2])); stops = json.load(open(sys.argv[3])); tf = json.load(open(sys.argv[4])); pdf = json.load(open(sys.argv[5]))
rid, rname, color, out = sys.argv[6], sys.argv[7], sys.argv[8], sys.argv[9]
A, t = np.array(tf['A']), np.array(tf['t'])
def geo(p):
    q = A @ np.array([p[0], -p[1]]) + t
    return (tf['lon0'] + q[0]/tf['kx'], tf['lat0'] + q[1]/tf['ky'])
def d(a, b): return math.hypot((a[0]-b[0])*101700, (a[1]-b[1])*110540)
def clean(n):
    n = n.replace("General Guadalupe Victoria", "Guadalupe Victoria").replace("Ingeniero Pastor Rouaix", "Pastor Rouaix")
    for p in ("Calle ", "Avenida ", "Boulevard ", "Privada ", "Prolongación "):
        if n.startswith(p):
            return {"Avenida ": "Av. ", "Boulevard ": "Blvd. ", "Privada ": "Priv. ", "Prolongación ": "Prol. "}.get(p, "") + n[len(p):]
    return n
# named street points
pts, names = [], []
for w in osm['elements']:
    if w['type'] != 'way' or 'name' not in w.get('tags', {}): continue
    g = w['geometry']
    for i in range(len(g)-1):
        a, b = (g[i]['lon'], g[i]['lat']), (g[i+1]['lon'], g[i+1]['lat'])
        n = max(1, int(d(a, b)/5))
        for k in range(n):
            pts.append(((a[0]+(b[0]-a[0])*k/n)*101700, (a[1]+(b[1]-a[1])*k/n)*110540)); names.append(w['tags']['name'])
tree = cKDTree(pts)
def place_name(p):
    q = (p[0]*101700, p[1]*110540)
    dd, i = tree.query(q); main = names[i]
    idx = tree.query_ball_point(q, 120)
    cross = sorted({(math.dist(pts[j], q), names[j]) for j in idx if names[j] != main and clean(names[j]) != clean(main)})
    return clean(main) + (" y " + clean(cross[0][1]) if cross else "")
def despike(L, close=12, maxlen=350):
    """Cuts out short detours where the matched path leaves and comes back to the same spot."""
    L = [tuple(p) for p in L]
    changed = True
    while changed:
        changed = False
        i = 0
        while i < len(L) - 2:
            run = 0.0
            for j in range(i + 1, len(L)):
                run += d(L[j-1], L[j])
                if run > maxlen: break
                if j > i + 1 and d(L[i], L[j]) < close and run > 3 * close:
                    del L[i+1:j]; changed = True; break
            i += 1
    out = [L[0]]
    for p in L[1:]:
        if d(p, out[-1]) > 0.5: out.append(p)
    return out
# DESPIKE=0 for routes traced with corr.py: their out-and-back spurs (retornos) are real.
import os
_ds = despike if os.environ.get('DESPIKE', '1') != '0' else (lambda L: [tuple(p) for p in L])
ida = _ds(m['ida']['matched']); vuelta = _ds(m['vuelta']['matched'])
line = ida + vuelta[1:] if d(ida[-1], vuelta[0]) < 1 else ida + vuelta
cum = [0.0]
for a, b in zip(line, line[1:]): cum.append(cum[-1] + d(a, b))
ida_len = sum(d(a, b) for a, b in zip(ida, ida[1:]))
# raw drawn lines in pdf space to decide which side (ida/vuelta) each stop belongs to
def seg_dist(p, L):
    best = 1e9
    for a, b in zip(L, L[1:]):
        ax, ay = b[0]-a[0], b[1]-a[1]; L2 = ax*ax+ay*ay or 1e-9
        tt = max(0, min(1, ((p[0]-a[0])*ax + (p[1]-a[1])*ay)/L2))
        best = min(best, math.hypot(p[0]-a[0]-tt*ax, p[1]-a[1]-tt*ay))
    return best
def proj(p, lo, hi):
    best = None
    for i in range(len(line)-1):
        if cum[i] < lo - 1 or cum[i] > hi + 1: continue
        a, b = line[i], line[i+1]
        ax, ay = (b[0]-a[0])*101700, (b[1]-a[1])*110540; L2 = ax*ax+ay*ay or 1e-9
        px, py = (p[0]-a[0])*101700, (p[1]-a[1])*110540
        tt = max(0, min(1, (px*ax+py*ay)/L2))
        dist = math.hypot(px-tt*ax, py-tt*ay)
        if best is None or dist < best[0]:
            best = (dist, cum[i] + tt*math.sqrt(L2), (a[0]+(b[0]-a[0])*tt, a[1]+(b[1]-a[1])*tt))
    return best
rawI = [((x-tf['lon0'])*tf['kx'], (y-tf['lat0'])*tf['ky']) for x, y in json.load(open(sys.argv[10]))['ida']]
rawV = [((x-tf['lon0'])*tf['kx'], (y-tf['lat0'])*tf['ky']) for x, y in json.load(open(sys.argv[10]))['vuelta']]
placed = []
for s in stops:
    g = s['geo']
    q = ((g[0]-tf['lon0'])*tf['kx'], (g[1]-tf['lat0'])*tf['ky'])
    side_ida = seg_dist(q, rawI) <= seg_dist(q, rawV)
    lo, hi = (0, ida_len) if side_ida else (ida_len, cum[-1])
    dist, along, snapped = proj(g, lo, hi)
    placed.append((along, snapped, dist, "ida" if side_ida else "vuelta"))
# DIF (start) and CREE (turnaround) as stops too
placed.append((0.0, tuple(line[0]), 0, "ida"))
placed.append((ida_len, tuple(ida[-1]), 0, "ida"))
placed.sort()
# The DIF and the CREE are stops already: drop icons within 200 m of them (the other side of the street).
anchors = [line[0], ida[-1]]
dedup = []
for p in placed:
    if p[2] != 0 and any(d(p[1], a) < 200 for a in anchors): continue
    if dedup and abs(p[0] - dedup[-1][0]) < 60: continue
    dedup.append(p)
props = {"id": rid, "name": rname, "color": color, "fuente": "Plano oficial Ruta Azul DIF Estatal (agosto 2026), georreferenciado"}
if os.environ.get('DESTINO'): props["destino"] = os.environ['DESTINO']
feats = [{"type": "Feature", "properties": props,
          "geometry": {"type": "LineString", "coordinates": [[round(x, 6), round(y, 6)] for x, y in line]}}]
for n, (along, p, dist, side) in enumerate(dedup, 1):
    if along == 0.0: name = "DIF Estatal"
    elif abs(along - ida_len) < 1: name = os.environ.get('DESTINO') or 'CREE'
    else: name = place_name(p)
    feats.append({"type": "Feature", "properties": {"id": f"{rid}-p{n}", "name": name[:120], "accessible": True},
                  "geometry": {"type": "Point", "coordinates": [round(p[0], 6), round(p[1], 6)]}})
    print(f"p{n:02d} {side:6} {round(along):6}m off={round(dist):3}m  {name}")
json.dump({"type": "FeatureCollection", "features": feats}, open(out, "w"), ensure_ascii=False)
print("line pts", len(line), "length km", round(cum[-1]/1000, 1), "ida km", round(ida_len/1000, 1))
