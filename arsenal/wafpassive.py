"""Network-free WAFW00F response-header fingerprinting.

The upstream CLI and package are deliberately not imported.  This module loads
the exact vendored 2.4.2 plugin sources only after a strict AST and corpus hash
gate, then exposes a matcher with normal-response header/cookie methods only.
"""

import ast
import hashlib
import json
import re
import unicodedata
from functools import lru_cache
from pathlib import Path


VERSION = "2.4.2"
TAG_COMMIT = "e81d38748109a805872ee92fd2377612bd639b3b"
SDIST_SHA256 = "05e4a49f6de433793ad506e37ae2c6f537c832959ee6e2bbdf573900f22194ae"
CORPUS_SHA256 = "fb296e4c6368b668b8c2ae31fecc19c311ddce0644a933becabf79a9a4ce5b9c"
PROFILE_REVISION = "wafw00f-passive-response-headers-v1"
CORPUS_PATH = Path(__file__).with_name("wafw00f") / "plugins-2.4.2.json"
PLUGIN_COUNT = 172
MAX_HEADER_FIELDS = 128
MAX_HEADER_VALUE_BYTES = 16 * 1024
MAX_HEADER_TOTAL_BYTES = 64 * 1024
MAX_MATCHES = 32

_RUN_ID = re.compile(r"^[A-Za-z0-9._-]{1,128}$")
_HEADER_NAME = re.compile(r"^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")
_PLUGIN_NAME = re.compile(r"^[a-z0-9_]+[.]py$")
_FUNCTION_NAME = re.compile(r"^[a-z][a-z0-9_]*$")
_SELF_MATCHERS = {"matchHeader", "matchCookie", "matchContent", "matchStatus", "matchReason"}
_ALLOWED_AST = {
    ast.Module, ast.Expr, ast.Assign, ast.FunctionDef, ast.arguments, ast.arg,
    ast.If, ast.Return, ast.Call, ast.Attribute, ast.Name, ast.Load, ast.Store,
    ast.Constant, ast.Tuple, ast.UnaryOp, ast.Not, ast.BoolOp, ast.And, ast.Or,
    ast.keyword,
}


def _canonical_json(value):
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=False)


def _sha256_text(value):
    return hashlib.sha256(value.encode("utf-8")).hexdigest()


def _clean_text(value, label, maximum=256):
    if not isinstance(value, str) or value != value.strip() or not value:
        raise ValueError("%s is missing or not normalized" % label)
    if unicodedata.normalize("NFC", value) != value or len(value.encode("utf-8")) > maximum:
        raise ValueError("%s is not normalized" % label)
    if any(unicodedata.category(ch) == "Cc" for ch in value):
        raise ValueError("%s contains control characters" % label)
    return value


def normalize_headers(value):
    """Validate the host-normalized, body-free response-header projection."""
    if not isinstance(value, dict):
        raise ValueError("response headers must be an object")
    normalized = {}
    fields = 0
    total = 0
    for name in sorted(value):
        if not isinstance(name, str) or name != name.lower() or not _HEADER_NAME.fullmatch(name):
            raise ValueError("response header name is not canonical")
        values = value[name]
        if not isinstance(values, list) or not values:
            raise ValueError("response header values must be non-empty arrays")
        clean_values = []
        for raw in values:
            if not isinstance(raw, str):
                raise ValueError("response header value must be text")
            encoded = raw.encode("utf-8")
            if len(encoded) > MAX_HEADER_VALUE_BYTES:
                raise ValueError("response header value exceeds the passive profile limit")
            if any(unicodedata.category(ch) == "Cc" for ch in raw):
                raise ValueError("response header value contains control characters")
            fields += 1
            total += len(name.encode("ascii")) + 2 + len(encoded) + 1
            if fields > MAX_HEADER_FIELDS or total > MAX_HEADER_TOTAL_BYTES:
                raise ValueError("response headers exceed the passive profile limit")
            clean_values.append(raw)
        normalized[name] = clean_values
    return normalized


def header_sha256(headers):
    """Stable cross-language hash; NUL is safe because controls are rejected."""
    digest = hashlib.sha256()
    for name in sorted(headers):
        digest.update(name.encode("ascii"))
        digest.update(b"\0")
        for value in headers[name]:
            digest.update(value.encode("utf-8"))
            digest.update(b"\0")
        digest.update(b"\0")
    return digest.hexdigest()


def _literal_string(node, label):
    if not isinstance(node, ast.Constant) or not isinstance(node.value, str):
        raise ValueError("%s must be a string literal" % label)
    return node.value


def _literal_bool(node, label):
    if not isinstance(node, ast.Constant) or type(node.value) is not bool:
        raise ValueError("%s must be a boolean literal" % label)
    return node.value


def _validate_matcher_call(call, filename):
    if not isinstance(call.func, ast.Attribute) or not isinstance(call.func.value, ast.Name) or call.func.value.id != "self":
        return False
    method = call.func.attr
    if method not in _SELF_MATCHERS:
        raise ValueError("%s calls an unknown self matcher" % filename)
    if len(call.args) != 1 or len(call.keywords) > 1:
        raise ValueError("%s uses an invalid matcher signature" % filename)
    attack = False
    if call.keywords:
        keyword = call.keywords[0]
        if keyword.arg != "attack" or method not in {"matchHeader", "matchCookie"}:
            raise ValueError("%s uses an invalid matcher keyword" % filename)
        attack = _literal_bool(keyword.value, "attack")
    argument = call.args[0]
    if method == "matchHeader":
        if not isinstance(argument, ast.Tuple) or len(argument.elts) != 2:
            raise ValueError("%s uses an invalid header matcher" % filename)
        _literal_string(argument.elts[0], "header name")
        pattern = _literal_string(argument.elts[1], "header regex")
        re.compile(pattern, re.I)
    elif method in {"matchCookie", "matchContent", "matchReason"}:
        pattern = _literal_string(argument, "matcher pattern")
        if method != "matchReason":
            re.compile(pattern, re.I)
    elif method == "matchStatus":
        if not isinstance(argument, ast.Constant) or type(argument.value) is not int:
            raise ValueError("%s uses a non-integer status matcher" % filename)
    if attack and method not in {"matchHeader", "matchCookie"}:
        raise ValueError("%s uses an unsupported attack matcher" % filename)
    return True


def _validate_expression(node, functions, filename):
    if isinstance(node, ast.Constant):
        if type(node.value) is not bool:
            raise ValueError("%s uses a non-boolean expression constant" % filename)
        return
    if isinstance(node, ast.UnaryOp) and isinstance(node.op, ast.Not):
        _validate_expression(node.operand, functions, filename)
        return
    if isinstance(node, ast.BoolOp) and isinstance(node.op, (ast.And, ast.Or)):
        for value in node.values:
            _validate_expression(value, functions, filename)
        return
    if isinstance(node, ast.Call):
        if _validate_matcher_call(node, filename):
            return
        if (isinstance(node.func, ast.Name) and node.func.id in functions and
                len(node.args) == 1 and isinstance(node.args[0], ast.Name) and
                node.args[0].id == "self" and not node.keywords):
            return
    raise ValueError("%s contains an unsupported expression" % filename)


def _validate_statements(statements, functions, filename):
    if not statements:
        raise ValueError("%s contains an empty function body" % filename)
    for statement in statements:
        if isinstance(statement, ast.Return):
            if not isinstance(statement.value, ast.Constant) or type(statement.value.value) is not bool:
                raise ValueError("%s returns a non-boolean value" % filename)
        elif isinstance(statement, ast.If):
            _validate_expression(statement.test, functions, filename)
            _validate_statements(statement.body, functions, filename)
            if statement.orelse:
                _validate_statements(statement.orelse, functions, filename)
        else:
            raise ValueError("%s contains an unsupported function statement" % filename)


def validate_plugin_source(filename, source):
    if not _PLUGIN_NAME.fullmatch(filename) or not isinstance(source, str):
        raise ValueError("invalid WAFW00F plugin entry")
    try:
        tree = ast.parse(source, filename=filename)
    except SyntaxError as exc:
        raise ValueError("%s does not parse" % filename) from exc
    if any(type(node) not in _ALLOWED_AST for node in ast.walk(tree)):
        raise ValueError("%s contains an unsupported AST node" % filename)
    assignments = []
    functions = {}
    for index, node in enumerate(tree.body):
        if isinstance(node, ast.Expr):
            if index != 0 or not isinstance(node.value, ast.Constant) or not isinstance(node.value.value, str):
                raise ValueError("%s contains a non-docstring top-level expression" % filename)
        elif isinstance(node, ast.Assign):
            if len(node.targets) != 1 or not isinstance(node.targets[0], ast.Name) or node.targets[0].id != "NAME":
                raise ValueError("%s contains an unsupported top-level assignment" % filename)
            assignments.append(_literal_string(node.value, "plugin NAME"))
        elif isinstance(node, ast.FunctionDef):
            valid_return_annotation = (node.returns is None or
                                       (isinstance(node.returns, ast.Name) and node.returns.id == "bool"))
            if (not _FUNCTION_NAME.fullmatch(node.name) or node.decorator_list or not valid_return_annotation or
                    node.args.posonlyargs or len(node.args.args) != 1 or node.args.args[0].arg != "self" or
                    node.args.vararg is not None or node.args.kwarg is not None or node.args.kwonlyargs or
                    node.args.defaults or node.args.kw_defaults):
                raise ValueError("%s contains an unsupported function definition" % filename)
            if node.name in functions:
                raise ValueError("%s defines a duplicate function" % filename)
            functions[node.name] = node
        else:
            raise ValueError("%s contains unsupported top-level code" % filename)
    if len(assignments) != 1 or "is_waf" not in functions:
        raise ValueError("%s is missing NAME or is_waf" % filename)
    name = _clean_text(assignments[0], "plugin NAME", 256)
    for function in functions.values():
        _validate_statements(function.body, functions, filename)
    return tree, name


def _split_vendor(name):
    matched = re.fullmatch(r"(.+?)\s+\(([^()]*)\)", name)
    if matched:
        return matched.group(1).strip(), matched.group(2).strip()
    return name, ""


@lru_cache(maxsize=4)
def load_corpus(path=CORPUS_PATH):
    path = Path(path)
    try:
        document = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeError, json.JSONDecodeError) as exc:
        raise ValueError("WAFW00F corpus is unavailable") from exc
    if not isinstance(document, dict) or set(document) != {"format_version", "plugins", "source"}:
        raise ValueError("WAFW00F corpus envelope is invalid")
    expected_source = {
        "version": VERSION, "tag_commit": TAG_COMMIT,
        "sdist_sha256": SDIST_SHA256, "corpus_sha256": CORPUS_SHA256,
    }
    if document["format_version"] != 1 or document["source"] != expected_source:
        raise ValueError("WAFW00F corpus provenance is invalid")
    entries = document["plugins"]
    if not isinstance(entries, list) or len(entries) != PLUGIN_COUNT:
        raise ValueError("WAFW00F corpus must contain exactly 172 plugins")
    digest = hashlib.sha256()
    loaded = []
    filenames = []
    for entry in entries:
        if not isinstance(entry, dict) or set(entry) != {"filename", "source"}:
            raise ValueError("WAFW00F corpus plugin entry is invalid")
        filename = entry["filename"]
        source = entry["source"]
        if not isinstance(filename, str) or not isinstance(source, str):
            raise ValueError("WAFW00F corpus plugin entry is invalid")
        filenames.append(filename)
        raw = source.encode("utf-8")
        digest.update(filename.encode("utf-8"))
        digest.update(b"\0")
        digest.update(raw)
        digest.update(b"\0")
        tree, name = validate_plugin_source(filename, source)
        namespace = {"__builtins__": {}, "bool": bool}
        exec(compile(tree, filename, "exec"), namespace)  # exact hash + strict AST gate above
        vendor, manufacturer = _split_vendor(name)
        loaded.append((filename, vendor, manufacturer, namespace["is_waf"]))
    if filenames != sorted(filenames) or len(set(filenames)) != PLUGIN_COUNT:
        raise ValueError("WAFW00F plugins are not uniquely sorted")
    if digest.hexdigest() != CORPUS_SHA256:
        raise ValueError("WAFW00F plugin corpus hash mismatch")
    return tuple(loaded)


def provenance(path=CORPUS_PATH):
    load_corpus(path)
    return {"version": VERSION, "artifact_sha256": SDIST_SHA256, "binary_sha256": CORPUS_SHA256}


class _PassiveMatcher:
    def __init__(self, headers):
        self.headers = headers
        self.evidence = set()

    def matchHeader(self, headermatch, attack=False):
        if attack:
            return False
        name, pattern = headermatch
        values = self.headers.get(str(name).lower(), ())
        for value in values:
            if re.search(pattern, value, re.I):
                self.evidence.add("cookie" if str(name).lower() == "set-cookie" else "header")
                return True
        return False

    def matchCookie(self, pattern, attack=False):
        if attack:
            return False
        for value in self.headers.get("set-cookie", ()):
            if re.search(pattern, value, re.I):
                self.evidence.add("cookie")
                return True
        return False

    def matchContent(self, _pattern, attack=True):
        return False

    def matchStatus(self, _status, attack=True):
        return False

    def matchReason(self, _reason, attack=True):
        return False


def evaluate(headers, path=CORPUS_PATH):
    headers = normalize_headers(headers)
    matches = []
    for filename, vendor, manufacturer, detector in load_corpus(path):
        matcher = _PassiveMatcher(headers)
        if detector(matcher) is True and matcher.evidence:
            matches.append({
                "tool": "wafw00f", "type": "waf_fingerprint",
                "plugin_id": filename, "vendor": vendor,
                "manufacturer": manufacturer,
                "evidence_kinds": sorted(matcher.evidence),
            })
            if len(matches) > MAX_MATCHES:
                raise ValueError("passive WAF match count exceeds the fixed limit")
    return matches


def _parse_json_object(raw, label):
    if not isinstance(raw, str):
        raise ValueError("%s is missing" % label)
    try:
        value = json.loads(raw)
    except json.JSONDecodeError as exc:
        raise ValueError("%s is invalid" % label) from exc
    if not isinstance(value, dict):
        raise ValueError("%s must be an object" % label)
    return value


def run(task_id, run_id, endpoint_id, source_traffic_id, scope_json, scope_hash,
        config_json, config_hash, headers_json, response_header_sha256,
        corpus_path=CORPUS_PATH):
    if type(task_id) is not int or task_id <= 0 or type(endpoint_id) is not int or endpoint_id <= 0:
        raise ValueError("task and endpoint ids must be positive integers")
    if not isinstance(run_id, str) or not _RUN_ID.fullmatch(run_id):
        raise ValueError("run id is invalid")
    traffic_id = _clean_text(source_traffic_id, "source traffic id", 256)
    if not isinstance(scope_hash, str) or not re.fullmatch(r"[0-9a-f]{64}", scope_hash):
        raise ValueError("scope hash is invalid")
    scope = _parse_json_object(scope_json, "scope")
    if set(scope) != {"cidrs", "exact_hosts", "root_domains"} or any(
            not isinstance(scope[key], list) or not all(isinstance(item, str) for item in scope[key])
            for key in scope):
        raise ValueError("scope projection is invalid")
    if _sha256_text(_canonical_json(scope)) != scope_hash:
        raise ValueError("scope hash mismatch")
    if not isinstance(config_hash, str) or not re.fullmatch(r"[0-9a-f]{64}", config_hash):
        raise ValueError("config hash is invalid")
    config = _parse_json_object(config_json, "config")
    expected_config = {
        "corpus_sha256": CORPUS_SHA256,
        "endpoint_id": endpoint_id,
        "profile_revision": PROFILE_REVISION,
        "response_header_sha256": response_header_sha256,
        "sdist_sha256": SDIST_SHA256,
        "source_traffic_id": traffic_id,
        "tag_commit": TAG_COMMIT,
        "tool_version": VERSION,
    }
    if config != expected_config or _sha256_text(_canonical_json({
            "config": config, "target": str(endpoint_id), "tool": "wafw00f",
    })) != config_hash:
        raise ValueError("passive WAF config or hash mismatch")
    headers = normalize_headers(_parse_json_object(headers_json, "response headers"))
    calculated_hash = header_sha256(headers)
    if (not isinstance(response_header_sha256, str) or
            response_header_sha256 != calculated_hash):
        raise ValueError("response header hash mismatch")
    records = evaluate(headers, corpus_path)
    return {
        "tool": "wafw00f", "exit_state": "ok" if records else "ok_empty",
        "complete": True, "records": records, "task_id": task_id,
        "run_id": run_id, "scope_hash": scope_hash, "config_hash": config_hash,
        "authority": "candidate_or_hypothesis_only",
        "stats": {
            "evaluated_plugins": PLUGIN_COUNT, "match_count": len(records),
            "profile_revision": PROFILE_REVISION,
            "response_header_sha256": calculated_hash,
            "target_requests": 0,
        },
    }
