import sys, numpy as np
from PIL import Image
im = np.asarray(Image.open(sys.argv[1]).convert('RGB')).astype(float) / 255
r, g, b = im[..., 0], im[..., 1], im[..., 2]
mx, mn = im.max(-1), im.min(-1)
sat = (mx - mn) / np.maximum(mx, 1e-6)
# highlighter yellow: red and green high, blue clearly lower
mask = (r > 0.55) & (g > 0.5) & (b < 0.75 * np.minimum(r, g)) & (sat > 0.25) & (np.abs(r - g) < 0.25)
out = np.zeros_like(im); out[mask] = [0, 0, 0]; out[~mask] = [1, 1, 1]
Image.fromarray((out * 255).astype('uint8')).save(sys.argv[2])
np.save(sys.argv[3], mask)
print('yellow px', int(mask.sum()))
