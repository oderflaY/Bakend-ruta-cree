"""Qué tanto se sale la ruta del marcador amarillo: % de la longitud a >X m del amarillo, por página."""
import json, math, sys, numpy as np, pagetf
from scipy.ndimage import distance_transform_edt
SP = sys.argv[1]
PAGES = {'cas-1': [1, 2], 'cas-2': [4, 3], 'cas-3': [5]}  # la primera manda donde se traslapan
E = {}
def edt(p):
    if p not in E:
        m = np.load(f'{SP}/rec/y-{p}.npy'); lat_c, lon_c, z = pagetf.PAGES[p]
        E[p] = (distance_transform_edt(~m) * pagetf.S13 * 2 ** (13 - z), pagetf.make(*pagetf.PAGES[p])[1], m.shape)
    return E[p]
for f in sys.argv[2:]:
    g = json.load(open(f)); rid = g['features'][0]['properties']['id']
    line = g['features'][0]['geometry']['coordinates']
    KX, KY = math.cos(math.radians(25.53)) * 111320, 110540
    pts = []
    for a, b in zip(line, line[1:]):
        L = math.hypot((b[0]-a[0])*KX, (b[1]-a[1])*KY); n = max(1, int(L / 5))
        pts += [(a[0]+(b[0]-a[0])*k/n, a[1]+(b[1]-a[1])*k/n) for k in range(n)]
    d = []
    bad = []
    for lon, lat in pts:
        best = None
        for p in PAGES[rid]:
            e, g2p, sh = edt(p); x, y = g2p(lat, lon); xi, yi = int(round(x)), int(round(y))
            if 0 <= xi < sh[1] and 0 <= yi < sh[0] and (best is None or e[yi, xi] < best[0]):
                best = (e[yi, xi], p, xi, yi)
        if best:
            d.append(best[0])
            if best[0] > 80: bad.append(best[1:])
    d = np.array(d)
    print(rid, f'muestras {len(d)}  fuera del amarillo >40 m: {(d>40).mean()*100:.1f}%  >80 m: {(d>80).mean()*100:.1f}%  >200 m: {(d>200).mean()*100:.1f}%  máx {d.max():.0f} m')
    # tramos malos agrupados
    seen = []
    for p, x, y in bad:
        if not any(q == p and abs(x - a) < 25 and abs(y - b) < 25 for q, a, b in seen): seen.append((p, x, y))
    print('   zonas fuera (página, x, y):', seen[:30])
