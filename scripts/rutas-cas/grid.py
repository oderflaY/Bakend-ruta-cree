import sys
from PIL import Image, ImageDraw
im = Image.open(sys.argv[1]).convert('RGB')
x0, y0, x1, y1 = map(int, sys.argv[3].split(','))
im = im.crop((x0, y0, x1, y1))
d = ImageDraw.Draw(im)
step = int(sys.argv[4]) if len(sys.argv) > 4 else 50
for x in range((x0 // step + 1) * step, x1, step):
    d.line([(x - x0, 0), (x - x0, y1 - y0)], fill=(255, 0, 0) if x % (step*2) == 0 else (255, 160, 160), width=1)
    d.text((x - x0 + 2, 2), str(x), fill=(200, 0, 0))
for y in range((y0 // step + 1) * step, y1, step):
    d.line([(0, y - y0), (x1 - x0, y - y0)], fill=(0, 0, 255) if y % (step*2) == 0 else (160, 160, 255), width=1)
    d.text((2, y - y0 + 2), str(y), fill=(0, 0, 200))
im.save(sys.argv[2])
