import json, sys, matplotlib
matplotlib.use('Agg')
import matplotlib.pyplot as plt
from PIL import Image
sys.path.insert(0, sys.argv[1]); import pagetf
# Draw the final line back onto the scan, to compare with the highlighter.
g = json.load(open(sys.argv[2])); page = int(sys.argv[3])
_, g2p = pagetf.make(*pagetf.PAGES[page])
im = Image.open(f'{sys.argv[1]}/rec/n-{page}.png')
fig, ax = plt.subplots(figsize=(16, 12.4), dpi=80); ax.imshow(im); ax.axis('off')
line = g['features'][0]['geometry']['coordinates']
xy = [g2p(lat, lon) for lon, lat in line]
ax.plot([p[0] for p in xy], [p[1] for p in xy], color='#d50000', lw=1.6)
for f in g['features'][1:]:
    lon, lat = f['geometry']['coordinates']; x, y = g2p(lat, lon)
    ax.plot(x, y, 'o', color='#1565c0', ms=7)
ax.set_xlim(0, im.width); ax.set_ylim(im.height, 0)
plt.tight_layout(); plt.savefig(sys.argv[4])
