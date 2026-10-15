import json, sys, math, unicodedata, numpy as np
from scipy.spatial import cKDTree
def norm(s): return unicodedata.normalize('NFKD', s).encode('ascii','ignore').decode().upper().strip('.,()')
osm = json.load(open(sys.argv[1])); pdf = json.load(open(sys.argv[2])); cps = json.loads(sys.argv[3]); frame = json.loads(sys.argv[4]); outp = sys.argv[5]
LAT0, LON0 = 24.03, -104.65; KX, KY = math.cos(math.radians(LAT0))*111320, 110540
xy = lambda lat, lon: np.array([(lon-LON0)*KX, (lat-LAT0)*KY])
STOP = {"CALLE","AVENIDA","BOULEVARD","PRIVADA","PROLONGACION","GENERAL","PROFESOR","DE","DEL","LA","LAS","LOS","EL"}
# named OSM points and per-token index
pts, toks = [], []
allp = []
for w in osm['elements']:
    if w['type'] != 'way': continue
    g = w['geometry']; name = norm(w.get('tags', {}).get('name', ''))
    tk = set(name.split()) - STOP
    for i in range(len(g)-1):
        a, b = xy(g[i]['lat'], g[i]['lon']), xy(g[i+1]['lat'], g[i+1]['lon'])
        n = max(1, int(np.linalg.norm(b-a)/4))
        for k in range(n):
            p = a + (b-a)*k/n; allp.append(p)
            if tk: pts.append(p); toks.append(tk)
pts = np.array(pts); allp = np.array(allp)
tree = cKDTree(pts); atree = cKDTree(allp)
# PDF labels in frame
labels = []
for x0, y0, x1, y1, word in pdf['words']:
    word = norm(word)
    if len(word) >= 5 and word not in STOP and frame[0] < x0 < frame[2] and frame[1] < y0 < frame[3]:
        labels.append((np.array([(x0+x1)/2, -(y0+y1)/2]), word))
SIM = len(sys.argv) > 6
def fit(src, dst):
    if not SIM:
        X = np.c_[src, np.ones(len(src))]; sol, *_ = np.linalg.lstsq(X, dst, rcond=None); return sol[:2].T, sol[2]
    n = len(src); M = np.zeros((2*n, 3)); M[0::2, 0] = src[:, 0]; M[1::2, 0] = src[:, 1]; M[0::2, 1] = 1; M[1::2, 2] = 1
    sol, *_ = np.linalg.lstsq(M, dst.reshape(-1), rcond=None)
    return np.eye(2)*sol[0], sol[1:]
# initial affine from control points (pdf pt coords, y negated)
P = np.array([[c[0]*72/110, -c[1]*72/110] for c in cps]); Q = np.array([xy(c[2], c[3]) for c in cps])
A, t = None, None
A, t = fit(P, Q)
print("cp residuals m", np.round(np.linalg.norm(P @ A.T + t - Q, axis=1)))
def label_pass(A, t, radius):
    src, dst, hits = [], [], 0
    for p, word in labels:
        q = A @ p + t
        idx = tree.query_ball_point(q, radius)
        cand = [i for i in idx if word in toks[i]]
        if cand:
            hits += 1
            j = min(cand, key=lambda i: np.linalg.norm(pts[i]-q))
            src.append(p); dst.append(pts[j])
    return hits, np.array(src), np.array(dst)
for radius in [400, 300, 200, 150, 100, 80, 60, 60]:
    hits, s, d = label_pass(A, t, radius)
    A, t = fit(s, d)
    # robust: drop worst 20%
    r = np.linalg.norm(s @ A.T + t - d, axis=1); keep = r <= np.percentile(r, 80)
    A, t = fit(s[keep], d[keep])
    print(f"radius {radius}: labels matched {hits}/{len(labels)} median resid {np.median(r):.1f}m")
hits, _, _ = label_pass(A, t, 40)
print(f"final: labels within 40m of their street {hits}/{len(labels)}")
json.dump({"A": A.tolist(), "t": t.tolist(), "lat0": LAT0, "lon0": LON0, "kx": KX, "ky": KY}, open(outp, "w"))
