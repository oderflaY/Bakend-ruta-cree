import json, sys, matplotlib
matplotlib.use('Agg')
import matplotlib.pyplot as plt
from PIL import Image
sys.path.insert(0, sys.argv[1]); import pagetf
# zoom.py SP page x0 y0 x1 y1 out.png route.geojson...  (x,y en pixeles del escaneo rec/n-page.png)
SP, page = sys.argv[1], int(sys.argv[2]); x0, y0, x1, y1 = map(float, sys.argv[3:7]); out = sys.argv[7]
_, g2p = pagetf.make(*pagetf.PAGES[page])
osm = json.load(open(f'{SP}/osm-gp.json'))
nd = {e['id']: (e['lat'], e['lon']) for e in osm['elements'] if e['type'] == 'node'}
im = Image.open(f'{SP}/rec/n-{page}.png')
fig, ax = plt.subplots(figsize=(14, 14 * (y1 - y0) / (x1 - x0)), dpi=80); ax.imshow(im); ax.axis('off')
col = {'trunk': 'red', 'primary': 'red', 'motorway': 'red', 'secondary': 'orange', 'tertiary': 'green', 'unclassified': '#3949ab', 'residential': '#3949ab'}
for w in osm['elements']:
    if w['type'] != 'way': continue
    hw = w.get('tags', {}).get('highway', '').replace('_link', '')
    c = col.get(hw)
    if not c: continue
    xy = [g2p(*nd[n]) for n in w['nodes'] if n in nd]
    if not any(x0 <= x <= x1 and y0 <= y <= y1 for x, y in xy): continue
    ax.plot([p[0] for p in xy], [p[1] for p in xy], color=c, lw=0.7 if c == '#3949ab' else 1.5, alpha=0.5)
    nm = w.get('tags', {}).get('name')
    if nm and c != '#3949ab':
        mx, my = xy[len(xy)//2]
        if x0 <= mx <= x1 and y0 <= my <= y1: ax.text(mx, my, nm, fontsize=7, color=c)
for f, cc in zip(sys.argv[8:], ['#d50000', '#000000']):
    line = json.load(open(f))['features'][0]['geometry']['coordinates']
    xy = [g2p(lat, lon) for lon, lat in line]
    ax.plot([p[0] for p in xy], [p[1] for p in xy], color=cc, lw=2.2, alpha=0.85)
import os
if os.environ.get('TR'):
    for t in json.load(open(os.environ['TR']))[os.environ['RID']]:
        xy = [g2p(lat, lon) for lon, lat in t]
        ax.plot([p[0] for p in xy], [p[1] for p in xy], color='#00c853', lw=1.5, ls='--')
ax.set_xlim(x0, x1); ax.set_ylim(y1, y0); plt.tight_layout(); plt.savefig(out)
