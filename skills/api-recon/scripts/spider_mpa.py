#!/usr/bin/env python3
"""
spider_mpa.py <BASE_URL> <OUTDIR> [--cookie "k=v; k2=v2"] [--max 200] [--depth 4]

Reference template — not a ready-made general tool. Adjust for the target before running: --exclude, cookie, depth/max, and other same-origin policies.

Fallback for NON-SPA targets (traditional server-rendered MPAs: Django/Rails/PHP/
JSP, classic admin panels). When there is no JS endpoint bundle, the API surface
lives in HTML <form action>, <a href>, and inline-JS ajax urls. This BFS-crawls
the same origin and extracts:
  - forms.txt : METHOD action  [param1, param2, ...]   (the real "endpoints")
  - links.txt : every same-origin URL reached
  - api_inline.txt : url-ish strings found in inline <script> / onclick (fetch/ajax)

Stdlib only. Same-origin, bounded, polite. Provide --cookie for an authed crawl.
"""
import sys, os, re, ssl, argparse, urllib.request, urllib.parse, time
from html.parser import HTMLParser
from collections import deque

CTX = ssl.create_default_context(); CTX.check_hostname = False; CTX.verify_mode = ssl.CERT_NONE
UA = "Mozilla/5.0 (spa-api-recon spider)"
SKIP_EXT = re.compile(r'\.(css|png|jpe?g|gif|svg|ico|woff2?|ttf|pdf|zip|mp4|webp|js)(\?|$)', re.I)
API_HINT = re.compile(r'/(?:api|rest|service|ajax|action|do|rpc|graphql|v\d|admin|backend)\b', re.I)

def fetch(url, cookie):
    h = {"User-Agent": UA}
    if cookie: h["Cookie"] = cookie
    try:
        with urllib.request.urlopen(urllib.request.Request(url, headers=h), context=CTX, timeout=20) as r:
            ct = r.headers.get("Content-Type", "")
            if "html" not in ct and "xml" not in ct: return "", ct
            return r.read().decode("utf-8", "ignore"), ct
    except Exception:
        return "", ""

class Page(HTMLParser):
    def __init__(self):
        super().__init__()
        self.links, self.scripts, self.inline = [], [], []
        self.forms, self._cur = [], None
        self._in_script = False
    def handle_starttag(self, tag, attrs):
        a = dict(attrs)
        if tag == "a" and a.get("href"): self.links.append(a["href"])
        elif tag == "script":
            self._in_script = True
            if a.get("src"): self.scripts.append(a["src"])
        elif tag == "form":
            self._cur = {"action": a.get("action", ""), "method": (a.get("method") or "GET").upper(), "params": []}
        elif tag in ("input", "select", "textarea", "button") and self._cur is not None:
            n = a.get("name")
            if n: self._cur["params"].append(n)
        # ajax-ish handlers
        for v in a.values():
            for m in re.findall(r'''["'](/[^"']{2,120})["']''', v or ""):
                if API_HINT.search(m): self.inline.append(m)
    def handle_endtag(self, tag):
        if tag == "script": self._in_script = False
        elif tag == "form" and self._cur is not None:
            self.forms.append(self._cur); self._cur = None
    def handle_data(self, data):
        if self._in_script and data:
            for m in re.findall(r'''["'`](/[A-Za-z0-9_\-./{}$:?=&]{2,120})["'`]''', data):
                if API_HINT.search(m): self.inline.append(m)

class _MpaCrawl:
    """Keep the BFS admission, page projection and text outputs in one run."""

    def __init__(self, args):
        self.args = args
        base = args.base if args.base.startswith("http") else "https://" + args.base
        os.makedirs(args.outdir, exist_ok=True)
        self.origin = "{0.scheme}://{0.netloc}".format(urllib.parse.urlparse(base))
        self.include = re.compile(args.include) if args.include else None
        self.exclude = re.compile(args.exclude, re.I) if args.exclude else None
        self.seen = {base.split("#")[0]}
        self.queue = deque([(base, 0)])
        self.forms, self.inline, self.links = {}, set(), set()
        self.count = 0

    def enqueue(self, url, depth, href):
        if href.startswith(("mailto:", "tel:", "javascript:", "#")):
            return
        target = urllib.parse.urljoin(url, href).split("#")[0]
        if not target.startswith(self.origin) or SKIP_EXT.search(target):
            return
        if self.exclude and self.exclude.search(target):
            return
        if self.include and not self.include.search(target):
            return
        if target not in self.seen:
            self.seen.add(target)
            self.queue.append((target, depth + 1))

    def visit(self, url, depth):
        html, content_type = fetch(url, self.args.cookie)
        if self.args.delay:
            time.sleep(self.args.delay)
        if not html:
            return
        page = Page()
        try:
            page.feed(html)
        except Exception:
            pass
        self.links.add(url.replace(self.origin, "") or "/")
        for form in page.forms:
            action = urllib.parse.urljoin(url, form["action"] or url)
            key = form["method"] + " " + action.replace(self.origin, "")
            self.forms.setdefault(key, set()).update(form["params"])
        self.inline.update(page.inline)
        if depth < self.args.depth:
            for href in page.links:
                self.enqueue(url, depth, href)

    def write(self):
        destination = self.args.outdir
        with open(os.path.join(destination, "forms.txt"), "w") as output:
            for key in sorted(self.forms):
                params = ", ".join(sorted(self.forms[key]))
                output.write(f"{key}  [{params}]\n")
        for filename, values in (("links.txt", self.links), ("api_inline.txt", self.inline)):
            open(os.path.join(destination, filename), "w").write("\n".join(sorted(values)))
        print(f"[+] crawled {self.count} pages (cap {self.args.max}, depth {self.args.depth})")
        print(f"[+] forms (endpoints): {len(self.forms)}  -> forms.txt")
        print(f"[+] inline ajax urls:  {len(self.inline)} -> api_inline.txt")
        print(f"[+] pages reached:     {len(self.links)} -> links.txt")
        if not self.args.cookie:
            print("[i] no --cookie: only public pages crawled. Pass an authed session cookie for the full surface.")

    def run(self):
        while self.queue and self.count < self.args.max:
            url, depth = self.queue.popleft()
            self.count += 1
            self.visit(url, depth)
        self.write()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("base"); ap.add_argument("outdir")
    ap.add_argument("--cookie", default=""); ap.add_argument("--max", type=int, default=200)
    ap.add_argument("--depth", type=int, default=4); ap.add_argument("--delay", type=float, default=0.1)
    ap.add_argument("--include", default=""); ap.add_argument("--exclude", default="logout|signout|delete|remove|destroy")
    _MpaCrawl(ap.parse_args()).run()


if __name__ == "__main__":
    main()
