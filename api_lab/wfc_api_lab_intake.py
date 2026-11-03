"""Operator-reviewed WFC proposal intake into the existing API Lab job store.

This module is deliberately not an MCP tool.  It validates one immutable WFC
proposal, an executable Traffic-to-spec schema, and a separate operator review,
then starts a normal :class:`api_lab.jobs.Jobs` experiment.  Replay consumes an
exported API Lab recipe and a fresh review; it never reads the original WFC job.
"""

from __future__ import annotations

import asyncio
import copy
import hashlib
import json
import math
import re
from dataclasses import dataclass
from datetime import datetime
from pathlib import Path

from api_lab.engine import Identities, digest, origin, validate_plan
from api_lab.schemathesis_wfc_api_lab import dumps_proposals
from api_lab.traffic_schema_projection import loads_executable_traffic_schema


REQUEST_CONTRACT = "uroboros-wfc-api-lab-intake-request-v1"
REVIEW_CONTRACT = "uroboros-wfc-api-lab-execution-review-v1"
INTAKE_CONTRACT = "uroboros-wfc-api-lab-intake-v1"
EXECUTION_CONTRACT = "uroboros-wfc-api-lab-reviewed-execution-v1"
ROUTE = "/operator/wfc-intake"
MAX_REQUEST_BYTES = 16 * 1024 * 1024
MAX_IDENTITY_BYTES = 1024 * 1024
SHA256 = re.compile(r"[0-9a-f]{64}\Z")
REFERENCE = re.compile(r"[A-Za-z0-9][A-Za-z0-9_.:@/-]{0,255}\Z")
JOB_ID = re.compile(r"[0-9a-f]{32}\Z")


def _canonical(value):
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=True).encode("ascii") + b"\n"


def _duplicate_rejecting_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("duplicate JSON key: " + key)
        result[key] = value
    return result


def _reject_constant(value):
    raise ValueError("non-finite JSON constant is not allowed: " + value)


def _load_json(data, label, maximum=MAX_REQUEST_BYTES):
    if not isinstance(data, (bytes, bytearray)):
        raise ValueError(label + " must be bytes")
    raw = bytes(data)
    if not raw or len(raw) > maximum:
        raise ValueError(label + " is empty or exceeds its byte limit")
    try:
        value = json.loads(
            raw.decode("ascii"),
            object_pairs_hook=_duplicate_rejecting_object,
            parse_constant=_reject_constant,
        )
    except UnicodeDecodeError:
        raise ValueError(label + " must be canonical ASCII JSON") from None
    except json.JSONDecodeError:
        raise ValueError(label + " is invalid JSON") from None
    if raw != _canonical(value):
        raise ValueError(label + " is not canonical")
    return value


def _hash(value, label):
    if not isinstance(value, str) or not SHA256.fullmatch(value):
        raise ValueError(label + " must be a lowercase SHA-256 string")
    return value


def _reference(value, label):
    if not isinstance(value, str) or not REFERENCE.fullmatch(value):
        raise ValueError(label + " is invalid")
    return value


def _bounded(value, label, maximum=1024):
    if not isinstance(value, str) or not value or len(value) > maximum or any(ord(char) < 32 for char in value):
        raise ValueError(label + " is invalid")
    return value


def _timestamp(value):
    value = _bounded(value, "review timestamp", 64)
    try:
        parsed = datetime.fromisoformat(value[:-1] + "+00:00" if value.endswith("Z") else value)
    except ValueError:
        raise ValueError("review timestamp must be ISO-8601") from None
    if parsed.tzinfo is None or parsed.utcoffset() is None:
        raise ValueError("review timestamp must include a timezone")
    return value


def _identity_bytes(path):
    raw = Path(path).read_bytes()
    if not raw or len(raw) > MAX_IDENTITY_BYTES:
        raise ValueError("identity configuration is empty or exceeds its byte limit")
    return raw


def identity_config_sha256(path):
    return hashlib.sha256(_identity_bytes(path)).hexdigest()


def load_reviewed_identities(path, expected_sha256):
    """Load credentials from the exact bytes approved by the operator review."""
    raw = _identity_bytes(path)
    expected = _hash(expected_sha256, "reviewed identity configuration hash")
    if hashlib.sha256(raw).hexdigest() != expected:
        raise ValueError("current API Lab identity configuration differs from the reviewed hash")
    try:
        data = json.loads(
            raw.decode("utf-8"),
            object_pairs_hook=_duplicate_rejecting_object,
            parse_constant=_reject_constant,
        )
    except UnicodeDecodeError:
        raise ValueError("identity configuration must be UTF-8 JSON") from None
    except json.JSONDecodeError:
        raise ValueError("identity configuration is invalid JSON") from None
    if not isinstance(data, dict):
        raise ValueError("identity configuration must be an object")
    return Identities(data=data)


def _execution_options(value):
    expected = {"budget_seconds", "max_requests", "rps", "request_timeout", "seed"}
    if not isinstance(value, dict) or set(value) != expected:
        raise ValueError("review execution limits are invalid")
    budget = value["budget_seconds"]
    requests = value["max_requests"]
    rps = value["rps"]
    timeout = value["request_timeout"]
    seed = value["seed"]
    if not isinstance(budget, int) or isinstance(budget, bool) or not 10 <= budget <= 7200:
        raise ValueError("review budget_seconds must be 10..7200")
    if not isinstance(requests, int) or isinstance(requests, bool) or not 1 <= requests <= 10000:
        raise ValueError("review max_requests must be 1..10000")
    if not isinstance(rps, (int, float)) or isinstance(rps, bool) or not math.isfinite(rps) or not 0 < rps <= 1000:
        raise ValueError("review rps must be positive and at most 1000")
    if not isinstance(timeout, (int, float)) or isinstance(timeout, bool) or not math.isfinite(timeout) or not 0 < timeout <= 60:
        raise ValueError("review request_timeout must be positive and at most 60")
    if not isinstance(seed, int) or isinstance(seed, bool) or not 0 <= seed <= 2**32 - 1:
        raise ValueError("review seed must be an unsigned 32-bit integer")
    return dict(value)


def load_review(data, expected_sha256):
    raw = bytes(data)
    expected = _hash(expected_sha256, "review artifact hash")
    if hashlib.sha256(raw).hexdigest() != expected:
        raise ValueError("review artifact digest mismatch")
    review = _load_json(raw, "review artifact", 1024 * 1024)
    fields = {
        "contract", "action", "decision", "reviewer_ref", "reviewed_at",
        "proposal_bundle_sha256", "proposal_id", "schema_artifact_sha256",
        "plan_sha256", "identity_config_sha256", "source_recipe_sha256",
        "evidence", "target", "execution", "cleanup",
    }
    if not isinstance(review, dict) or set(review) != fields or review.get("contract") != REVIEW_CONTRACT:
        raise ValueError("WFC API Lab review is invalid")
    if review.get("action") not in {"start", "replay"} or review.get("decision") != "approved_for_execution":
        raise ValueError("WFC API Lab review is not an execution approval")
    _reference(review["reviewer_ref"], "reviewer reference")
    _timestamp(review["reviewed_at"])
    for key in (
        "proposal_bundle_sha256", "proposal_id", "schema_artifact_sha256",
        "plan_sha256", "identity_config_sha256",
    ):
        _hash(review[key], "review " + key)
    if review["action"] == "start":
        if review["source_recipe_sha256"] is not None:
            raise ValueError("initial review must not name a source recipe")
    else:
        _hash(review["source_recipe_sha256"], "review source recipe hash")
    evidence = review.get("evidence")
    if not isinstance(evidence, dict) or set(evidence) != {
        "schemathesis_report_sha256", "runtime_evidence_sha256", "policy_artifact_sha256"
    }:
        raise ValueError("review evidence binding is invalid")
    for key, value in evidence.items():
        _hash(value, "review evidence " + key)
    target = review.get("target")
    if not isinstance(target, dict) or set(target) != {
        "origin", "revision", "disposition", "scope_ref", "mutation_scope_ref",
        "reset_ref", "cleanup_ref",
    }:
        raise ValueError("review target is invalid")
    if origin(target["origin"]) != target["origin"]:
        raise ValueError("review target origin must be a canonical HTTP(S) origin")
    _bounded(target["revision"], "review target revision", 256)
    if target["disposition"] not in {"disposable", "resettable"}:
        raise ValueError("review target disposition must be disposable or resettable")
    for key in ("scope_ref", "mutation_scope_ref", "reset_ref", "cleanup_ref"):
        _reference(target[key], "review target " + key)
    _execution_options(review["execution"])
    if not isinstance(review["cleanup"], list):
        raise ValueError("review cleanup must be an array")
    return review


def dumps_review(value):
    raw = _canonical(value)
    load_review(raw, hashlib.sha256(raw).hexdigest())
    return raw


def _operation_methods(document):
    result = {}
    for path, path_item in document.get("paths", {}).items():
        if not isinstance(path_item, dict):
            continue
        for method, operation in path_item.items():
            if method.lower() in {"get", "post", "put", "patch", "delete", "head", "options"} and isinstance(operation, dict):
                identifier = operation.get("operationId")
                if isinstance(identifier, str):
                    result[identifier] = method.upper()
    return result


def _validate_cleanup(plan, review, document, selected_identities):
    cleanup = plan.get("cleanup", [])
    if cleanup != review["cleanup"]:
        raise ValueError("review cleanup differs from the reviewed plan")
    if review["target"]["disposition"] == "resettable" and not cleanup:
        raise ValueError("resettable target review requires in-plan cleanup")
    methods = _operation_methods(document)
    for step in cleanup:
        if not isinstance(step, dict) or set(step) != {
            "name", "operation_id", "identity", "path_parameters", "expected_status"
        }:
            raise ValueError("cleanup must be a reference-only DELETE step")
        if methods.get(step.get("operation_id")) != "DELETE":
            raise ValueError("cleanup must use an observed DELETE operation")
        if step.get("identity") not in selected_identities:
            raise ValueError("cleanup identity is not bound to the WFC proposal")
        parameters = step.get("path_parameters")
        if not isinstance(parameters, dict) or not parameters or any(
            not isinstance(item, dict)
            or set(item) != {"$from"}
            or not isinstance(item["$from"], str)
            for item in parameters.values()
        ):
            raise ValueError("cleanup path parameters must be response references")
        status = step.get("expected_status")
        if not isinstance(status, int) or isinstance(status, bool) or not 200 <= status <= 299:
            raise ValueError("cleanup requires an expected successful status")


def _plan_with_cleanup(proposal, review, document):
    plan = copy.deepcopy(proposal["plan"])
    cleanup = copy.deepcopy(review["cleanup"])
    if cleanup:
        plan["cleanup"] = cleanup
    validate_plan(plan)
    _validate_cleanup(plan, review, document, set(proposal["source"]["identities"].values()))
    if digest(plan) != review["plan_sha256"]:
        raise ValueError("review plan hash differs from the reviewed proposal plus cleanup")
    return plan


def _validate_identity_bindings(identity_path, expected_hash, target_origin, plan, review):
    identities = load_reviewed_identities(identity_path, expected_hash)
    for step in plan["steps"] + plan.get("cleanup", []):
        identities.headers(step["identity"], target_origin)
    for check in plan["checks"]:
        if "peer_identity" in check:
            identities.headers(check["peer_identity"], target_origin)
    if identities.redact(plan) != plan or identities.redact(review) != review:
        raise ValueError("reviewed intake contains a configured identity credential value")
    return expected_hash


def _intake_context(source, proposal, proposal_hash, plan):
    result = {
        "contract": INTAKE_CONTRACT,
        "proposal_bundle_sha256": proposal_hash,
        "proposal_id": proposal["proposal_id"],
        "schema_artifact_sha256": source["schema_artifact_sha256"],
        "schema_id": source["schema_id"],
        "schemathesis_report_sha256": source["schemathesis_report_sha256"],
        "runtime_evidence_sha256": source["runtime_evidence_sha256"],
        "policy_artifact_sha256": source["policy_artifact_sha256"],
        "policy_id": source["policy_id"],
        "identity_config_sha256": source["identity_config_sha256"],
        "source_run_id": source["run_id"],
        "source_target_origin": source["target_origin"],
        "source_target_revision": source["target_revision"],
        "scope_ref": source["scope_ref"],
        "mutation_scope_ref": source["mutation_scope_ref"],
        "reset_ref": source["reset_ref"],
        "policy_rule_id": proposal["policy"]["rule_id"],
        "case_ref": proposal["source"]["case_ref"],
        "identities": copy.deepcopy(proposal["source"]["identities"]),
        "plan_sha256": digest(plan),
        "claims": {"findings": 0, "confirmed_vulnerabilities": 0},
        "raw_wfc_values_stored": False,
        "credential_values_stored": False,
        "finding_promotion": False,
    }
    validate_intake_context(result)
    return result


def validate_intake_context(value):
    expected = {
        "contract", "proposal_bundle_sha256", "proposal_id", "schema_artifact_sha256", "schema_id",
        "schemathesis_report_sha256", "runtime_evidence_sha256", "policy_artifact_sha256", "policy_id",
        "identity_config_sha256", "source_run_id", "source_target_origin", "source_target_revision",
        "scope_ref", "mutation_scope_ref", "reset_ref", "policy_rule_id", "case_ref", "identities",
        "plan_sha256", "claims", "raw_wfc_values_stored", "credential_values_stored", "finding_promotion",
    }
    if not isinstance(value, dict) or set(value) != expected or value.get("contract") != INTAKE_CONTRACT:
        raise ValueError("WFC intake context is invalid")
    for key in (
        "proposal_bundle_sha256", "proposal_id", "schema_artifact_sha256", "schema_id",
        "schemathesis_report_sha256", "runtime_evidence_sha256", "policy_artifact_sha256", "policy_id",
        "identity_config_sha256", "case_ref", "plan_sha256",
    ):
        _hash(value[key], "WFC intake " + key)
    for key in ("source_run_id", "scope_ref", "mutation_scope_ref", "reset_ref", "policy_rule_id"):
        _reference(value[key], "WFC intake " + key)
    if origin(value["source_target_origin"]) != value["source_target_origin"]:
        raise ValueError("WFC intake source target origin is invalid")
    _bounded(value["source_target_revision"], "WFC intake source target revision", 256)
    identities = value.get("identities")
    if not isinstance(identities, dict) or set(identities) != {"owner", "peer"}:
        raise ValueError("WFC intake identities are invalid")
    owner = _reference(identities["owner"], "WFC intake owner identity")
    peer = _reference(identities["peer"], "WFC intake peer identity")
    if owner == peer:
        raise ValueError("WFC intake identities must be distinct")
    if value["claims"] != {"findings": 0, "confirmed_vulnerabilities": 0}:
        raise ValueError("WFC intake attempted to promote a claim")
    if any(value[key] is not False for key in ("raw_wfc_values_stored", "credential_values_stored", "finding_promotion")):
        raise ValueError("WFC intake boundary flags are invalid")
    return value


def _execution_context(review, review_hash):
    result = {
        "contract": EXECUTION_CONTRACT,
        "action": review["action"],
        "review_sha256": review_hash,
        "review": copy.deepcopy(review),
        "automatic_submission": False,
        "finding_promotion": False,
    }
    validate_execution_context(result)
    return result


def validate_execution_context(value):
    if not isinstance(value, dict) or set(value) != {
        "contract", "action", "review_sha256", "review", "automatic_submission", "finding_promotion"
    } or value.get("contract") != EXECUTION_CONTRACT:
        raise ValueError("WFC reviewed execution context is invalid")
    raw = dumps_review(value["review"])
    if hashlib.sha256(raw).hexdigest() != _hash(value["review_sha256"], "execution review hash"):
        raise ValueError("WFC execution review hash differs from its document")
    if value["action"] != value["review"]["action"]:
        raise ValueError("WFC execution action differs from its review")
    if value["automatic_submission"] is not False or value["finding_promotion"] is not False:
        raise ValueError("WFC execution boundary flags are invalid")
    return value


def _check_review_bindings(review, source, proposal_id, plan_hash, identity_hash, *, action, recipe_hash=None):
    if review["action"] != action:
        raise ValueError("review action differs from the requested action")
    expected = {
        "proposal_bundle_sha256": source["proposal_bundle_sha256"],
        "proposal_id": proposal_id,
        "schema_artifact_sha256": source["schema_artifact_sha256"],
        "plan_sha256": plan_hash,
        "identity_config_sha256": identity_hash,
    }
    for key, value in expected.items():
        if review[key] != value:
            raise ValueError("review " + key + " differs from the selected intake")
    evidence = review["evidence"]
    for key in ("schemathesis_report_sha256", "runtime_evidence_sha256", "policy_artifact_sha256"):
        if evidence[key] != source[key]:
            raise ValueError("review evidence differs from the selected intake")
    if action == "start":
        target = review["target"]
        if target["origin"] != source["source_target_origin"] or target["revision"] != source["source_target_revision"]:
            raise ValueError("initial review target differs from the WFC source target")
        for key in ("scope_ref", "mutation_scope_ref", "reset_ref"):
            if target[key] != source[key]:
                raise ValueError("initial review target " + key + " differs from the WFC source")
    elif review["source_recipe_sha256"] != recipe_hash:
        raise ValueError("replay review source recipe hash differs from the exported recipe")


def _check_archived_execution(recipe, intake, execution):
    review = execution["review"]
    expected = {
        "proposal_bundle_sha256": intake["proposal_bundle_sha256"],
        "proposal_id": intake["proposal_id"],
        "schema_artifact_sha256": intake["schema_artifact_sha256"],
        "plan_sha256": intake["plan_sha256"],
        "identity_config_sha256": intake["identity_config_sha256"],
    }
    for key, value in expected.items():
        if review[key] != value:
            raise ValueError("archived WFC execution review differs from its intake context")
    for key in ("schemathesis_report_sha256", "runtime_evidence_sha256", "policy_artifact_sha256"):
        if review["evidence"][key] != intake[key]:
            raise ValueError("archived WFC execution evidence differs from its intake context")
    if recipe.get("base_url") != review["target"]["origin"]:
        raise ValueError("archived WFC execution target differs from its recipe")
    metadata = recipe.get("metadata")
    if (
        not isinstance(metadata, dict)
        or metadata.get("wfc_intake_hash") != digest(intake)
        or metadata.get("wfc_proposal_id") != intake["proposal_id"]
        or metadata.get("execution_review_sha256") != execution["review_sha256"]
        or metadata.get("reviewed_target_revision") != review["target"]["revision"]
    ):
        raise ValueError("archived WFC execution metadata differs from its reviewed contexts")
    if review["action"] == "start":
        if (
            review["target"]["origin"] != intake["source_target_origin"]
            or review["target"]["revision"] != intake["source_target_revision"]
            or any(review["target"][key] != intake[key] for key in ("scope_ref", "mutation_scope_ref", "reset_ref"))
        ):
            raise ValueError("archived initial WFC execution target differs from its intake context")


@dataclass(frozen=True)
class PreparedExecution:
    action: str
    base_url: str
    schema_document: dict
    plan: dict
    options: dict
    intake_context: dict
    execution_context: dict
    source_recipe_hash: str = ""
    replay_of: str = ""

    def summary(self):
        review = self.execution_context["review"]
        return {
            "action": self.action,
            "proposal_id": self.intake_context["proposal_id"],
            "proposal_bundle_sha256": self.intake_context["proposal_bundle_sha256"],
            "schema_artifact_sha256": self.intake_context["schema_artifact_sha256"],
            "plan_sha256": self.intake_context["plan_sha256"],
            "review_sha256": self.execution_context["review_sha256"],
            "target_origin": self.base_url,
            "target_revision": review["target"]["revision"],
            "target_disposition": review["target"]["disposition"],
            "cleanup_steps": len(self.plan.get("cleanup", [])),
            "cleanup_ref": review["target"]["cleanup_ref"],
            "source_recipe_sha256": self.source_recipe_hash or None,
            "findings_promoted": 0,
        }


def prepare_start(request, identity_path):
    expected = {
        "contract", "action", "proposal_bundle", "proposal_bundle_sha256", "proposal_id",
        "schema", "schema_artifact_sha256", "review", "review_sha256",
    }
    if not isinstance(request, dict) or set(request) != expected or request.get("contract") != REQUEST_CONTRACT or request.get("action") != "start":
        raise ValueError("WFC intake start request is invalid")
    proposal_hash = _hash(request["proposal_bundle_sha256"], "proposal bundle hash")
    proposal_raw = dumps_proposals(request["proposal_bundle"])
    if hashlib.sha256(proposal_raw).hexdigest() != proposal_hash:
        raise ValueError("proposal bundle digest mismatch")
    proposal_id = _hash(request["proposal_id"], "proposal ID")
    proposals = [item for item in request["proposal_bundle"]["entries"] if item["proposal_id"] == proposal_id]
    if len(proposals) != 1:
        raise ValueError("selected proposal ID is absent or ambiguous")
    proposal = proposals[0]
    schema_hash = _hash(request["schema_artifact_sha256"], "schema artifact hash")
    document = loads_executable_traffic_schema(_canonical(request["schema"]), schema_hash)
    source_bundle = request["proposal_bundle"]["source"]
    if source_bundle["schema_artifact_sha256"] != schema_hash or source_bundle["schema_id"] != digest(document):
        raise ValueError("proposal source differs from the selected schema")
    review_raw = _canonical(request["review"])
    review = load_review(review_raw, request["review_sha256"])
    plan = _plan_with_cleanup(proposal, review, document)
    source = {
        "proposal_bundle_sha256": proposal_hash,
        "schema_artifact_sha256": schema_hash,
        "schemathesis_report_sha256": source_bundle["schemathesis_report_sha256"],
        "runtime_evidence_sha256": source_bundle["runtime_evidence_sha256"],
        "policy_artifact_sha256": source_bundle["policy_artifact_sha256"],
        "source_target_origin": source_bundle["target_origin"],
        "source_target_revision": source_bundle["target_revision"],
        "scope_ref": source_bundle["scope_ref"],
        "mutation_scope_ref": source_bundle["mutation_scope_ref"],
        "reset_ref": source_bundle["reset_ref"],
    }
    identity_hash = source_bundle["identity_config_sha256"]
    _check_review_bindings(review, source, proposal_id, digest(plan), identity_hash, action="start")
    _validate_identity_bindings(identity_path, identity_hash, review["target"]["origin"], plan, review)
    intake = _intake_context(source_bundle, proposal, proposal_hash, plan)
    execution = _execution_context(review, request["review_sha256"])
    return PreparedExecution(
        action="start",
        base_url=review["target"]["origin"],
        schema_document=document,
        plan=plan,
        options=_execution_options(review["execution"]),
        intake_context=intake,
        execution_context=execution,
    )


def prepare_replay(request, identity_path):
    expected = {
        "contract", "action", "recipe", "recipe_sha256", "review", "review_sha256", "replay_of"
    }
    if not isinstance(request, dict) or set(request) != expected or request.get("contract") != REQUEST_CONTRACT or request.get("action") != "replay":
        raise ValueError("WFC intake replay request is invalid")
    recipe = request["recipe"]
    recipe_hash = _hash(request["recipe_sha256"], "source recipe hash")
    if not isinstance(recipe, dict) or digest(recipe) != recipe_hash:
        raise ValueError("source recipe digest mismatch")
    if not isinstance(recipe.get("schema_document"), dict) or not isinstance(recipe.get("plan"), dict):
        raise ValueError("WFC replay recipe must contain its schema and plan")
    intake = copy.deepcopy(recipe.get("wfc_intake"))
    validate_intake_context(intake)
    previous_execution = copy.deepcopy(recipe.get("wfc_execution"))
    validate_execution_context(previous_execution)
    _check_archived_execution(recipe, intake, previous_execution)
    document = loads_executable_traffic_schema(
        _canonical(recipe["schema_document"]), intake["schema_artifact_sha256"]
    )
    if digest(document) != intake["schema_id"]:
        raise ValueError("WFC replay schema differs from its intake context")
    plan = copy.deepcopy(recipe["plan"])
    validate_plan(plan)
    if digest(plan) != intake["plan_sha256"]:
        raise ValueError("WFC replay plan differs from its reviewed intake")
    checks = plan.get("checks", [])
    oracle = recipe.get("oracle")
    metadata = recipe.get("metadata")
    if (
        len(checks) != 1
        or checks[0].get("kind") != "private_read"
        or not isinstance(oracle, dict)
        or oracle.get("name") != checks[0].get("name")
        or oracle.get("kind") != "private_read"
        or oracle.get("verdict") != "violation"
        or not isinstance(metadata, dict)
        or previous_execution["review"]["proposal_id"] != intake["proposal_id"]
        or previous_execution["review"]["plan_sha256"] != intake["plan_sha256"]
    ):
        raise ValueError("WFC replay recipe is not an archived private-read violation")
    review = load_review(_canonical(request["review"]), request["review_sha256"])
    source = {
        "proposal_bundle_sha256": intake["proposal_bundle_sha256"],
        "schema_artifact_sha256": intake["schema_artifact_sha256"],
        "schemathesis_report_sha256": intake["schemathesis_report_sha256"],
        "runtime_evidence_sha256": intake["runtime_evidence_sha256"],
        "policy_artifact_sha256": intake["policy_artifact_sha256"],
        "source_target_origin": intake["source_target_origin"],
        "source_target_revision": intake["source_target_revision"],
        "scope_ref": intake["scope_ref"],
        "mutation_scope_ref": intake["mutation_scope_ref"],
        "reset_ref": intake["reset_ref"],
    }
    _check_review_bindings(
        review, source, intake["proposal_id"], digest(plan), intake["identity_config_sha256"],
        action="replay", recipe_hash=recipe_hash,
    )
    _validate_cleanup(plan, review, document, set(intake["identities"].values()))
    _validate_identity_bindings(
        identity_path, intake["identity_config_sha256"], review["target"]["origin"], plan, review
    )
    replay_of = request["replay_of"]
    if not isinstance(replay_of, str) or replay_of and not JOB_ID.fullmatch(replay_of):
        raise ValueError("replay_of must be empty or an API Lab job ID")
    return PreparedExecution(
        action="replay",
        base_url=review["target"]["origin"],
        schema_document=document,
        plan=plan,
        options=_execution_options(review["execution"]),
        intake_context=intake,
        execution_context=_execution_context(review, request["review_sha256"]),
        source_recipe_hash=recipe_hash,
        replay_of=replay_of,
    )


def prepare(request, identity_path):
    if not isinstance(request, dict):
        raise ValueError("WFC intake request must be an object")
    if request.get("action") == "start":
        return prepare_start(request, identity_path)
    if request.get("action") == "replay":
        return prepare_replay(request, identity_path)
    raise ValueError("WFC intake action must be start or replay")


def submit(store, identity_path, request):
    selected = prepare(request, identity_path)
    schema_id = store.save_schema(selected.schema_document)
    if schema_id != selected.intake_context["schema_id"]:
        raise ValueError("retained API Lab schema ID differs from the reviewed intake")
    return store.start(
        selected.base_url,
        "",
        selected.plan,
        selected.options,
        examples=1,
        minimize=False,
        replay_of=selected.replay_of,
        source_recipe_hash=selected.source_recipe_hash,
        schema_document=selected.schema_document,
        wfc_intake=selected.intake_context,
        wfc_execution=selected.execution_context,
    )


async def _read_asgi_body(receive):
    body = bytearray()
    while True:
        message = await receive()
        if message["type"] == "http.disconnect":
            raise ValueError("request disconnected")
        if message["type"] != "http.request":
            continue
        body.extend(message.get("body", b""))
        if len(body) > MAX_REQUEST_BYTES:
            raise ValueError("operator intake request exceeds the byte limit")
        if not message.get("more_body", False):
            return bytes(body)


async def _json_response(send, status, value):
    body = json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=True).encode("ascii")
    await send({
        "type": "http.response.start",
        "status": status,
        "headers": [(b"content-type", b"application/json"), (b"content-length", str(len(body)).encode("ascii"))],
    })
    await send({"type": "http.response.body", "body": body})


class OperatorIntakeRoutes:
    """Small ASGI router that leaves the FastMCP application unchanged."""

    def __init__(self, app, store_factory, identity_path_factory):
        self.app = app
        self.store_factory = store_factory
        self.identity_path_factory = identity_path_factory

    async def __call__(self, scope, receive, send):
        if scope.get("type") != "http" or scope.get("path") != ROUTE:
            await self.app(scope, receive, send)
            return
        if scope.get("method") != "POST":
            await _json_response(send, 405, {"exit_state": "operator_error", "complete": False, "reason": "POST required"})
            return
        headers = dict(scope.get("headers") or [])
        if not headers.get(b"content-type", b"").decode("latin-1").lower().startswith("application/json"):
            await _json_response(send, 415, {"exit_state": "operator_error", "complete": False, "reason": "application/json required"})
            return
        try:
            raw = await _read_asgi_body(receive)
            request = json.loads(
                raw.decode("ascii"),
                object_pairs_hook=_duplicate_rejecting_object,
                parse_constant=_reject_constant,
            )
            result = await asyncio.to_thread(
                submit, self.store_factory(), self.identity_path_factory(), request
            )
            await _json_response(send, 200, result)
        except (ValueError, TypeError, KeyError, IndexError, AttributeError, OSError, UnicodeDecodeError, json.JSONDecodeError) as exc:
            await _json_response(send, 400, {"exit_state": "operator_error", "complete": False, "reason": str(exc)})
