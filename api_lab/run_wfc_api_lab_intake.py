"""Inspect or submit one operator-reviewed WFC API Lab execution."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import urllib.error
import urllib.request
from pathlib import Path
from urllib.parse import urlsplit

from api_lab.engine import digest
from api_lab.wfc_api_lab_intake import (
    MAX_REQUEST_BYTES,
    REQUEST_CONTRACT,
    ROUTE,
    _duplicate_rejecting_object,
    _reject_constant,
    _load_json,
    load_review,
    prepare,
)


def _read(path, label, maximum=MAX_REQUEST_BYTES):
    raw = Path(path).read_bytes()
    if not raw or len(raw) > maximum:
        raise ValueError(label + " is empty or exceeds its byte limit")
    return raw


def _sha(value, label):
    if not isinstance(value, str) or len(value) != 64 or any(char not in "0123456789abcdef" for char in value):
        raise ValueError(label + " must be a lowercase SHA-256 string")
    return value


def _artifact(path, expected, label):
    raw = _read(path, label)
    expected = _sha(expected, label + " hash")
    if hashlib.sha256(raw).hexdigest() != expected:
        raise ValueError(label + " digest mismatch")
    return _load_json(raw, label)


def _review(path, expected):
    raw = _read(path, "review artifact", 1024 * 1024)
    return load_review(raw, expected)


def start_request(args):
    return {
        "contract": REQUEST_CONTRACT,
        "action": "start",
        "proposal_bundle": _artifact(args.proposal, args.proposal_sha256, "proposal bundle"),
        "proposal_bundle_sha256": args.proposal_sha256,
        "proposal_id": _sha(args.proposal_id, "proposal ID"),
        "schema": _artifact(args.schema, args.schema_sha256, "schema artifact"),
        "schema_artifact_sha256": args.schema_sha256,
        "review": _review(args.review, args.review_sha256),
        "review_sha256": args.review_sha256,
    }


def replay_request(args):
    raw = _read(args.recipe, "API Lab recipe")
    try:
        recipe = json.loads(
            raw.decode("ascii"),
            object_pairs_hook=_duplicate_rejecting_object,
            parse_constant=_reject_constant,
        )
    except (UnicodeDecodeError, json.JSONDecodeError):
        raise ValueError("API Lab recipe is not ASCII JSON") from None
    recipe_hash = _sha(args.recipe_sha256, "API Lab recipe hash")
    if not isinstance(recipe, dict) or digest(recipe) != recipe_hash:
        raise ValueError("API Lab recipe digest mismatch")
    return {
        "contract": REQUEST_CONTRACT,
        "action": "replay",
        "recipe": recipe,
        "recipe_sha256": recipe_hash,
        "review": _review(args.review, args.review_sha256),
        "review_sha256": args.review_sha256,
        "replay_of": args.replay_of,
    }


def _endpoint(base):
    parsed = urlsplit(base)
    if parsed.scheme not in {"http", "https"} or not parsed.hostname or parsed.username or parsed.password or parsed.query or parsed.fragment:
        raise ValueError("API Lab URL must be HTTP(S), without credentials, query, or fragment")
    path = parsed.path.rstrip("/")
    if path.endswith(ROUTE):
        return base.rstrip("/")
    if path and path != "/":
        raise ValueError("API Lab URL must be an origin or the operator intake route")
    return base.rstrip("/") + ROUTE


def _post(base, token, payload):
    if not token:
        raise ValueError("API Lab bearer token is empty")
    data = json.dumps(payload, sort_keys=True, separators=(",", ":"), ensure_ascii=True).encode("ascii")
    if len(data) > MAX_REQUEST_BYTES:
        raise ValueError("operator intake request exceeds the byte limit")
    request = urllib.request.Request(
        _endpoint(base),
        data=data,
        method="POST",
        headers={"Authorization": "Bearer " + token, "Content-Type": "application/json"},
    )
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    try:
        with opener.open(request, timeout=30) as response:
            raw = response.read(1024 * 1024 + 1)
    except urllib.error.HTTPError as exc:
        raw = exc.read(1024 * 1024 + 1)
        try:
            reason = json.loads(raw).get("reason", "HTTP %d" % exc.code)
        except (UnicodeDecodeError, json.JSONDecodeError, AttributeError):
            reason = "HTTP %d" % exc.code
        raise ValueError("API Lab operator intake rejected the request: " + reason) from None
    if len(raw) > 1024 * 1024:
        raise ValueError("API Lab operator response exceeds the byte limit")
    try:
        result = json.loads(raw.decode("ascii"))
    except (UnicodeDecodeError, json.JSONDecodeError):
        raise ValueError("API Lab operator response is invalid JSON") from None
    if not isinstance(result, dict):
        raise ValueError("API Lab operator response is not an object")
    return result


def _add_identity(parser):
    parser.add_argument("--identities", default=os.environ.get("API_LAB_IDENTITIES", ""), help="Current API Lab identities file")


def _add_review(parser):
    parser.add_argument("--review", required=True)
    parser.add_argument("--review-sha256", required=True)
    _add_identity(parser)


def _add_start(parser):
    parser.add_argument("--proposal", required=True)
    parser.add_argument("--proposal-sha256", required=True)
    parser.add_argument("--proposal-id", required=True)
    parser.add_argument("--schema", required=True)
    parser.add_argument("--schema-sha256", required=True)
    _add_review(parser)


def _add_replay(parser):
    parser.add_argument("--recipe", required=True)
    parser.add_argument("--recipe-sha256", required=True)
    parser.add_argument("--replay-of", default="")
    _add_review(parser)


def _add_remote(parser):
    parser.add_argument("--api-lab-url", required=True, help="API Lab origin or full operator intake URL")
    parser.add_argument("--token-env", default="API_LAB_TOKEN", help="Environment variable containing the bearer token")


def main(argv=None):
    parser = argparse.ArgumentParser(description="Operator-reviewed WFC proposal intake into API Lab")
    subparsers = parser.add_subparsers(dest="command", required=True)
    plan_start = subparsers.add_parser("plan", help="Validate a proposed initial execution without network or database writes")
    _add_start(plan_start)
    run_start = subparsers.add_parser("run", help="Submit a reviewed initial execution to the existing API Lab service")
    _add_start(run_start)
    _add_remote(run_start)
    plan_replay = subparsers.add_parser("plan-replay", help="Validate an independent replay without network or database writes")
    _add_replay(plan_replay)
    run_replay = subparsers.add_parser("replay", help="Submit an exported recipe with a fresh target review")
    _add_replay(run_replay)
    _add_remote(run_replay)
    args = parser.parse_args(argv)
    if not args.identities:
        parser.error("--identities or API_LAB_IDENTITIES is required")
    request = start_request(args) if args.command in {"plan", "run"} else replay_request(args)
    if args.command in {"plan", "plan-replay"}:
        result = {"validated": True, **prepare(request, args.identities).summary()}
    else:
        token = os.environ.get(args.token_env, "")
        result = _post(args.api_lab_url, token, request)
    print(json.dumps(result, sort_keys=True, indent=2))


if __name__ == "__main__":
    main()
