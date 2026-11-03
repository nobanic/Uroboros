#!/usr/bin/env python3
"""
extract_route_map.py <JSDIR> <OUTDIR>

Scan downloaded JS chunks for routeMap / routeLink style objects:
  KEY:{name:"...",link:"/path"}

Writes route_map.json — used by build_perm_tree.py to resolve alias refs.
"""
import re, json, os, sys, glob

class _RouteCatalog:
    def __init__(self):
        self.routes, self.source = {}, None
        self.pattern = re.compile(r'([A-Z_][A-Z0-9_]*):\{name:"([^"]*)",link:"([^"]+)"')

    def scan(self, directory):
        for candidate in glob.glob(os.path.join(directory, '*.js')):
            try:
                text = open(candidate, encoding='utf-8', errors='ignore').read()
            except Exception:
                continue
            hits = self.pattern.findall(text)
            # Selection deliberately compares raw hits with the distinct-key catalog.
            if len(hits) > len(self.routes):
                self.routes = {key: {'name': name, 'link': link} for key, name, link in hits}
                self.source = candidate
        return self

    def write(self, directory):
        if not self.routes:
            print('[!] no routeMap pattern found — widen regex or grep manually')
            sys.exit(1)
        destination = os.path.join(directory, 'route_map.json')
        json.dump(self.routes, open(destination, 'w', encoding='utf-8'), ensure_ascii=False, indent=2)
        print(f'[+] {len(self.routes)} routes from {os.path.basename(self.source)} -> {destination}')


def main():
    if len(sys.argv) < 3:
        print("usage: extract_route_map.py <JSDIR> <OUTDIR>")
        sys.exit(1)
    directory = sys.argv[2]
    os.makedirs(directory, exist_ok=True)
    _RouteCatalog().scan(sys.argv[1]).write(directory)


if __name__ == '__main__':
    main()
