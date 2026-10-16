# near.py osm.json lon lat [radius m] -> named ways passing within radius, nearest point and its distance
import json, sys, math
osm = json.load(open(sys.argv[1])); lon, lat = float(sys.argv[2]), float(sys.argv[3]); R = float(sys.argv[4]) if len(sys.argv) > 4 else 120
def d(a, b): return math.hypot((a[0]-b[0])*101700, (a[1]-b[1])*110540)
best = {}
for w in osm['elements']:
    g = [(p['lon'], p['lat']) for p in w['geometry']]
    for a, b in zip(g, g[1:]):
        ax, ay = (b[0]-a[0])*101700, (b[1]-a[1])*110540; L2 = ax*ax+ay*ay or 1e-9
        t = max(0, min(1, (((lon-a[0])*101700)*ax + ((lat-a[1])*110540)*ay)/L2))
        q = (a[0]+(b[0]-a[0])*t, a[1]+(b[1]-a[1])*t); dd = d((lon, lat), q)
        k = (w.get('tags', {}).get('name', '?'), w['tags'].get('highway'), w['tags'].get('oneway', ''))
        if dd < R and (k not in best or dd < best[k][0]): best[k] = (dd, q)
for k, (dd, q) in sorted(best.items(), key=lambda x: x[1][0]): print(f"{round(dd):4d} m  {q[0]:.6f},{q[1]:.6f}  {k}")
