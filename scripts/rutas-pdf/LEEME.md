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
