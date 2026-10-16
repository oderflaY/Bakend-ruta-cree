import json, math, sys
raw = json.load(open(sys.argv[1])); outdir = sys.argv[2]
def dist(a, b): return math.hypot((a[0]-b[0])*100500, (a[1]-b[1])*110540)
CRIT = 'CRIT Teletón (Blvd. México)'
SPEC = {
 'cas-1': ('Ruta 1 CAS - La Loma - Lerdo - CRIT', '#F4511E', [('La Loma', 25.4638515, -103.6728113), ('Juan E. García', 25.4915120, -103.6907855),
     ('León Guzmán', 25.4998581, -103.6598253), ('Seis de Enero', 25.5202607, -103.6015190), ('Raymundo', 25.5112090, -103.5431279),
     ('Lerdo, Plaza de Armas', 25.5364882, -103.5242803), ('Gómez Palacio Centro (Sanatorio San José)', 25.5663737, -103.5007695)]),
 'cas-2': ('Ruta 2 CAS - Ciudad Juárez - Lerdo - CRIT', '#8E24AA', [('Ciudad Juárez', 25.4920965, -103.5920291), ('Carlos Real', 25.5058995, -103.5199498),
     ('Lerdo, Plaza de Armas', 25.5364882, -103.5242803), ('Gómez Palacio Centro (Sanatorio San José)', 25.5663737, -103.5007695)]),
 'cas-3': ('Ruta 3 CAS - Gómez Palacio Centro - CRIT', '#00897B', [('Lerdo, Plaza de Armas', 25.5364882, -103.5242803),
     ('Sanatorio San José', 25.5663737, -103.5007695), ('Central de Autobuses Gómez Palacio', 25.5639080, -103.4764695), ('Torre Eiffel Gómez Palacio', 25.5498559, -103.4790447)]),
}
for rid, (name, color, places) in SPEC.items():
    ida, vuelta = raw[rid]['ida'], raw[rid]['vuelta']
    line = ida + (vuelta[1:] if dist(ida[-1], vuelta[0]) < 5 else vuelta)
    cum = [0.0]
    for a, b in zip(line, line[1:]): cum.append(cum[-1] + dist(a, b))
    ida_len = sum(dist(a, b) for a, b in zip(ida, ida[1:]))
    # Routes that come back the same way: their stops go on the way out (ida).
    limit = ida_len + 1 if rid in ('cas-1', 'cas-2') else cum[-1] + 1
    def proj(p):
        best = None
        for i in range(len(line) - 1):
            if cum[i] > limit: break
            a, b = line[i], line[i+1]
            ax, ay = (b[0]-a[0])*100500, (b[1]-a[1])*110540; L2 = ax*ax + ay*ay or 1e-9
            px, py = (p[0]-a[0])*100500, (p[1]-a[1])*110540
            t = max(0, min(1, (px*ax + py*ay)/L2)); d = math.hypot(px - t*ax, py - t*ay)
            if best is None or d < best[0]: best = (d, cum[i] + t*math.sqrt(L2), (a[0]+(b[0]-a[0])*t, a[1]+(b[1]-a[1])*t))
        return best
    first = places[0][0] if dist(line[0], (places[0][2], places[0][1])) < 1500 else 'Inicio de ruta'
    stops = [(0.0, line[0], first), (ida_len, tuple(ida[-1]), CRIT)]
    for nm, lat, lon in places:
        d, along, p = proj((lon, lat))
        if d < 1200: stops.append((along, p, nm))
        else: print('  lejos de la ruta:', nm, round(d), 'm')
    stops.sort(key=lambda s: s[0])
    dedup = []
    for s in stops:
        if dedup and (abs(s[0]-dedup[-1][0]) < 150 or any(s[2] == d[2] for d in dedup)): continue
        dedup.append(s)
    feats = [{'type': 'Feature', 'properties': {'id': rid, 'name': name, 'color': color,
              'fuente': 'Recorrido marcado a mano sobre Google Maps (Ruta azul Recorrido.pdf), ajustado a las calles'},
              'geometry': {'type': 'LineString', 'coordinates': [[round(x, 6), round(y, 6)] for x, y in line]}}]
    for n, (along, p, nm) in enumerate(dedup, 1):
        feats.append({'type': 'Feature', 'properties': {'id': f'{rid}-p{n}', 'name': nm, 'accessible': True},
                      'geometry': {'type': 'Point', 'coordinates': [round(p[0], 6), round(p[1], 6)]}})
        print(f'  {rid} p{n:02d} {round(along/1000,1):5} km  {nm}')
    json.dump({'type': 'FeatureCollection', 'features': feats}, open(f'{outdir}/{rid}.geojson', 'w'), ensure_ascii=False)
    print(rid, name, 'km', round(cum[-1]/1000, 1), 'paradas', len(dedup))
