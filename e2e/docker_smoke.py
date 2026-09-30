#!/usr/bin/env python3
"""Exercise the shipped Compose file using a disposable local image and volume."""
import http.cookiejar
import json
import os
import pathlib
import re
import subprocess
import urllib.request
import uuid

root = pathlib.Path(__file__).resolve().parents[1]
project = "dingzi-smoke-" + uuid.uuid4().hex[:10]
env = {**os.environ, "DINGZI_IMAGE": os.environ.get("DINGZI_TEST_IMAGE", "dingzi-ci:local"),
       "DINGZI_BIND": "127.0.0.1", "DINGZI_PORT": "0", "DINGZI_SECURE_COOKIE": "false"}
compose = ["docker", "compose", "-f", str(root / "compose.yaml"), "-p", project]

def run(*args):
    return subprocess.check_output(args, env=env, text=True, encoding="utf-8", stderr=subprocess.PIPE).strip()

def dc(*args):
    return run(*compose, *args)

def client():
    return urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))

def request(opener, base, path, payload=None, method=None):
    data = None if payload is None else json.dumps(payload).encode()
    req = urllib.request.Request(base + path, data=data, method=method,
                                 headers={"Content-Type": "application/json"})
    with opener.open(req, timeout=5) as response:
        return json.load(response)

try:
    version = run("docker", "run", "--rm", "--read-only", "--network", "none", env["DINGZI_IMAGE"], "--version")
    assert version == "dingzi-server " + os.environ.get("DINGZI_TEST_VERSION", "ci"), "Wrong image version"
    dc("config", "--quiet")
    dc("up", "-d", "--pull", "never", "--wait", "--wait-timeout", "90")
    cid = dc("ps", "-q", "dingzi")
    info = json.loads(run("docker", "inspect", cid))[0]
    assert info["Config"]["User"] == "10001:10001", "Container does not use the unprivileged user"
    assert info["HostConfig"]["ReadonlyRootfs"], "Root filesystem is writable"
    assert "ALL" in info["HostConfig"]["CapDrop"], "Capabilities were not dropped"
    assert any("no-new-privileges" in item for item in info["HostConfig"]["SecurityOpt"])
    assert info["State"]["Health"]["Status"] == "healthy"
    address = dc("port", "dingzi", "8008")
    base = "http://" + address
    logs = dc("logs", "--no-log-prefix", "dingzi")
    password = re.search(r"管理员密码:\s*(\S+)", logs)
    assert password, "First-start credentials were not produced"
    password = password[1]
    opener = client()
    request(opener, base, "/api/v1/login", {"password": password})
    request(opener, base, "/api/v1/settings", {"site_name": "Compose persistence fixture", "description": "temporary", "terminal_enabled": False}, "PUT")
    original_config = run("docker", "exec", cid, "sha256sum", "/data/config.yaml").split()[0]
    assert run("docker", "exec", cid, "stat", "-c", "%a:%u", "/data/config.yaml") == "600:10001"
    dc("stop", "-t", "30", "dingzi")
    assert json.loads(run("docker", "inspect", cid))[0]["State"]["ExitCode"] == 0, "SIGTERM shutdown failed"
    dc("up", "-d", "--pull", "never", "--force-recreate", "--wait", "--wait-timeout", "90")
    cid = dc("ps", "-q", "dingzi")
    base = "http://" + dc("port", "dingzi", "8008")
    assert run("docker", "exec", cid, "sha256sum", "/data/config.yaml").split()[0] == original_config, "Credentials changed after recreation"
    opener = client()
    request(opener, base, "/api/v1/login", {"password": password})
    public = request(opener, base, "/api/v1/public/servers")
    assert public["site_name"] == "Compose persistence fixture", "SQLite state was not preserved"
    print("Compose smoke passed: health, non-root/read-only execution, login, SQLite/config persistence, graceful shutdown.")
finally:
    # Only this script's UUID-named project and its disposable volume are removed.
    dc("down", "--volumes", "--remove-orphans", "--timeout", "30")
