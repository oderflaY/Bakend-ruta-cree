# ov.py osm.json plan.pdf tf.json m.json out.png [x0 y0 x1 y1 in plan px]  -> plan image georeferenced under the routed line
import json, sys, numpy as np, pymupdf, matplotlib
matplotlib.use("Agg"); import matplotlib.pyplot as plt
osm = json.load(open(sys.argv[1])); tf = json.load(open(sys.argv[3])); m = json.load(open(sys.argv[4])); out = sys.argv[5]
A, t = np.array(tf['A']), np.array(tf['t'])
def geo(px, py):
    q = A @ np.array([px*72/110, -py*72/110]) + t
    return (tf['lon0'] + q[0]/tf['kx'], tf['lat0'] + q[1]/tf['ky'])
box = list(map(float, sys.argv[6:10])) if len(sys.argv) > 9 else [60, 75, 830, 690]
Z = 3 if (box[2]-box[0]) > 400 else 6
pg = pymupdf.open(sys.argv[2])[0]; s = 72/110
pix = pg.get_pixmap(matrix=pymupdf.Matrix(Z*110/72, Z*110/72), clip=pymupdf.Rect(box[0]*s, box[1]*s, box[2]*s, box[3]*s))
img = np.frombuffer(pix.samples, np.uint8).reshape(pix.height, pix.width, pix.n)[:, :, :3]
(lx0, ly0), (lx1, ly1) = geo(box[0], box[1]), geo(box[2], box[3])
fig, ax = plt.subplots(figsize=(16, 16*(box[3]-box[1])/(box[2]-box[0])), dpi=70)
ax.imshow(img, extent=(lx0, lx1, ly1, ly0), alpha=0.55, zorder=0)
for w in osm['elements']:
    g = [(p['lon'], p['lat']) for p in w['geometry']]
    xs, ys = zip(*g)
    if max(xs) < lx0 or min(xs) > lx1 or max(ys) < ly1 or min(ys) > ly0: continue
    ax.plot(xs, ys, color="#555", lw=0.4, zorder=1)
for k, c in (('ida', '#0d47a1'), ('vuelta', '#b71c1c')):
    G = m[k]['matched']; ax.plot(*zip(*G), color=c, lw=1.6, zorder=3)
    for i, p in enumerate(m[k]['raw']):
        if lx0 <= p[0] <= lx1 and ly1 <= p[1] <= ly0:
            ax.plot(*p, 'o', color=c, ms=5, zorder=4); ax.annotate(f"{k[0]}{i}", p, fontsize=9, color=c, zorder=5)
ax.set_xlim(lx0, lx1); ax.set_ylim(ly1, ly0); ax.set_aspect(1/0.914); plt.tight_layout(); plt.savefig(out)
