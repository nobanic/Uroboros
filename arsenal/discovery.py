"""Typed backend stages for task-bound host discovery.

Only Uroboros constructs these leases. Subfinder is restricted to two audited,
no-key passive providers; naabu receives frozen IP literals and an authenticated
SOCKS lease; httpx receives only hostname/port pairs already opened by naabu.
"""

import hmac
import ipaddress
import json
import math
from pathlib import Path
from urllib.parse import urlparse

from arsenal import contracts as c, parsers, taskrun


PASSIVE_SOURCES = ("anubis", "rapiddns")
REQUIRED_CONFIG = {"ports", "max_subdomains", "max_addresses_per_host", "budget_seconds"}


def validate_config(config):
    if not isinstance(config, dict) or set(config) != REQUIRED_CONFIG:
        raise ValueError(
            "discovery config must contain only ports, max_subdomains, "
            "max_addresses_per_host and budget_seconds"
        )
    ports = config.get("ports")
    if not isinstance(ports, list) or not 1 <= len(ports) <= 8:
        raise ValueError("ports must contain 1..8 integers")
    if any(isinstance(port, bool) or not isinstance(port, int) or not 1 <= port <= 65535
           for port in ports):
        raise ValueError("ports must contain integers between 1 and 65535")
    if ports != sorted(set(ports)):
        raise ValueError("ports must be sorted and unique")
    taskrun._integer(config, "max_subdomains", 0, 1, 50)
    taskrun._integer(config, "max_addresses_per_host", 0, 1, 2)
    taskrun._integer(config, "budget_seconds", 0, 10, 600)
    return config


def parse_contract(task_id, run_id, domain, scope_json, expected_scope_hash,
                   config_json, expected_config_hash):
    if isinstance(task_id, bool) or not isinstance(task_id, int) or task_id <= 0:
        raise ValueError("task_id must be a positive integer")
    if not taskrun._RUN_ID.fullmatch(str(run_id or "")):
        raise ValueError("run_id is invalid")
    try:
        scope = taskrun.normalize_scope(json.loads(scope_json))
        config = validate_config(json.loads(config_json))
    except json.JSONDecodeError as exc:
        raise ValueError("scope/config JSON is invalid") from exc
    domain = taskrun._clean_host(domain)
    try:
        ipaddress.ip_address(domain)
    except ValueError:
        pass
    else:
        raise ValueError("discovery domain cannot be an IP literal")
    if "." not in domain or domain not in scope["root_domains"]:
        raise ValueError("discovery domain must be an authorized root domain")
    actual_scope_hash = taskrun.scope_hash(scope)
    if not expected_scope_hash or not hmac.compare_digest(actual_scope_hash, expected_scope_hash):
        raise ValueError("frozen scope hash mismatch")
    actual_config_hash = taskrun.config_hash("discovery", domain, config)
    if not expected_config_hash or not hmac.compare_digest(actual_config_hash, expected_config_hash):
        raise ValueError("discovery config hash mismatch")
    return {
        "task_id": task_id,
        "run_id": str(run_id),
        "tool": "discovery",
        "domain": domain,
        "scope": scope,
        "scope_hash": actual_scope_hash,
        "config": config,
        "config_hash": actual_config_hash,
    }


def validate_proxy(raw, scheme):
    parsed = urlparse(str(raw or "").strip())
    if parsed.scheme != scheme or not parsed.hostname or not parsed.port:
        raise ValueError("task stage requires an authenticated %s proxy URL" % scheme)
    if parsed.username != "uroboros" or not parsed.password or len(parsed.password) < 32:
        raise ValueError("task stage proxy credentials are missing")
    return str(raw).strip()


def parse_dns_targets(raw, contract):
    try:
        values = json.loads(raw)
    except json.JSONDecodeError as exc:
        raise ValueError("DNS targets JSON is invalid") from exc
    maximum = ((contract["config"]["max_subdomains"] + 1) *
               contract["config"]["max_addresses_per_host"])
    if not isinstance(values, list) or len(values) > maximum:
        raise ValueError("DNS targets exceed the frozen discovery bound")
    out, seen = [], set()
    for value in values:
        if not isinstance(value, dict) or set(value) != {"host", "address"}:
            raise ValueError("each DNS target must contain only host and address")
        host = taskrun._clean_host(value["host"])
        if host != contract["domain"] and not host.endswith("." + contract["domain"]):
            raise ValueError("DNS target host is outside the discovery domain")
        try:
            address = ipaddress.ip_address(str(value["address"]).strip().strip("[]")).compressed
        except ValueError as exc:
            raise ValueError("DNS target address is invalid") from exc
        key = (host, address)
        if key not in seen:
            seen.add(key)
            out.append({"host": host, "address": address})
    return out


def parse_service_targets(raw, contract):
    try:
        values = json.loads(raw)
    except json.JSONDecodeError as exc:
        raise ValueError("service targets JSON is invalid") from exc
    maximum = (contract["config"]["max_subdomains"] + 1) * len(contract["config"]["ports"])
    if not isinstance(values, list) or len(values) > maximum:
        raise ValueError("service targets exceed the frozen discovery bound")
    out, seen = [], set()
    for value in values:
        if not isinstance(value, dict) or set(value) != {"host", "address", "port"}:
            raise ValueError("each service target must contain only host, address and port")
        host = taskrun._clean_host(value["host"])
        if host != contract["domain"] and not host.endswith("." + contract["domain"]):
            raise ValueError("service target host is outside the discovery domain")
        try:
            address = ipaddress.ip_address(str(value["address"]).strip().strip("[]")).compressed
        except ValueError as exc:
            raise ValueError("service target address is invalid") from exc
        port = value["port"]
        if (isinstance(port, bool) or not isinstance(port, int) or
                port not in contract["config"]["ports"]):
            raise ValueError("service target port is outside the typed port set")
        key = (host, address, port)
        if key not in seen:
            seen.add(key)
            out.append({"host": host, "address": address, "port": port})
    return out


def subfinder_command(contract, proxy_url, runtime_dir):
    proxy_url = validate_proxy(proxy_url, "http")
    config = contract["config"]
    timeout = min(c.CAPS["subfinder"]["timeout"], config["budget_seconds"])
    runtime = Path(runtime_dir)
    config_path = runtime / "subfinder-config.yaml"
    provider_path = runtime / "subfinder-provider-config.yaml"
    config_path.write_text("{}\n", encoding="utf-8")
    provider_path.write_text("{}\n", encoding="utf-8")
    argv = [
        "subfinder", "-d", contract["domain"], "-s", ",".join(PASSIVE_SOURCES),
        "-oJ", "-cs", "-silent", "-duc", "-rl", str(c.CAPS["subfinder"]["rps"]),
        "-timeout", "5", "-max-time", str(max(1, math.ceil(timeout / 60))),
        "-max-results", str(config["max_subdomains"]), "-rsr", str(c.OUTPUT_CAP_BYTES),
        "-config", str(config_path), "-pc", str(provider_path), "-proxy", proxy_url,
    ]
    return argv, timeout, parsers.parse_subfinder, {
        "stage": "subfinder",
        "sources": list(PASSIVE_SOURCES),
        "provider_proxy_enforced": True,
        "max_subdomains": config["max_subdomains"],
    }


def naabu_command(contract, dns_targets, socks_url, runtime_dir):
    socks_url = validate_proxy(socks_url, "socks5")
    parsed = urlparse(socks_url)
    addresses = sorted({item["address"] for item in dns_targets})
    if not addresses:
        raise ValueError("naabu requires at least one frozen DNS address")
    targets_path = Path(runtime_dir) / "naabu-targets.txt"
    targets_path.write_text("\n".join(addresses) + "\n", encoding="utf-8")
    ports = ",".join(str(port) for port in contract["config"]["ports"])
    timeout = min(c.CAPS["naabu"]["timeout"], contract["config"]["budget_seconds"])
    # Do not add naabu's -verify flag here. In v2.6.1 ConnectVerification uses
    # net.DialTimeout directly instead of the configured SOCKS dialer, which
    # would turn its second pass into unaudited egress outside the frozen lease.
    argv = [
        "naabu", "-l", str(targets_path), "-p", ports, "-s", "c", "-Pn",
        "-j", "-silent", "-duc", "-no-stdin", "-rate", str(c.CAPS["naabu"]["rps"]),
        "-c", str(c.CAPS["naabu"]["conc"]), "-retries", "1", "-timeout", "1000",
        "-iv", "4,6", "-proxy", parsed.netloc.rsplit("@", 1)[-1],
        "-proxy-auth", "%s:%s" % (parsed.username, parsed.password),
    ]
    return argv, timeout, parsers.parse_naabu, {
        "stage": "naabu",
        "scan_type": "connect",
        "socks_proxy_enforced": True,
        "address_count": len(addresses),
        "ports": contract["config"]["ports"],
    }


def httpx_command(contract, service_targets, proxy_url, runtime_dir):
    proxy_url = validate_proxy(proxy_url, "http")
    host_ports = sorted({"%s:%d" % (item["host"], item["port"])
                         for item in service_targets})
    if not host_ports:
        raise ValueError("httpx requires at least one naabu-open service target")
    targets_path = Path(runtime_dir) / "httpx-targets.txt"
    targets_path.write_text("\n".join(host_ports) + "\n", encoding="utf-8")
    timeout = min(300, contract["config"]["budget_seconds"])
    argv = [
        "httpx", "-l", str(targets_path), "-json", "-title", "-tech-detect", "-status-code",
        "-silent", "-rl", str(c.CAPS["httpx"]["rps"]),
        "-threads", str(c.CAPS["httpx"]["conc"]), "-fr", "-maxr", "5", "-nfs", "-duc",
        "-no-stdin", "-timeout", "5", "-retries", "0", "-http-proxy", proxy_url,
    ]
    return argv, timeout, parsers.parse_httpx, {
        "stage": "httpx",
        "follow_redirects": True,
        "proxy_enforced": True,
        "target_count": len(host_ports),
    }
