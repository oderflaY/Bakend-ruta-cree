import json, sys, math, numpy as np
pdf = json.load(open(sys.argv[1])); tf = json.load(open(sys.argv[2])); frame = json.loads(sys.argv[3])
A, t = np.array(tf['A']), np.array(tf['t'])
def geo(p):  # pdf pt -> (lon, lat)
    q = A @ np.array([p[0], -p[1]]) + t
    return (tf['lon0'] + q[0]/tf['kx'], tf['lat0'] + q[1]/tf['ky'])
def segments(polys):
    segs = []
    for poly in polys:
        P = np.array(poly)
        c = P.mean(0)
        if not (frame[0] < c[0] < frame[2] and frame[1] < c[1] < frame[3]): continue
        U = np.unique(np.round(P, 3), axis=0)
        # principal axis
        w, v = np.linalg.eigh(np.cov((U - c).T)); ax = v[:, -1]
        proj = (U - c) @ ax
        segs.append((c + ax*proj.min(), c + ax*proj.max()))
    # orient consecutive segments
    out = []
    for i, (a, b) in enumerate(segs):
        if i == 0:
            nxt = segs[1]; d = lambda p: min(np.linalg.norm(p-nxt[0]), np.linalg.norm(p-nxt[1]))
            if d(a) < d(b): a, b = b, a
        else:
            if np.linalg.norm(a - out[-1][1]) > np.linalg.norm(b - out[-1][1]): a, b = b, a
        out.append((a, b))
    return out
res = {}
for k in ('ida', 'vuelta'):
    s = segments(pdf['route'][k])
    gaps = [np.linalg.norm(s[i][0]-s[i-1][1]) for i in range(1, len(s))]
    print(k, "segments", len(s), "max gap pt", round(max(gaps),1))
    line = [s[0][0]] + [p for a, b in s for p in (a, b)][1:]
    res[k] = [geo(p) for p in line]
json.dump(res, open(sys.argv[4], 'w'))
