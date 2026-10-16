# Rutas CAS (Gómez Palacio – Lerdo → CRIT) desde "Ruta azul Recorrido.pdf"

**3.ª versión (2026-10-13, publicada):** a pedido del usuario, en ciudad (páginas 1, 4, 5) la ruta ya NO
prefiere avenidas a toda costa: va por la calle más cercana al trazo del marcador (las CAS recogen
gente en sus casas). Costo por arista = longitud × clase^PEXP × (1 + (max(0, d − FREE)/SIG)²), con
d = distancia al trazo; `FREE=90 PEXP=3 SIG=45,45,40`. Así, si una avenida (Periférico, Blvd. México)
queda a <90 m del trazo gana ella; si no, la calle de colonia más cercana. Las páginas de carretera
(2, 3) siguen igual que la 2.ª versión. Esos son los valores por defecto de `corridor.py`.

Estado al 2026-10-13 (2.ª versión): `geojson/` y `seed/routes/cas-*.geojson`, 100 % sobre calles de OSM.
Lo aprendido al compararlo contra el documento:
- En ciudad (páginas 1, 4, 5) el marcador está corrido ~50–100 m de la avenida que marca (Periférico
  Ejército Mexicano, Blvd. México, Blvd. Miguel Alemán). Obligar a quedarse DENTRO del amarillo metía la
  ruta en escalones por la colonia de al lado ("sobre las casas"). Ahora el amarillo tiene tolerancia
  (`TOL`: 150 m en páginas 1 y 4, 70 m en la 5) y dentro de ella gana la avenida.
- En carretera (páginas 2 y 3, zoom 12) el escaneo está desfasado hasta ~750 m de la Carretera
  Durango–Torreón de OSM: ahí el amarillo no se usa como regla (TOL 5000), solo el trazo + la jerarquía.
- Los enlaces sin marcador (Ruta 3) van por avenidas (`LINK_EXP` 6). El documento NO marca cómo se unen
  los tramos de la Ruta 3 (Hospital General, la vuelta del Museo y la "V" de Blvd. México): pendiente de
  confirmar con el DIF.
`python fidelity.py <carpeta> geojson/cas-*.geojson` mide qué tanto se sale del amarillo;
`python maskview.py <página> geojson/cas-N.geojson out.png raw.json.traces cas-N` lo dibuja.

Cómo se ajusta (`corridor.py`): el trazo del marcador se parte en tramos (700 m en ciudad, 2.5 km en
carretera) y cada tramo se resuelve con camino más corto sobre la red de calles de OSM, solo dentro de
una franja alrededor del trazo, con costo por arista = longitud × clase⁴ (avenida 1 … residencial ~10)
× (1 + 0.3·(d/franja)²) + 200·(max(0, distancia fuera del amarillo − TOL) / 30 m)². El amarillo es la máscara
`rec/y-N.npy`: cualquier calle dentro del marcador vale, gana la de mayor jerarquía, y salirse cuesta
mucho. Cada tramo termina en la calle que el camino elija cerca del fin del tramo (no en un punto
fijo), así no salta entre calles paralelas. Los enlaces sin marcador ("link" en `cas-spec.json`)
van por la red completa prefiriendo avenidas, pasando por los puntos dados.

Quedan "escalones" cortos donde el marcador cruza en diagonal una cuadrícula (no hay calle recta ahí):
son calles reales. El enlace Hospital General → CRIT de la Ruta 3 (ida) no tiene marcador: va por el
mismo bulevar que el regreso (interpretación propia, pendiente de confirmar, igual que el orden de la
Ruta 3).

Cómo correr (venv con numpy scipy scikit-image matplotlib pillow), desde una carpeta con `*.py`,
`cas-spec.json`, `datos/osm-gp.json` y `datos/rec/` juntos (enlaces simbólicos):
1. `python corridor.py <carpeta> cas-raw.json` → recorridos por calle (y `cas-raw.json.traces`, el trazo)
2. `python cas-final.py cas-raw.json geojson` → GeoJSON con paradas provisionales
3. `python simplify.py 2 geojson/cas-*.geojson` → quita puntos alineados (tolerancia 2 m)
4. `python onroad.py osm-gp.json geojson/cas-*.geojson` → % fuera de calle (debe dar 0.0 %)
5. `python cas-preview.py <carpeta> geojson/cas-1.geojson 1 prev.png` → la ruta sobre el escaneo;
   `TR=cas-raw.json.traces RID=cas-1 python zoom.py <carpeta> 1 x0 y0 x1 y1 z.png geojson/cas-1.geojson`
   → acercamiento con calles de OSM por tipo y el trazo del marcador.

`osmmatch.py` (Viterbi / waypoints) queda como versión anterior; `corridor.py` importa de ahí el grafo.
Georreferencia de los escaneos (`pagetf.py`): centro y zoom del enlace de Google impreso; página 1 medida
(centro px 739,508; 15.07 m/px a z13; 150 dpi rotado −90°). Publicar: PUT /api/v1/admin/routes con cada
GeoJSON (ids cas-1, cas-2, cas-3).
