"""Observed HTTP exchanges -> OpenAPI shapes with immutable traffic provenance.

No examples, credentials, required JSON fields or access policy are inferred.
Value matches are dependency candidates, never proven producer relationships.
"""
import copy
import hashlib
import re
from urllib.parse import parse_qs, unquote, urlsplit

from api_lab.engine import digest, origin


FORMAT = "uroboros-traffic-schema-v1"
METHODS = {"get", "post", "put", "patch", "delete", "head", "options"}
SECRET_NAMES = {"authorization", "cookie", "set_cookie", "password", "token",
                "access_token", "refresh_token", "api_key", "apikey", "client_secret",
                "session", "sessionid", "csrf_token", "auth"}
UUID = re.compile(r"[0-9a-fA-F]{8}(?:-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}\Z")
PATH_PARAMETER = re.compile(r"[A-Za-z_][A-Za-z0-9_]*\Z")
AUTOMATIC_PATH_PARAMETER = re.compile(r"path_[0-9]+\Z")


def sensitive(name):
    return name.lower().replace("-", "_") in SECRET_NAMES


def shape(value, exchange_id=None):
    result = _shape(value, exchange_id)
    if exchange_id:
        result["x-traffic-evidence"] = [exchange_id]
    return result


def _shape(value, exchange_id):
    if value is None:
        return {"x-observed-null": True}
    if isinstance(value, bool):
        return {"type": "boolean"}
    if isinstance(value, int):
        return {"type": "integer"}
    if isinstance(value, float):
        return {"type": "number"}
    if isinstance(value, str):
        return {"type": "string"}
    if isinstance(value, list):
        items = None
        for item in value:
            items = merge(items, shape(item, exchange_id))
        return {"type": "array", "items": items or {}}
    if isinstance(value, dict):
        return {"type": "object", "properties": {key: shape(item, exchange_id) for key, item in sorted(value.items())}}
    raise ValueError("traffic bodies must contain JSON values")


def merge(left, right):
    result = _merge(left, right)
    refs = sorted(set((left or {}).get("x-traffic-evidence", []) + (right or {}).get("x-traffic-evidence", [])))
    if refs:
        result["x-traffic-evidence"] = refs
    return result


def _merge(left, right):
    if left is None:
        return copy.deepcopy(right)
    if right is None or left == right:
        return copy.deepcopy(left)
    if left.get("x-observed-null") or right.get("x-observed-null"):
        other = right if left.get("x-observed-null") else left
        result = copy.deepcopy(other)
        if "anyOf" in result:
            result["anyOf"] = [dict(item, nullable=True) for item in result["anyOf"]]
        elif result.get("type"):
            result["nullable"] = True
        return result
    lt, rt = left.get("type"), right.get("type")
    if lt == rt and lt:
        result = copy.deepcopy(left)
        if lt == "object":
            props = result.setdefault("properties", {})
            for key, item in right.get("properties", {}).items():
                props[key] = merge(props.get(key), item)
        elif lt == "array":
            result["items"] = merge(left.get("items") or None, right.get("items") or None) or {}
        if right.get("nullable"):
            result["nullable"] = True
        return result
    if {lt, rt} == {"integer", "number"}:
        result = {"type": "number"}
        if left.get("nullable") or right.get("nullable"):
            result["nullable"] = True
        return result
    variants = []
    for value in (left, right):
        for item in value.get("anyOf", [value]):
            for index, previous in enumerate(variants):
                if item.get("type") == previous.get("type"):
                    variants[index] = merge(previous, item)
                    break
            else:
                variants.append(copy.deepcopy(item))
    return {"anyOf": sorted(variants, key=digest)}


def path_template(path, explicit=None):
    """Only numeric/UUID segments are generalized automatically; others stay literal."""
    segments = path.split("/")
    if explicit is None:
        parts = ["{path_%d}" % i if re.fullmatch(r"[0-9]+", part) or UUID.fullmatch(part) else part
                 for i, part in enumerate(segments)]
        template = "/".join(parts)
    else:
        template = explicit
        parts = template.split("/")
        if not template.startswith("/") or "?" in template or "#" in template or len(parts) != len(segments):
            raise ValueError("path template must match the observed path segments")
    params = {}
    for observed, part in zip(segments, parts):
        match = re.fullmatch(r"\{([A-Za-z_][A-Za-z0-9_]*)\}", part)
        if match:
            if match[1] in params:
                raise ValueError("duplicate path parameter")
            params[match[1]] = unquote(observed)
        elif part != observed or "{" in part or "}" in part:
            raise ValueError("path template constants must match the observed path")
    return template, params


def _operation_id(method, template):
    return "observed_" + method + "_" + hashlib.sha256(template.encode()).hexdigest()[:16]


def _pointer_leaf(value):
    if not isinstance(value, str) or not value.startswith("/"):
        return None
    leaf = value.rsplit("/", 1)[-1].replace("~1", "/").replace("~0", "~")
    if not PATH_PARAMETER.fullmatch(leaf) or sensitive(leaf):
        return None
    return leaf


def _promote_semantic_path_parameters(paths, evidence, dependencies, automatic_parameters, explicit_templates):
    """Use only unambiguous dependency hints to improve generated parameter names.

    The observed value match remains unverified and does not become an OpenAPI
    Link or policy claim. Ambiguous, sensitive, colliding, or explicitly named
    parameters stay unchanged.
    """
    operations = {
        operation["operationId"]: (template, method, operation)
        for template, path_item in paths.items()
        for method, operation in path_item.items()
    }
    candidates = {}
    for dependency in dependencies:
        if dependency["location"] != "path":
            continue
        operation = operations.get(dependency["consumer_operation_id"])
        if operation is None:
            continue
        template = operation[0]
        parameter = _pointer_leaf(dependency["parameter_pointer"])
        key = (template, parameter)
        if (
            parameter is None
            or key not in automatic_parameters
            or template in explicit_templates
        ):
            continue
        candidate = _pointer_leaf(dependency["response_pointer"])
        if candidate is not None and candidate != parameter:
            candidates.setdefault(key, set()).add(candidate)

    renames = {}
    for template in paths:
        placeholders = set(re.findall(r"\{([^{}]+)\}", template))
        proposals = {
            parameter: next(iter(names))
            for (candidate_template, parameter), names in candidates.items()
            if candidate_template == template and len(names) == 1
        }
        counts = {name: list(proposals.values()).count(name) for name in proposals.values()}
        accepted = {
            parameter: name
            for parameter, name in proposals.items()
            if name not in placeholders - {parameter} and counts[name] == 1
        }
        if accepted:
            renames[template] = accepted
    if not renames:
        return

    operation_ids = {}
    consumer_renames = {}
    updated_paths = {}
    for template, path_item in paths.items():
        selected = renames.get(template, {})
        updated_template = template
        for previous, current in selected.items():
            updated_template = updated_template.replace("{" + previous + "}", "{" + current + "}")
        if updated_template in updated_paths:
            raise ValueError("semantic path parameter promotion produced a duplicate route")
        for method, operation in path_item.items():
            previous_id = operation["operationId"]
            if selected:
                for parameter in operation["parameters"]:
                    if parameter["in"] == "path" and parameter["name"] in selected:
                        parameter["name"] = selected[parameter["name"]]
                operation["operationId"] = _operation_id(method, updated_template)
                operation_ids[previous_id] = operation["operationId"]
                consumer_renames[previous_id] = selected
        updated_paths[updated_template] = path_item
    paths.clear()
    paths.update(updated_paths)

    for item in evidence:
        previous_id = item["operation_id"]
        if previous_id in operation_ids:
            item["operation_id"] = operation_ids[previous_id]
            item["path_basis"] = "numeric_uuid_heuristic_with_unverified_dependency_name"
    for dependency in dependencies:
        producer_id = dependency["producer_operation_id"]
        consumer_id = dependency["consumer_operation_id"]
        selected = consumer_renames.get(consumer_id, {})
        parameter = _pointer_leaf(dependency["parameter_pointer"])
        if dependency["location"] == "path" and parameter in selected:
            dependency["parameter_pointer"] = "/" + selected[parameter]
        dependency["producer_operation_id"] = operation_ids.get(producer_id, producer_id)
        dependency["consumer_operation_id"] = operation_ids.get(consumer_id, consumer_id)


def leaves(value, pointer=""):
    if isinstance(value, dict):
        for key, item in value.items():
            if not sensitive(key):
                yield from leaves(item, pointer + "/" + key.replace("~", "~0").replace("/", "~1"))
    elif isinstance(value, list):
        for index, item in enumerate(value):
            yield from leaves(item, pointer + "/" + str(index))
    elif isinstance(value, (str, int)) and not isinstance(value, bool) and value != "" and value != "[REDACTED]":
        yield pointer, value


def infer(base_url, exchanges, templates=None, provenance=None):
    selected_origin = origin(base_url)
    base = urlsplit(base_url)
    if base.query or base.fragment:
        raise ValueError("base_url must not contain a query or fragment")
    if not isinstance(exchanges, list) or not exchanges:
        raise ValueError("nonempty exchanges are required")
    templates = templates or {}
    if not isinstance(templates, dict) or any(not isinstance(k, str) or not isinstance(v, str) for k, v in templates.items()):
        raise ValueError("path_templates must map observed paths to template strings")
    paths, evidence, dependencies, previous = {}, [], [], []
    route_templates = {}
    automatic_parameters, explicit_templates = set(), set()
    seen = set()
    for exchange in sorted(exchanges, key=lambda e: (e.get("timestamp", 0), e.get("exchange_id", ""))):
        eid = exchange.get("exchange_id")
        if not isinstance(eid, str) or not eid or eid in seen:
            raise ValueError("every observation needs a unique exchange_id")
        seen.add(eid)
        url = exchange.get("url", "")
        if origin(url) != selected_origin:
            raise ValueError("all exchanges must use the selected target origin")
        parsed = urlsplit(url)
        # The engine appends operation paths to base_url, which may have a prefix.
        prefix = base.path.rstrip("/")
        if prefix and not parsed.path.startswith(prefix + "/") and parsed.path != prefix:
            raise ValueError("exchange is outside the selected base path")
        path = parsed.path[len(prefix):] or "/"
        explicit = path in templates
        template, params = path_template(path, templates.get(path))
        if explicit:
            explicit_templates.add(template)
        else:
            automatic_parameters.update(
                (template, name) for name in params if AUTOMATIC_PATH_PARAMETER.fullmatch(name)
            )
        route = re.sub(r"\{[^}]+\}", "{}", template)
        if route in route_templates and route_templates[route] != template:
            raise ValueError("equivalent routes need consistent path parameter names in path_templates")
        route_templates[route] = template
        method = exchange.get("method", "").lower()
        if method not in METHODS:
            raise ValueError("unsupported HTTP method in traffic")
        status = exchange.get("status")
        if not isinstance(status, int) or isinstance(status, bool) or not 100 <= status <= 599:
            raise ValueError("observation needs a recorded HTTP response status")
        operation_id = _operation_id(method, template)
        operation = paths.setdefault(template, {}).setdefault(method, {
            "operationId": operation_id, "parameters": [], "responses": {},
            "x-traffic-evidence": [], "x-inference": "observed_shape_not_declared_contract"})
        operation["x-traffic-evidence"].append(eid)
        query = {key: values for key, values in parse_qs(parsed.query, keep_blank_values=True).items() if not sensitive(key)}
        for location, values in (("path", params), ("query", query)):
            for name, value in values.items():
                schema = {"type": "string"}
                if location == "path" and re.fullmatch(r"[0-9]+", value):
                    schema = {"type": "integer"}
                if location == "query" and len(value) > 1:
                    schema = {"type": "array", "items": {"type": "string"}}
                existing = next((p for p in operation["parameters"] if p["name"] == name and p["in"] == location), None)
                if existing is None:
                    existing = {"name": name, "in": location, "required": location == "path", "schema": schema,
                                "x-traffic-evidence": []}
                    operation["parameters"].append(existing)
                else:
                    existing["schema"] = merge(existing["schema"], schema)
                existing["x-traffic-evidence"].append(eid)
        if exchange.get("request_json_present"):
            body = operation.setdefault("requestBody", {"required": False, "content": {"application/json": {}}})
            media = body["content"]["application/json"]
            media["schema"] = merge(media.get("schema"), shape(exchange.get("request_json"), eid))
        response = operation["responses"].setdefault(str(status), {"description": "Observed HTTP response", "x-traffic-evidence": []})
        response["x-traffic-evidence"].append(eid)
        if exchange.get("response_json_present"):
            content_type = exchange.get("response_content_type", "application/json").split(";", 1)[0].strip().lower()
            if not content_type or not (content_type == "application/json" or content_type.endswith("+json")):
                content_type = "application/json"
            media = response.setdefault("content", {}).setdefault(content_type, {})
            media["schema"] = merge(media.get("schema"), shape(exchange.get("response_json"), eid))
        evidence.append({"exchange_id": eid, "operation_id": operation_id,
                         "timestamp": exchange.get("timestamp", 0), "status": status,
                         "request_body_sha256": exchange.get("request_body_sha256", ""),
                         "response_body_sha256": exchange.get("response_body_sha256", ""),
                         "request_head_sha256": exchange.get("request_head_sha256", ""),
                         "response_head_sha256": exchange.get("response_head_sha256", ""),
                         "identity_ref": exchange.get("identity_ref", "unknown"),
                         "path_basis": "supplied_template" if path in templates else "numeric_uuid_heuristic" if params else "literal_observed_path",
                         "json_observed": {"request": bool(exchange.get("request_json_present")),
                                           "response": bool(exchange.get("response_json_present"))}})
        consumers = [("path", "/" + name, value) for name, value in params.items()]
        consumers += [("query", "/" + name, value) for name, values in query.items() for value in values]
        if exchange.get("request_json_present"):
            consumers += [("body", p, v) for p, v in leaves(exchange.get("request_json"))]
        for producer in previous:
            for location, p, value in consumers:
                if str(value) == str(producer["value"]) and (location == "path" or p.rsplit("/", 1)[-1] == producer["pointer"].rsplit("/", 1)[-1]):
                    dependencies.append({"producer_exchange_id": producer["exchange_id"],
                                         "producer_operation_id": producer["operation_id"],
                                         "response_pointer": producer["pointer"], "consumer_exchange_id": eid,
                                         "consumer_operation_id": operation_id, "location": location, "parameter_pointer": p,
                                         "basis": "observed_value_match", "verified": False})
        if 200 <= status < 300 and exchange.get("response_json_present"):
            previous += [{"exchange_id": eid, "operation_id": operation_id, "pointer": p, "value": v}
                         for p, v in leaves(exchange.get("response_json"))]
    if set(templates) - {urlsplit(e["url"]).path[len(base.path.rstrip("/")):] or "/" for e in exchanges}:
        raise ValueError("path template refers to an unobserved path")
    _promote_semantic_path_parameters(
        paths,
        evidence,
        dependencies,
        automatic_parameters,
        explicit_templates,
    )
    document = {"openapi": "3.0.3", "info": {"title": "Observed API", "version": "1"},
                "servers": [{"url": base_url.rstrip("/")}], "paths": paths}
    manifest = {"format": FORMAT, "inference": "observed_shape_not_declared_contract", "policy_inferred": False,
                "coverage": "selected_captured_exchanges_only", "source": provenance or {"kind": "supplied_observations"},
                "evidence": evidence, "dependency_candidates": dependencies}
    document["x-uroboros-traffic"] = manifest
    return document
