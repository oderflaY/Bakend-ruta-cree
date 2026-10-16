# Rutas desde los planos PDF del DIF

Convierte un plano "RUTA AZUL n DIF ESTATAL DGO.pdf" (vectorial) en el GeoJSON que publica el panel.
Así se hicieron azul-3 y azul-4 (octubre 2026).

Requisitos (en un venv): `pip install pymupdf numpy scipy scikit-image matplotlib`

1. Calles de OpenStreetMap (Overpass; si responde 406/504, reintentar):
   `curl --data-urlencode 'data=[out:json][timeout:180];way["highway"~"motorway|trunk|primary|secondary|tertiary|unclassified|residential|living_street|.*_link"](23.96,-104.76,24.12,-104.54);out tags geom;' https://overpass-api.de/api/interpreter -o osm.json`
2. `python extract.py plano.pdf rN.json` — ida (verde), vuelta (naranja), paradas (icono azul), calles y textos.
3. Georreferenciar con 3–4 cruces conocidos (pixeles del plano a 110 dpi → lat, lon), refinado con los
   nombres de calle: `python georef2.py osm.json rN.json '[[px,py,lat,lon],...]' '[35,50,540,455]' tfN.json sim`
   (`osmtool.py osm.json inter "calle a & calle b"` da la coordenada de un cruce). Debe salir ~70 % de
   etiquetas a <40 m de su calle.
4. `python build.py rN.json tfN.json '[35,50,540,455]' rawN.json` y `python stops.py rN.json tfN.json '[35,50,540,455]' stN.json`
5. Ajustar a las calles con OSRM: `python match.py rawN.json keep|rev mN.json` (rev si la vuelta viene al revés: debe ir CREE → DIF).
6. `python final.py osm.json mN.json stN.json tfN.json rN.json azul-N "Ruta N - Nombre" "#RRGGBB" azul-N.geojson rawN.json`
7. Revisar con `python preview.py osm.json azul-N.geojson <raw ida/vuelta> prevN.png` y publicar
   (PUT /api/v1/admin/routes).

## Rutas 1, 2 y 5 (octubre 2026): trazo por corredor

El ajuste con OSRM (`match.py`) hacía zigzags en estas rutas (calles divididas, nombres distintos en OSM:
el Blvd. Guadiana es "Av. Sahuatoba" y la Carr. al Mezquital es "Blvd. IPN"). Se trazaron así:

1. Pasos 1–3 igual (extract + georef2; los `tfN.json` quedaron en `specs/`).
2. `specs/specN.json`: puntos de paso en orden según el ITINERARIO del plano, para ida y vuelta.
   Cada punto es `{"p": [px, py]}` (pixeles del plano a 110 dpi) o `{"geo": [lon, lat]}`; `"s"` = regex
   del nombre de calle donde debe caer; `"d": true` = tramo recto copiado del plano (calles que OSM no tiene).
3. `python corr.py osm.json specs/specN.json rN.json cN.json` — camino por la red de calles entre los
   puntos, donde cada calle cuesta más mientras más lejos queda de la franja dibujada en el plano (así
   respeta el dibujo y no "corta camino"). `near.py osm.json lon lat` lista las calles cercanas a un punto.
4. Revisar sobre el plano: `python ov.py osm.json plano.pdf specs/tfN.json cN.json out.png [x0 y0 x1 y1]`.
5. `final.py` con `DESPIKE=0` (los retornos son reales) y, en la ruta 5, que no termina en el CREE,
   `DESTINO="CAM Joyas del Valle"`. `cN.json` tiene el mismo formato que la salida de `match.py`; el
   último argumento (líneas para elegir el lado de cada parada) es `{"ida": cN.ida.matched, "vuelta": …}`.
