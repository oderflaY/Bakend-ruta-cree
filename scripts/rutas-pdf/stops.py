import json, sys, numpy as np
pdf = json.load(open(sys.argv[1])); tf = json.load(open(sys.argv[2])); frame = json.loads(sys.argv[3])
A, t = np.array(tf['A']), np.array(tf['t'])
def geo(p):
    q = A @ np.array([p[0], -p[1]]) + t
    return [round(tf['lon0'] + q[0]/tf['kx'], 6), round(tf['lat0'] + q[1]/tf['ky'], 6)]
pts = [((r[0]+r[2])/2, (r[1]+r[3])/2, r[2]-r[0], r[3]-r[1], tuple(r[4])) for r in pdf['stops']]
pts = [p for p in pts if frame[0] < p[0] < frame[2] and frame[1] < p[1] < frame[3]]
clusters = []
for p in pts:
    for c in clusters:
        if abs(c['x'] - p[0]) < 5 and abs(c['y'] - p[1]) < 5:
            c['m'].append(p); break
    else:
        clusters.append({'x': p[0], 'y': p[1], 'm': [p]})
for c in clusters:
    xs = [m[0] for m in c['m']]; ys = [m[1] for m in c['m']]
    c['x'], c['y'] = float(np.mean(xs)), float(np.mean(ys))
    c['size'] = max(max(m[2] for m in c['m']), max(m[3] for m in c['m']))
    c['geo'] = geo((c['x'], c['y']))
for c in clusters: print(round(c['x']), round(c['y']), len(c['m']), round(c['size'],1), set(m[4] for m in c['m']), c['geo'])
json.dump([{'pdf': [c['x'], c['y']], 'geo': c['geo'], 'n': len(c['m']), 'size': c['size']} for c in clusters], open(sys.argv[4], 'w'))
