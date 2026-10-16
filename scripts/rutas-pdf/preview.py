import json, sys, matplotlib
matplotlib.use("Agg")
import matplotlib.pyplot as plt
osm = json.load(open(sys.argv[1])); g = json.load(open(sys.argv[2])); raw = json.load(open(sys.argv[3]))
line = next(f for f in g['features'] if f['geometry']['type'] == 'LineString')['geometry']['coordinates']
xs, ys = zip(*line); pad = 0.006
x0, x1, y0, y1 = min(xs)-pad, max(xs)+pad, min(ys)-pad, max(ys)+pad
fig, ax = plt.subplots(figsize=(14, 11), dpi=90)
for w in osm['elements']:
    if w['type'] != 'way': continue
    c = [(p['lon'], p['lat']) for p in w['geometry'] if x0 < p['lon'] < x1 and y0 < p['lat'] < y1]
    if len(c) > 1: ax.plot(*zip(*c), color="#cccccc", lw=0.5, zorder=1)
ax.plot(*zip(*raw['ida']), color="#9ccc65", lw=6, alpha=.5, zorder=2, label="plano: ida")
ax.plot(*zip(*raw['vuelta']), color="#ffb74d", lw=6, alpha=.5, zorder=2, label="plano: vuelta")
ax.plot(xs, ys, color="#1565c0", lw=1.3, zorder=3, label="trazo final (calles)")
for f in g['features']:
    if f['geometry']['type'] == 'Point':
        x, y = f['geometry']['coordinates']; ax.plot(x, y, 'o', color="#d50000", ms=6, zorder=4)
        ax.annotate(f['properties']['id'].split('-')[-1], (x, y), fontsize=8, zorder=5)
ax.set_xlim(x0, x1); ax.set_ylim(y0, y1); ax.set_aspect(1/0.914); ax.legend(loc="lower right"); ax.set_title(g['features'][0]['properties']['name'])
plt.tight_layout(); plt.savefig(sys.argv[4])
