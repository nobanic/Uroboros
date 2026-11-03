#!/usr/bin/env python3
"""
build_perm_tree.py <JSDIR> <OUTDIR> [--config recon/config.json]

Rebuild a permissive permission/menu stub from frontend auth modules.

Typical consumer chain (TDP / enterprise admin SPAs):
  POST /api/web/user/role_permissions  -> { permissions: [...], role_type }
  POST /api/web/permissions/all        -> tree[{ code, position, children }]
  getResultTree(tree, permissions)     -> menu include lists
  userRouteAuth[code].url              -> frontend route path

This script:
  1. Locates userRouteAuth={MONITOR:{url:...},...} in js/
  2. Resolves webpack alias refs (He=o.DASHBOARD) via route_map.json
  3. Infers hierarchy from code prefixes (MONITOR_ -> MONITOR)
  4. Writes permissions_tree.json, permissions_all_stub.json,
     role_permissions_stub.json, userRouteAuth.json
  5. Optionally patches config.json stubs (permissions/all + role_permissions)

Adjust ROOTS / PREFIX_PARENT / EXTRA_PARENT per target if heuristics miss nodes.
"""
import re, json, os, sys, glob, argparse

DEFAULT_ROOTS = [
    'MONITOR', 'THREAT', 'ASSETS_RISK', 'INVESTIGATION', 'MANAGEMENT',
    'AGENT_EVIDENCE', 'MDR', 'PLATFORM',
]
DEFAULT_PREFIX_PARENT = {
    'MONITOR_': 'MONITOR', 'THREAT_': 'THREAT', 'ASSETS_RISK_': 'ASSETS_RISK',
    'INVESTIGATION_': 'INVESTIGATION', 'MANAGEMENT_': 'MANAGEMENT', 'MDR_': 'MDR',
    'PLATFORM_': 'PLATFORM', 'CUSTOM_': 'PLATFORM', 'FALSE_': 'PLATFORM',
}
DEFAULT_EXTRA_PARENT = {
    'LINKAGE_DISPOSAL': 'MANAGEMENT',
    'ASSETS_RISK_API': 'ASSETS_RISK',
    'ASSETS_RISK_WEAK_PWD': 'ASSETS_RISK',
}


def find_auth_file(jsdir):
    best_fp, best_len = None, 0
    for fp in glob.glob(os.path.join(jsdir, '*.js')):
        try:
            txt = open(fp, encoding='utf-8', errors='ignore').read()
        except Exception:
            continue
        if 'userRouteAuth' not in txt:
            continue
        m = re.search(r'userRouteAuth=\{MONITOR:', txt)
        if m and len(txt) > best_len:
            best_fp, best_len = fp, len(m.group(0))
        elif 'userRouteAuth' in txt and best_fp is None:
            best_fp = fp
    return best_fp


def parse_aliases(chunk):
    aliases = {}
    for m in re.finditer(r'([a-zA-Z_$][\w$]*)=o\.([A-Z_0-9]+)\b', chunk[:8000]):
        aliases[m.group(1)] = m.group(2)
    return aliases


def resolve_ref(token, aliases, route_map):
    token = token.strip()
    if token.startswith('"'):
        return json.loads(token)
    if token in aliases:
        key = aliases[token]
        return route_map.get(key, {}).get('link', key)
    return token


def parse_user_route_auth(txt, route_map):
    m = re.search(r'(?:t\.)?userRouteAuth=(\{MONITOR:.*?\})\},', txt, re.S)
    if not m:
        m = re.search(r'userRouteAuth=(\{[A-Z_0-9]+:\{url:', txt)
        if not m:
            return None, {}
        # greedy fallback — trim at next webpack module
        body = m.group(1)
        end = body.rfind('}')
        obj_src = body[: end + 1] if end > 0 else body
    else:
        obj_src = m.group(1)

    chunk_start = txt.find('userRouteAuth')
    chunk = txt[chunk_start:chunk_start + 35000]
    aliases = parse_aliases(chunk)

    entries = {}
    for em in re.finditer(
        r'([A-Z_0-9]+):\{url:([^,}]+)(?:,control:(\[.*?\]|[^,}]+))?\}', obj_src
    ):
        key = em.group(1)
        url = resolve_ref(em.group(2).strip(), aliases, route_map)
        controls = []
        if em.group(3):
            raw = em.group(3).strip()
            vars_ = re.findall(r'([A-Za-z_$][\w$]*)', raw) if raw.startswith('[') else [raw]
            controls = [aliases.get(v, v) for v in vars_]
        entries[key] = {'code': key, 'url': url, 'control': controls}
    return obj_src, entries


def parent_of(code, roots, prefix_parent, extra_parent):
    if code in extra_parent:
        return extra_parent[code]
    if code in roots:
        return None
    for pref, par in prefix_parent.items():
        if code.startswith(pref):
            return par
    return None


def build_tree(entries, roots, prefix_parent, extra_parent):
    children_of = {k: [] for k in entries}
    for code in entries:
        p = parent_of(code, roots, prefix_parent, extra_parent)
        if p:
            children_of.setdefault(p, []).append(code)

    def make_node(code):
        node = {
            'code': code,
            'position': 'top' if code in roots else 'left',
            'name': code,
        }
        kids = sorted(children_of.get(code, []))
        if kids:
            node['children'] = [make_node(c) for c in kids]
        return node

    tree = [make_node(r) for r in roots if r in entries or children_of.get(r)]
    orphans = [c for c in entries if parent_of(c, roots, prefix_parent, extra_parent) is None and c not in roots]
    for code in sorted(orphans):
        tree.append(make_node(code))
    return tree


def flat_codes(nodes):
    out = []
    for n in nodes:
        out.append(n['code'])
        out.extend(flat_codes(n.get('children', [])))
    return out


class _PermissionArtifacts:
    """Own one ordered permission artifact set and its optional config patch."""

    def __init__(self, entries, role):
        self.entries = entries
        self.tree = build_tree(entries, DEFAULT_ROOTS, DEFAULT_PREFIX_PARENT, DEFAULT_EXTRA_PARENT)
        self.codes = sorted(set(flat_codes(self.tree) + [code for entry in entries.values()
                                                      for code in entry.get('control', [])]))
        self.permission_stub = self.envelope(self.tree)
        self.role_stub = self.envelope({'role_type': role, 'permissions': self.codes})

    @staticmethod
    def envelope(data):
        return {'response_code': 0, 'verbose_msg': 'ok', 'data': data}

    def write(self, directory):
        outputs = (('userRouteAuth.json', self.entries), ('permissions_tree.json', self.tree),
                   ('perm_codes_all.txt', '\n'.join(self.codes)),
                   ('permissions_all_stub.json', self.permission_stub),
                   ('role_permissions_stub.json', self.role_stub))
        for filename, value in outputs:
            path = os.path.join(directory, filename)
            if isinstance(value, str):
                open(path, 'w', encoding='utf-8').write(value)
            else:
                json.dump(value, open(path, 'w', encoding='utf-8'), ensure_ascii=False, indent=2)
        print(f'[+] entries={len(self.entries)} codes={len(self.codes)} tree_roots={len(self.tree)}')

    def patch_config(self, path):
        if not os.path.exists(path):
            return
        config = json.load(open(path, encoding='utf-8'))
        retained = [stub for stub in config.get('stubs', [])
                    if not re.search(r'permissions/all|role_permissions', stub.get('match', ''))]
        config['stubs'] = [{'match': 'permissions/all', 'body': self.permission_stub},
                           {'match': 'role_permissions', 'body': self.role_stub}] + retained
        json.dump(config, open(path, 'w', encoding='utf-8'), ensure_ascii=False, indent=2)
        print(f'[+] patched stubs -> {path}')

    @classmethod
    def from_directory(cls, args):
        os.makedirs(args.outdir, exist_ok=True)
        map_path = os.path.join(args.outdir, 'route_map.json')
        if os.path.exists(map_path):
            routes = json.load(open(map_path, encoding='utf-8'))
        else:
            print('[*] route_map.json missing — run extract_route_map.py first')
            routes = {}
        auth_file = find_auth_file(args.jsdir)
        if not auth_file:
            print('[!] userRouteAuth module not found in js/')
            sys.exit(1)
        print(f'[*] auth module: {os.path.basename(auth_file)}')
        text = open(auth_file, encoding='utf-8', errors='ignore').read()
        _, entries = parse_user_route_auth(text, routes)
        if not entries:
            print('[!] failed to parse userRouteAuth object — adjust regex in script')
            sys.exit(1)
        return cls(entries, args.role)


def main():
    ap = argparse.ArgumentParser(description='Build permission tree stubs from JS auth modules')
    ap.add_argument('jsdir')
    ap.add_argument('outdir')
    ap.add_argument('--config', help='patch stubs into config.json')
    ap.add_argument('--role', default='SUPER_ADMIN', help='role_type in role_permissions stub')
    args = ap.parse_args()
    artifacts = _PermissionArtifacts.from_directory(args)
    artifacts.write(args.outdir)
    artifacts.patch_config(args.config or os.path.join(args.outdir, 'config.json'))


if __name__ == '__main__':
    main()
