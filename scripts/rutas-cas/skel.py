import sys, numpy as np
from scipy import ndimage
from skimage.morphology import skeletonize, binary_closing, disk, remove_small_objects
m = np.load(sys.argv[1])
m = binary_closing(m, disk(6))
m = remove_small_objects(m, 400)
sk = skeletonize(m)
np.save(sys.argv[2], sk)
nb = ndimage.convolve(sk.astype(int), np.ones((3, 3), int), mode='constant') - 1
ys, xs = np.nonzero(sk & (nb == 1))
print('endpoints', sorted(zip(xs.tolist(), ys.tolist())))
ys, xs = np.nonzero(sk & (nb >= 3))
pts = sorted(zip(xs.tolist(), ys.tolist()))
# cluster junction pixels
cl = []
for p in pts:
    if not cl or abs(p[0]-cl[-1][0]) + abs(p[1]-cl[-1][1]) > 12: cl.append(p)
print('junctions', cl)
lab, n = ndimage.label(sk, structure=np.ones((3,3)))
print('components', n, [int((lab==i).sum()) for i in range(1, n+1)])
