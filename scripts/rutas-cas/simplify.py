"""Quita puntos casi alineados (Douglas–Peucker, tolerancia en metros) sin sacar la línea de la calle."""
import json, math, sys
tol = float(sys.argv[1])
KX, KY = math.cos(math.radians(25.53)) * 111320, 110540
def rdp(P, eps):
    keep = [False] * len(P); keep[0] = keep[-1] = True; stack = [(0, len(P) - 1)]
    while stack:
        i, j = stack.pop()
        (ax, ay), (bx, by) = P[i], P[j]; dx, dy = bx - ax, by - ay; L = math.hypot(dx, dy) or 1e-9
        best, k = -1, -1
        for m in range(i + 1, j):
            px, py = P[m]
            t = max(0, min(1, ((px-ax)*dx + (py-ay)*dy) / (L*L)))
            d = math.hypot(px - ax - t*dx, py - ay - t*dy)
            if d > best: best, k = d, m
        if best > eps: keep[k] = True; stack += [(i, k), (k, j)]
    return [p for p, f in zip(P, keep) if f]
for f in sys.argv[2:]:
    g = json.load(open(f)); line = g['features'][0]['geometry']['coordinates']
    P = [(x * KX, y * KY) for x, y in line]
    out = rdp(P, tol)
    g['features'][0]['geometry']['coordinates'] = [[round(x / KX, 6), round(y / KY, 6)] for x, y in out]
    json.dump(g, open(f, 'w'), ensure_ascii=False)
    print(f, len(line), '->', len(out))
