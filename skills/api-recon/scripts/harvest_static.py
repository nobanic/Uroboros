#!/usr/bin/env python3
"""
harvest_static.py <BASE_URL> <OUTDIR>

Reference template — not a ready-made general tool. Adjust for the target site before running; common changes:
  - the extract_endpoints regex (endpoint dialect)
  - webpack/Vite manifest parsing logic
  - micro-frontend publicPath, retry strategy

Static SPA bundle harvester. Framework-agnostic; tuned for webpack + Vite.
  1. Fetch entry HTML, collect script/module references.
  2. Parse the runtime chunk manifest(s) and download EVERY chunk (not just the
     ones in HTML), looping until no new chunk ids appear. Handles multiple
     micro-frontend runtimes, each with its own publicPath.
  3. Extract API endpoints and route paths from all downloaded JS.

Stdlib only. TLS verification is disabled (recon against self-signed/internal hosts).
"""
import sys, os, re, ssl, json, urllib.request, urllib.parse
from concurrent.futures import ThreadPoolExecutor

CTX = ssl.create_default_context(); CTX.check_hostname = False; CTX.verify_mode = ssl.CERT_NONE
UA = "Mozilla/5.0 (spa-api-recon)"

def fetch(url, binary=False):
    try:
        req = urllib.request.Request(url, headers={"User-Agent": UA})
        with urllib.request.urlopen(req, context=CTX, timeout=25) as r:
            data = r.read()
            return (data if binary else data.decode("utf-8", "ignore")), r.status
    except Exception as e:
        code = getattr(e, "code", 0)
        return "", code

def origin_of(u):
    p = urllib.parse.urlparse(u)
    return f"{p.scheme}://{p.netloc}"

# ---- chunk manifest parsing -------------------------------------------------
def matched_block(s, end_brace_idx):
    """Walk back from a '}' index to its matching '{' and return the object body."""
    depth = 0; j = end_brace_idx
    while j >= 0:
        if s[j] == '}': depth += 1
        elif s[j] == '{':
            depth -= 1
            if depth == 0: return s[j:end_brace_idx+1]
        j -= 1
    return ""

def parse_chunk_maps(js_text):
    """Return list of (publicPath_hint, {id: hash}) for every `}[x]+".js"` map
    (webpack __webpack_require__.u) found in the text."""
    maps = []
    # publicPath hints in this file: .p="..."  or publicPath="..."
    pubs = re.findall(r'(?:\.p|publicPath)\s*=\s*"([^"]*)"', js_text)
    for m in re.finditer(r'\}\[[A-Za-z_$]\]\s*\+\s*"\.js"', js_text):
        body = matched_block(js_text, m.start())
        pairs = re.findall(r'(\d+):"([0-9a-fA-F]{6,16})"', body)
        if pairs:
            maps.append((pubs, dict(pairs)))
    # Vite style: __vite__mapDeps map of "assets/xx.js"
    for fn in re.findall(r'"(assets/[^"]+\.js)"', js_text):
        maps.append((["/"], {"__vite__": fn}))
    return maps

def chunk_urls(base, js_text):
    """Yield absolute chunk URLs reconstructable from this file's manifest(s)."""
    origin = origin_of(base)
    out = set()
    for pubs, idmap in parse_chunk_maps(js_text):
        paths = pubs or ["/assets/", "/"]
        for cid, h in idmap.items():
            if cid == "__vite__":
                fn = h  # already "assets/xx.js"
                for pp in paths:
                    out.add(urllib.parse.urljoin(origin + "/", fn))
                continue
            for pp in paths:
                if pp.startswith("http"): bareorigin = ""; prefix = pp
                else: bareorigin = origin; prefix = pp if pp.startswith("/") else "/"+pp
                if not prefix.endswith("/"): prefix += "/"
                out.add(f"{bareorigin}{prefix}{cid}.{h}.js")
    return out

# ---- endpoint / route extraction -------------------------------------------
API_HINT = re.compile(r'/(?:api|rest|service|services|gateway|graphql|v\d|web|admin|backend|open)\b', re.I)
ASSET_EXT = re.compile(r'\.(js|css|png|jpe?g|svg|gif|woff2?|ttf|ico|map|json|mp4|webp)(\?|$)', re.I)

def extract_endpoints(js_text):
    paths = set()
    # quoted ('/...'), double-quoted, and backtick template paths
    for m in re.findall(r'''["'`](/[A-Za-z0-9_\-./{}$:]+)["'`]''', js_text):
        paths.add(m)
    # concatenation heads:  "/api/x/" + var
    for m in re.findall(r'''["'](/[A-Za-z0-9_\-./]+/)["']\s*\+''', js_text):
        paths.add(m)
    api, other = set(), set()
    for p in paths:
        if ASSET_EXT.search(p): continue
        if p.count('/') < 2 and not API_HINT.search(p): continue
        (api if API_HINT.search(p) else other).add(p)
    return api, other

def extract_routes(js_text):
    r = set()
    for key in ('path', 'to', 'redirect', 'href'):
        for m in re.findall(key + r'''\s*:\s*["'](/[A-Za-z0-9_\-/:]*)["']''', js_text):
            if not ASSET_EXT.search(m) and not API_HINT.search(m):
                r.add(m)
    return r

# ---- main -------------------------------------------------------------------
class _StaticHarvest:
    """Own one download graph and its static inventory, without companion files."""

    def __init__(self, base, outdir):
        self.base, self.outdir = base, outdir
        self.jsdir = os.path.join(outdir, "js")
        os.makedirs(self.jsdir, exist_ok=True)
        self.have = {}

    def download(self, url):
        filename = os.path.basename(urllib.parse.urlparse(url).path)
        if not filename.endswith(".js"):
            return None
        local = os.path.join(self.jsdir, filename)
        if url in self.have:
            return self.have[url]
        data, status = fetch(url, binary=True)
        if status == 200 and data and not data[:15].lstrip().startswith(b"<"):
            open(local, "wb").write(data)
            self.have[url] = local
            return local
        return None

    def download_batch(self, urls, workers):
        with ThreadPoolExecutor(max_workers=workers) as pool:
            list(pool.map(self.download, urls))

    def texts(self, paths):
        for local in paths:
            try:
                text = open(local, encoding="utf-8", errors="ignore").read()
            except:
                continue
            yield text

    def expand(self):
        seen = set(self.have)
        frontier = list(self.have.values())
        rounds = 0
        while frontier and rounds < 6:
            rounds += 1
            found = set()
            for text in self.texts(frontier):
                found.update(url for url in chunk_urls(self.base, text) if url not in seen)
            seen.update(found)
            if not found:
                break
            print(f"[*] round {rounds}: {len(found)} new chunk urls from manifest")
            previous = set(self.have.values())
            self.download_batch(found, 24)
            frontier = [local for local in self.have.values() if local not in previous]

    def retry_manifest(self):
        manifest = set()
        for local in list(self.have.values()):
            try:
                manifest.update(chunk_urls(self.base, open(local, encoding="utf-8", errors="ignore").read()))
            except:
                pass
        missing = [url for url in manifest if url not in self.have]
        if missing:
            self.download_batch(missing, 24)
            remaining = [url for url in manifest if url not in self.have]
            print(f"[*] manifest chunks: {len(manifest)} | downloaded {len(self.have)} | "
                  f"unreachable {len(remaining)} (CSS-only / undeployed)")
        return manifest

    def inventory(self, manifest):
        api, other, routes = set(), set(), set()
        for text in self.texts(self.have.values()):
            endpoints, paths = extract_endpoints(text)
            api.update(endpoints)
            other.update(paths)
            routes.update(extract_routes(text))
        outputs = (("api_static.txt", api), ("paths_other.txt", other), ("routes.txt", routes),
                   ("chunkmap.txt", sorted(os.path.basename(url) for url in manifest)))
        for filename, items in outputs:
            open(os.path.join(self.outdir, filename), "w").write("\n".join(sorted(items)))
        print(f"[+] api endpoints: {len(api)}  (api_static.txt)")
        print(f"[+] other paths:   {len(other)} (paths_other.txt)")
        print(f"[+] route paths:   {len(routes)} (routes.txt)")
        print(f"[+] outdir: {self.outdir}")
        print("\n[next] reverse the 3 gate facts (see reference.md), fill config.json, "
              "then: node runtime_harvest.js config.json")

    def run(self):
        print(f"[*] entry: {self.base}")
        html, status = fetch(self.base)
        open(os.path.join(self.outdir, "index.html"), "w").write(html)
        origin = origin_of(self.base)
        references = set(re.findall(r'<script[^>]+src="([^"]+\.js[^"]*)"', html))
        references.update(re.findall(r'(?:src|href)="([^"]*\.js)"', html))
        seed = {ref if ref.startswith("http") else urllib.parse.urljoin(self.base, ref)
                for ref in references}
        print(f"[*] {len(seed)} scripts referenced in HTML")
        self.download_batch(seed, 20)
        self.expand()
        manifest = self.retry_manifest()
        print(f"[+] total JS downloaded: {len(self.have)}")
        self.inventory(manifest)


def main():
    if len(sys.argv) < 3:
        print("usage: harvest_static.py <BASE_URL> <OUTDIR>"); sys.exit(1)
    base = sys.argv[1]
    if not base.startswith("http"):
        base = "https://" + base
    _StaticHarvest(base, sys.argv[2]).run()


if __name__ == "__main__":
    main()
