import json, math, sys
# Every print comes from the same Google Maps window: centre pixel and m/px at zoom 13 measured on page 1.
CX, CY, S13 = 739, 508, 15.072
def make(lat_c, lon_c, z):
    s = S13 * 2 ** (13 - z)
    kx, ky = math.cos(math.radians(lat_c)) * 111320, 110540
    def px2geo(x, y):
        return (lat_c - (y - CY) * s / ky, lon_c + (x - CX) * s / kx)
    def geo2px(lat, lon):
        return (CX + (lon - lon_c) * kx / s, CY - (lat - lat_c) * ky / s)
    return px2geo, geo2px
PAGES = {1: (25.5590111, -103.4644118, 13), 2: (25.5104631, -103.6174099, 12), 3: (25.5104631, -103.6174099, 12),
         4: (25.5590111, -103.4644118, 13), 5: (25.5610311, -103.4785196, 14)}
if __name__ == '__main__':
    page = int(sys.argv[1]); _, g2p = make(*PAGES[page])
    for name, lat, lon in [("Sanatorio", 25.5663737, -103.5007695), ("Torre Eiffel", 25.5498559, -103.4790447), ("Parque Victoria", 25.5423422, -103.5231483)]:
        print(name, [round(v) for v in g2p(lat, lon)])
