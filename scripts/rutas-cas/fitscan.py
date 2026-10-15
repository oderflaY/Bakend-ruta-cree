import json, sys, math, numpy as np
# control points: [px, py, lat, lon]
cps = json.loads(sys.argv[1])
LAT0, LON0 = 25.55, -103.48
KX, KY = math.cos(math.radians(LAT0))*111320, 110540
P = np.array([[c[0], -c[1]] for c in cps], float)
Q = np.array([[(c[3]-LON0)*KX, (c[2]-LAT0)*KY] for c in cps])
# similarity: q = s*R*p + t  (solve as complex)
pc, qc = P.mean(0), Q.mean(0)
pz = (P-pc)[:,0] + 1j*(P-pc)[:,1]; qz = (Q-qc)[:,0] + 1j*(Q-qc)[:,1]
a = (np.conj(pz) @ qz) / (np.conj(pz) @ pz)
fit = lambda p: (a*((p[0]-pc[0]) + 1j*(-p[1]-pc[1])))
res = []
for c, q in zip(cps, Q):
    z = fit(c) ; x, y = z.real + qc[0], z.imag + qc[1]
    res.append(math.hypot(x-q[0], y-q[1]))
print("scale m/px", round(abs(a),2), "rot deg", round(math.degrees(np.angle(a)),2), "residuals m", [round(r) for r in res])
json.dump({"a": [a.real, a.imag], "pc": pc.tolist(), "qc": qc.tolist(), "lat0": LAT0, "lon0": LON0, "kx": KX, "ky": KY}, open(sys.argv[2], "w"))
