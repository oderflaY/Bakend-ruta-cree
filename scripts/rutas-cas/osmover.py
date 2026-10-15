import json, sys, matplotlib
matplotlib.use('Agg')
import matplotlib.pyplot as plt
from PIL import Image
sys.path.insert(0, sys.argv[1]); import pagetf
page = int(sys.argv[2]); _, g2p = pagetf.make(*pagetf.PAGES[page])
osm = json.load(open(f'{sys.argv[1]}/osm-gp.json'))
nd = {e['id']: (e['lat'], e['lon']) for e in osm['elements'] if e['type'] == 'node'}
im = Image.open(f'{sys.argv[1]}/rec/n-{page}.png')
fig, ax = plt.subplots(figsize=(16, 12.4), dpi=80); ax.imshow(im); ax.axis('off')
col = {'trunk': 'red', 'primary': 'red', 'secondary': 'orange', 'tertiary': 'green', 'motorway': 'red'}
for w in osm['elements']:
    if w['type'] != 'way': continue
    hw = w.get('tags', {}).get('highway', '')
    c = col.get(hw.replace('_link', ''))
    if not c and page in (1, 4, 5) and hw in ('residential', 'unclassified'): c = '#3949ab'
    if not c: continue
    xy = [g2p(*nd[n]) for n in w['nodes'] if n in nd]
    ax.plot([p[0] for p in xy], [p[1] for p in xy], color=c, lw=0.8 if c == '#3949ab' else 1.4, alpha=0.6)
ax.set_xlim(0, im.width); ax.set_ylim(im.height, 0); plt.tight_layout(); plt.savefig(sys.argv[3])
