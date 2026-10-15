import numpy as np, json, sys, pagetf
from PIL import Image, ImageDraw
# maskview.py page route.geojson out.png [traces.json rid]
page, f, out = int(sys.argv[1]), sys.argv[2], sys.argv[3]
m = np.load(f'rec/y-{page}.npy'); im = Image.open(f'rec/n-{page}.png').convert('RGB'); a = np.asarray(im).copy(); a[m] = [255, 0, 255]
im = Image.fromarray(a); dr = ImageDraw.Draw(im); _, g2p = pagetf.make(*pagetf.PAGES[page])
if len(sys.argv) > 5:
    for t in json.load(open(sys.argv[4]))[sys.argv[5]]:
        dr.line([g2p(lat, lon) for lon, lat in t], fill=(0, 200, 80), width=2)
line = json.load(open(f))['features'][0]['geometry']['coordinates']
dr.line([g2p(lat, lon) for lon, lat in line], fill=(0, 0, 0), width=3)
im.crop((0, 150, 1601, 1100)).save(out)
