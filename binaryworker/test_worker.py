import hashlib
import json
import os
import socket
import struct
import sys
import tempfile
import threading
import time
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent))
import worker


class FakeHeader:
    machine_type = "AMD64\nignore"
    object_type = "EXECUTABLE"
    endianness = "LITTLE"


class FakeSection:
    def __init__(self, name, offset, address):
        self.name = name
        self.offset = offset
        self.size = 10
        self.virtual_address = address
        self.virtual_size = 20
        self.entropy = 4.123456789


class FakeFunction:
    def __init__(self, name):
        self.name = name


class FakeBinary:
    format = "PE"
    entrypoint = 4096
    imagebase = 4194304
    virtual_size = 8192
    original_size = 512
    header = FakeHeader()
    sections = [FakeSection(".data", 200, 8192), FakeSection(".text", 100, 4096)]
    imported_functions = [FakeFunction("zeta"), FakeFunction("alpha")]
    imports = []
    exported_functions = [FakeFunction("exported")]
    libraries = ["KERNEL32.dll", "KERNEL32.dll"]


def fake_capa(_path, assignment, _connection):
    summary = {
        "analysis": {"arch": "amd64", "extractor": "fixture", "format": "pe", "os": "windows"},
        "artifact_sha256": assignment["sha256"],
        "authority": "candidate",
        "candidate_count": 0,
        "diagnostics_truncated": False,
        "emitted_count": 0,
        "excluded_internal_rule_count": 0,
        "excluded_library_rule_count": 0,
        "profile_revision": worker.PROFILE_REVISION,
        "raw_rule_match_count": 0,
        "schema_version": 1,
        "truncated": False,
        "verified": False,
    }
    stage_projection = worker.canonical_json({"candidates": [], "summary": summary})
    return {
        "analyzer_version": worker.CAPA_VERSION,
        "candidates": [],
        "projection_hash": hashlib.sha256(stage_projection).hexdigest(),
        "raw_result_hash": hashlib.sha256(b"fake-capa").hexdigest(),
        "status": "completed",
        "summary": summary,
        "truncated": False,
    }


def fake_floss(_path, assignment, _connection):
    summary = {
        "artifact_sha256": assignment["sha256"],
        "authority": "candidate",
        "candidate_count": 0,
        "diagnostics_truncated": False,
        "duplicate_count": 0,
        "emitted_count": 0,
        "functions_analyzed_decoded": 0,
        "functions_discovered": 0,
        "functions_library": 0,
        "profile_revision": worker.PROFILE_REVISION,
        "raw_decoded_string_count": 0,
        "schema_version": 1,
        "truncated": False,
        "verified": False,
    }
    stage_projection = worker.canonical_json({"candidates": [], "summary": summary})
    return {
        "analyzer_version": worker.FLOSS_VERSION,
        "candidates": [],
        "projection_hash": hashlib.sha256(stage_projection).hexdigest(),
        "raw_result_hash": hashlib.sha256(b"fake-floss").hexdigest(),
        "status": "completed",
        "summary": summary,
        "truncated": False,
    }


class WorkerProjectionTests(unittest.TestCase):
    def test_standard_fingerprint_matches_host_contract(self):
        policy = worker.STANDARD_LIMIT_PROFILE
        payload = {
            "analyzer": worker.ANALYZER,
            "analyzer_version": worker.ANALYZER_VERSION,
            "blob_sha256": "b" * 64,
            "limit_profile": [{"key": key, "value": policy[key]} for key in sorted(policy)],
            "profile_revision": worker.PROFILE_REVISION,
            "tool_rule_pins": [
                {"key": key, "value": worker.STANDARD_TOOL_RULE_PINS[key]}
                for key in sorted(worker.STANDARD_TOOL_RULE_PINS)
            ],
            "worker_image_digest": "sha256:" + "a" * 64,
        }
        self.assertEqual(
            hashlib.sha256(worker.canonical_json(payload)).hexdigest(),
            "0734d676617536b951c7c40fc4d0a5daf6906239d9a6605da147fcfd4010a9c0",
        )

    def test_projection_is_canonical_bounded_and_sorted(self):
        raw, truncated = worker.build_projection(FakeBinary(), "a" * 64, 512)
        self.assertFalse(truncated)
        self.assertEqual(raw, worker.canonical_json(json.loads(raw)))
        value = json.loads(raw)
        self.assertEqual(value["imports"], ["alpha", "zeta"])
        self.assertEqual([item["name"] for item in value["sections"]], [".text", ".data"])
        self.assertEqual(value["binary"]["header"]["machine_type"], "AMD64ignore")
        self.assertLessEqual(len(raw), worker.MAX_PROJECTION_BYTES)

    def test_one_connection_handles_exactly_one_assignment(self):
        identity = "sha256:" + "b" * 64
        artifact = b"MZ fixture"
        artifact_hash = hashlib.sha256(artifact).hexdigest()
        server, client = socket.socketpair()
        old_tempdir = tempfile.TemporaryDirectory

        def local_tempdir(*args, **kwargs):
            kwargs["dir"] = None
            return old_tempdir(*args, **kwargs)

        errors = []

        def run():
            try:
                with mock.patch.object(worker.tempfile, "TemporaryDirectory", side_effect=local_tempdir):
                    worker.run_once(client, identity, lambda _: FakeBinary(), fake_capa, fake_floss)
            except Exception as exc:  # pragma: no cover - assertion captures it
                errors.append(exc)
            finally:
                client.close()

        thread = threading.Thread(target=run)
        thread.start()
        hello = worker.read_frame(server, 16 << 10)
        self.assertEqual(hello["worker_identity"], identity)
        assignment = {
            "expected_size": len(artifact),
            "fingerprint": "",
            "job_id": "binary-job-" + "1" * 32,
            "lease_nonce": "lease-" + "2" * 32,
            "limit_profile": dict(worker.STANDARD_LIMIT_PROFILE),
            "profile_revision": worker.PROFILE_REVISION,
            "sha256": artifact_hash,
            "tool_rule_pins": dict(worker.STANDARD_TOOL_RULE_PINS),
            "version": worker.WORKER_PROTOCOL_VERSION,
        }
        fingerprint_payload = {
            "analyzer": worker.ANALYZER,
            "analyzer_version": worker.ANALYZER_VERSION,
            "blob_sha256": artifact_hash,
            "limit_profile": [
                {"key": key, "value": worker.STANDARD_LIMIT_PROFILE[key]}
                for key in sorted(worker.STANDARD_LIMIT_PROFILE)
            ],
            "profile_revision": worker.PROFILE_REVISION,
            "tool_rule_pins": [
                {"key": key, "value": worker.STANDARD_TOOL_RULE_PINS[key]}
                for key in sorted(worker.STANDARD_TOOL_RULE_PINS)
            ],
            "worker_image_digest": identity,
        }
        assignment["fingerprint"] = hashlib.sha256(worker.canonical_json(fingerprint_payload)).hexdigest()
        worker.write_frame(server, assignment, worker.MAX_ASSIGNMENT_BYTES)
        server.sendall(artifact)
        result = worker.read_frame(server, worker.MAX_RESULT_BYTES)
        self.assertEqual(result["status"], "completed")
        self.assertEqual(result["artifact_sha256"], artifact_hash)
        encoded_projection = worker.canonical_json(result["projection"])
        self.assertEqual(result["canonical_projection_hash"], hashlib.sha256(encoded_projection).hexdigest())
        self.assertEqual(result["projection"]["lief"]["record"]["binary"]["format"], "PE")
        self.assertEqual(result["projection"]["capa"]["summary"]["candidate_count"], 0)
        self.assertEqual(result["projection"]["floss"]["summary"]["candidate_count"], 0)
        thread.join(2)
        self.assertFalse(thread.is_alive())
        self.assertEqual(errors, [])
        server.close()

    def test_assignment_rejects_unpinned_policy(self):
        valid = {
            "expected_size": 1,
            "fingerprint": "c" * 64,
            "job_id": "binary-job-" + "3" * 32,
            "lease_nonce": "lease-" + "4" * 32,
            "limit_profile": {},
            "profile_revision": worker.PROFILE_REVISION,
            "sha256": "d" * 64,
            "tool_rule_pins": {"lief": "latest"},
            "version": worker.WORKER_PROTOCOL_VERSION,
        }
        with self.assertRaises(worker.ProtocolError):
            worker.validate_assignment(valid)

    def test_assignment_rejects_limit_profile_drift(self):
        identity = "sha256:" + "a" * 64
        assignment = {
            "expected_size": 1,
            "fingerprint": "c" * 64,
            "job_id": "binary-job-" + "5" * 32,
            "lease_nonce": "lease-" + "6" * 32,
            "limit_profile": dict(worker.STANDARD_LIMIT_PROFILE),
            "profile_revision": worker.PROFILE_REVISION,
            "sha256": "d" * 64,
            "tool_rule_pins": dict(worker.STANDARD_TOOL_RULE_PINS),
            "version": worker.WORKER_PROTOCOL_VERSION,
        }
        assignment["limit_profile"]["wall_seconds"] += 1
        with self.assertRaises(worker.ProtocolError):
            worker.validate_assignment(assignment, identity)

    @unittest.skipUnless(hasattr(os, "fork"), "hard timeout requires Linux fork isolation")
    def test_hard_timeout_kills_parser_child(self):
        connection, peer = socket.socketpair()
        try:
            with tempfile.TemporaryDirectory() as directory:
                artifact = Path(directory) / "artifact.bin"
                artifact.write_bytes(b"fixture")

                def slow_parser(_):
                    time.sleep(5)
                    return FakeBinary()

                started = time.monotonic()
                result = worker.analyze_with_hard_timeout(
                    connection,
                    artifact,
                    {"expected_size": 7, "limit_profile": {"lief_wall_seconds": 1}},
                    slow_parser,
                )
                self.assertLess(time.monotonic() - started, 3)
                self.assertEqual(result["status"], "failed")
                self.assertEqual(result["exit_class"], "resource_limit")
                self.assertEqual(result["error_class"], "wall_timeout")
        finally:
            connection.close()
            peer.close()

    @unittest.skipUnless(hasattr(os, "fork"), "child crash handling requires Linux fork isolation")
    def test_parser_child_crash_fails_closed(self):
        connection, peer = socket.socketpair()
        try:
            with tempfile.TemporaryDirectory() as directory:
                artifact = Path(directory) / "artifact.bin"
                artifact.write_bytes(b"fixture")

                def crashing_parser(_):
                    os._exit(17)

                result = worker.analyze_with_hard_timeout(
                    connection,
                    artifact,
                    {"expected_size": 7, "limit_profile": {"lief_wall_seconds": 2}},
                    crashing_parser,
                )
                self.assertEqual(result["status"], "failed")
                self.assertEqual(result["exit_class"], "analyzer_failure")
                self.assertEqual(result["error_class"], "worker_child")
        finally:
            connection.close()
            peer.close()

    def test_capa_normalization_is_candidate_only_and_drops_volatile_trees(self):
        artifact_hash = "a" * 64
        assignment = {"sha256": artifact_hash}
        document = {
            "meta": {
                "version": worker.CAPA_VERSION,
                "timestamp": "volatile",
                "argv": ["prompt injection"],
                "sample": {"sha256": artifact_hash, "path": "/volatile/path"},
                "flavor": "static",
                "analysis": {"format": "pe", "arch": "amd64", "os": "windows", "extractor": "fixture"},
            },
            "rules": {
                "rule\x1b[31m name": {
                    "meta": {
                        "name": "rule\x1b[31m name",
                        "namespace": "fixture\nnamespace",
                        "scopes": {"static": "function"},
                        "attack": [{"id": "T0001", "tactic": "Execution"}],
                        "mbc": [],
                        "lib": False,
                        "is_subscope_rule": False,
                    },
                    "source": "must not survive",
                    "matches": [[{"type": "absolute", "value": 4096}, {"children": ["large tree"]}]],
                },
                "library helper": {
                    "meta": {"name": "library helper", "lib": True},
                    "matches": [],
                },
            },
        }
        summary, candidates, truncated = worker.normalize_capa(worker.canonical_json(document), assignment, False)
        self.assertFalse(truncated)
        self.assertEqual(summary["candidate_count"], 1)
        self.assertEqual(summary["excluded_library_rule_count"], 1)
        self.assertEqual(len(candidates), 1)
        encoded = worker.canonical_json(candidates[0])
        self.assertNotIn(b"source", encoded)
        self.assertNotIn(b"large tree", encoded)
        self.assertNotIn(b"\\u001b", encoded)
        self.assertEqual(candidates[0]["record"]["authority"], "candidate")
        self.assertFalse(candidates[0]["record"]["verified"])
        rule = candidates[0]["record"]["rule"]
        identity = (
            b"capa-rule-v1\x00"
            + rule["name"].encode("utf-8")
            + b"\x00"
            + rule["namespace"].encode("utf-8")
        )
        self.assertEqual(
            candidates[0]["stable_key"],
            f"capa:rule:{hashlib.sha256(identity).hexdigest()}:{worker.PROFILE_REVISION}",
        )

    def test_floss_normalization_is_candidate_only_deterministic_and_escaped(self):
        artifact_hash = "f" * 64
        document = {
            "metadata": {
                "file_path": "/volatile/artifact.bin",
                "version": worker.FLOSS_VERSION,
                "runtime": {"start_date": "volatile", "total": 99.5},
            },
            "analysis": {
                "enable_static_strings": False,
                "enable_stack_strings": False,
                "enable_tight_strings": False,
                "enable_decoded_strings": True,
                "functions": {
                    "discovered": 4,
                    "library": 1,
                    "analyzed_decoded_strings": 2,
                    "decoding_function_scores": {"4096": {"score": 9.9}},
                },
            },
            "strings": {
                "decoded_strings": [
                    {
                        "address": 8192,
                        "address_type": "GLOBAL",
                        "string": "command\x1b[31m\\literal",
                        "encoding": "ASCII",
                        "decoded_at": 4096,
                        "decoding_routine": 12288,
                    },
                    {
                        "address": 8192,
                        "address_type": "GLOBAL",
                        "string": "command\x1b[31m\\literal",
                        "encoding": "ASCII",
                        "decoded_at": 4096,
                        "decoding_routine": 12288,
                    },
                ],
                "static_strings": [{"string": "must not survive"}],
            },
        }
        summary, candidates, truncated = worker.normalize_floss(
            worker.canonical_json(document), {"sha256": artifact_hash}, False,
        )
        self.assertFalse(truncated)
        self.assertEqual(summary["raw_decoded_string_count"], 2)
        self.assertEqual(summary["candidate_count"], 1)
        self.assertEqual(summary["duplicate_count"], 1)
        self.assertEqual(len(candidates), 1)
        encoded = worker.canonical_json(candidates[0])
        self.assertNotIn(b"volatile", encoded)
        self.assertNotIn(b"must not survive", encoded)
        self.assertIn(b"\\\\u001b", encoded)
        self.assertEqual(candidates[0]["record"]["authority"], "candidate")
        self.assertFalse(candidates[0]["record"]["verified"])

    def test_floss_admission_gates_oversize_and_unknown_formats_without_process(self):
        assignment = {
            "sha256": "a" * 64,
            "expected_size": worker.MAX_FLOSS_ARTIFACT_BYTES + 1,
            "limit_profile": dict(worker.STANDARD_LIMIT_PROFILE),
        }
        with tempfile.TemporaryDirectory() as directory:
            artifact = Path(directory) / "artifact.bin"
            artifact.write_bytes(b"MZ")
            stage = worker.analyze_floss(artifact, assignment, mock.Mock())
            self.assertEqual(stage["status"], "resource_gated")
            self.assertEqual(stage["summary"]["reason_code"], "artifact_too_large")
            assignment["expected_size"] = 4
            artifact.write_bytes(b"raw!")
            stage = worker.analyze_floss(artifact, assignment, mock.Mock())
            self.assertEqual(stage["status"], "unsupported")
            self.assertEqual(stage["summary"]["reason_code"], "unsupported_format")


if __name__ == "__main__":
    unittest.main()
