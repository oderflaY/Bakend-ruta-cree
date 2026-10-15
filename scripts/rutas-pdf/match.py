import json, sys, math, time, urllib.request, urllib.error
raw = json.load(open(sys.argv[1])); reverse_vuelta = sys.argv[2] == 'rev'; out = sys.argv[3]
def dist(a, b): return math.hypot((a[0]-b[0])*101700, (a[1]-b[1])*110540)
def densify(L, step=55):
    o = [L[0]]
    for a, b in zip(L, L[1:]):
        n = max(1, int(dist(a, b)/step))
        o += [(a[0]+(b[0]-a[0])*k/n, a[1]+(b[1]-a[1])*k/n) for k in range(1, n+1)]
    return o
def match(pts):
    res = []
    for i in range(0, len(pts)-1, 8):
        chunk = pts[i:i+10]
        if len(chunk) < 2: break
        coords = ";".join(f"{x:.6f},{y:.6f}" for x, y in chunk)
        url = f"https://router.project-osrm.org/match/v1/driving/{coords}?overview=full&geometries=geojson&radiuses={';'.join(['40']*len(chunk))}&gaps=ignore&tidy=true"
        d = {}
        for attempt in range(4):
            try:
                d = json.load(urllib.request.urlopen(urllib.request.Request(url, headers={"User-Agent": "rutacree-dev"}), timeout=60)); break
            except urllib.error.HTTPError as e:
                d = json.loads(e.read() or b"{}"); print("http", e.code, d.get("message")); time.sleep(3)
            except Exception as e:
                print("err", e); time.sleep(3)
        if d.get("code") != "Ok":
            print("match failed, keeping drawn points", i, d.get("code")); geom = [list(p) for p in chunk]
        else:
            geom = [c for m in d["matchings"] for c in m["geometry"]["coordinates"]]
        # cut overlap: keep matched points up to the projection of chunk point 80
        if i + 10 < len(pts):
            cut = pts[i+8]; j = min(range(len(geom)), key=lambda k: dist(geom[k], cut)); geom = geom[:j+1]
        if res and geom: 
            j = min(range(min(len(geom), 25)), key=lambda k: dist(geom[k], res[-1])); geom = geom[j+1:]
        res += geom
        time.sleep(0.4)
    return res
result = {}
for k in ('ida', 'vuelta'):
    L = raw[k][::-1] if (k == 'vuelta' and reverse_vuelta) else raw[k]
    D = densify(L)
    M = match(D)
    dev = max(min(dist(p, q) for q in M) for p in D) if M else None
    rawlen = sum(dist(a, b) for a, b in zip(L, L[1:])); mlen = sum(dist(a, b) for a, b in zip(M, M[1:])) if M else 0
    print(k, "raw len m", round(rawlen), "matched len m", round(mlen), "max deviation m", round(dev) if dev else None)
    result[k] = {"raw": L, "matched": M}
json.dump(result, open(out, 'w'))
