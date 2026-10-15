import sys, json, pymupdf
doc = pymupdf.open(sys.argv[1]); page = doc[0]
out = {"route": {"ida": [], "vuelta": []}, "streets": [], "words": [], "stops": [], "marks": []}
def col(c): return tuple(round(v,2) for v in c) if c else None
for d in page.get_drawings():
    f, c, w = col(d.get("fill")), col(d.get("color")), d.get("width") or 0
    pts = []
    for it in d["items"]:
        if it[0] == "l": pts += [(it[1].x, it[1].y), (it[2].x, it[2].y)]
        elif it[0] == "c": pts += [(it[1].x, it[1].y), (it[4].x, it[4].y)]
        elif it[0] == "re": r = it[1]; pts += [(r.x0, r.y0), (r.x1, r.y1)]
        elif it[0] == "qu": q = it[1]; pts += [(q.ul.x,q.ul.y),(q.lr.x,q.lr.y)]
    if f == (0.18, 0.72, 0.0): out["route"]["ida"].append(pts)
    elif f == (0.87, 0.43, 0.0): out["route"]["vuelta"].append(pts)
    elif c == (0.73, 0.73, 0.73) and d["type"] == "s": out["streets"].append(pts)
    elif f in ((0.0, 0.22, 0.87),): out["stops"].append([d["rect"].x0, d["rect"].y0, d["rect"].x1, d["rect"].y1, f])
    elif f == (1.0, 0.0, 0.0) or c == (1.0,0.0,0.0): out["marks"].append([d["rect"].x0, d["rect"].y0, d["rect"].x1, d["rect"].y1])
for w in page.get_text("words"):
    out["words"].append([w[0], w[1], w[2], w[3], w[4]])
json.dump(out, open(sys.argv[2], "w"))
print({k: len(v) for k, v in out.items() if k != "route"}, len(out["route"]["ida"]), len(out["route"]["vuelta"]))
