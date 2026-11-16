#!/usr/bin/env python3
"""Upgrade an old installation to default web updates using disposable data.

Usage: python3 scripts/test_default_updates.py OLD_BUNDLE NEW_BUNDLE
On Linux run as root, matching the private files written by the Docker agent.
"""
import http.cookiejar
import json
import os
from pathlib import Path
import secrets
import socket
import subprocess
import sys
import tarfile
import tempfile
import time
import urllib.error
import urllib.request


def run(*args, **kwargs):
    result = subprocess.run(args, capture_output=True, text=True, **kwargs)
    assert result.returncode == 0, result.stdout + result.stderr
    return result


def extract(bundle, root):
    with tarfile.open(bundle) as archive:
        archive.extractall(root, filter="data")


def scenario(legacy_agent):
    old, new = [Path(path).resolve() for path in sys.argv[1:3]]
    project = "auth-default-test-" + secrets.token_hex(5)
    with tempfile.TemporaryDirectory(prefix="auth-default-test-") as temporary:
        root = Path(temporary)
        extract(old, root)
        app = root / "auth-center"
        old_version = (app / "RELEASE").read_text().strip()
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            port = sock.getsockname()[1]
        password = secrets.token_urlsafe(24)
        # No opt-in variables, just the original administrator configuration.
        (app / ".env").write_text(f"AUTH_PORT={port}\nAUTH_PUBLIC_URL=http://127.0.0.1:{port}\nAUTH_ADMIN_EMAIL=upgrade@example.test\nAUTH_ADMIN_PASSWORD='{password}'\n")
        (app / ".env").chmod(0o600)
        env = {**os.environ, "AUTH_PROJECT_NAME": project}
        compose = ["docker", "compose", "-p", project, "--env-file", str(app / ".env"), "-f", str(app / "compose.yaml")]
        old_agent_compose = ["docker", "compose", "-p", project + "-deploy", "--env-file", str(app / ".env"), "-f", str(app / "compose.deploy.yaml")]
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))

        def api(path, body=None):
            data = json.dumps(body).encode() if body is not None else None
            req = urllib.request.Request(f"http://127.0.0.1:{port}{path}", data=data,
                                         headers={"Content-Type": "application/json", "Origin": f"http://127.0.0.1:{port}"})
            for attempt in range(20):
                try:
                    with opener.open(req, timeout=10) as response:
                        payload = response.read()
                        return json.loads(payload) if payload else None
                except (urllib.error.URLError, ConnectionError):
                    if body is not None or attempt == 19:
                        raise
                    time.sleep(1)

        def ctl(action, allow=False):
            return run("sh", "./authctl.sh", action, cwd=app,
                       env={**env, "AUTH_ALLOW_SCHEMA_UPGRADE": str(allow).lower()})

        try:
            ctl("install")
            api("/api/auth/login", {"email": "upgrade@example.test", "password": password})
            assert not api("/api/admin/deployment")["enabled"]
            api("/api/applications", {"name": "默认更新迁移测试", "description": "保留已有账户与应用"})
            saved_apps = api("/api/applications")
            if legacy_agent:
                agent_id = (app / "deploy-agent/image.id").read_text().strip()
                run("docker", "load", "-i", str(app / "deploy-agent/image.tar"))
                run(*old_agent_compose, "up", "-d", "--no-build", "--pull", "never", cwd=app,
                    env={**env, "AUTH_DEPLOY_AGENT_IMAGE": agent_id})
            before = run("docker", "run", "--rm", "--network", "none", "--entrypoint", "sha256sum",
                         "-v", f"{project}_auth-data:/data:ro", (app / "packages" / f"{old_version}.meta").read_text().splitlines()[0], "/data/master.key").stdout
            # Same installation directory, old .env unchanged. The new host
            # installer must start the new agent and wire the Auth connection.
            extract(new, root)
            new_version = (app / "RELEASE").read_text().strip()
            # A feature release can also introduce an explicit schema migration.
            # Default agent setup must never bypass that opt-in gate.
            ctl("update", allow=True)
            assert api("/api/status")["version"] == new_version
            deployment = api("/api/admin/deployment")
            assert deployment["enabled"] and deployment["connected"], deployment
            assert deployment["current"]["health"] == "healthy"
            assert api("/api/applications") == saved_apps
            image_id = (app / "packages" / f"{new_version}.meta").read_text().splitlines()[0]
            after = run("docker", "run", "--rm", "--network", "none", "--entrypoint", "sha256sum",
                        "-v", f"{project}_auth-data:/data:ro", image_id, "/data/master.key").stdout
            assert before == after, "master key replaced"
            active = run("docker", "ps", "-q", "--filter", f"label=com.docker.compose.project={project}-deploy", "--filter", "label=com.docker.compose.service=deploy-agent").stdout.strip()
            assert not active, "old independent agent still running"
            assert (app / ".env").read_text().count("AUTH_DEPLOY_SOCKET") == 0
            print("PASS default updates from " + ("old separate agent" if legacy_agent else "old installation without agent") + "; existing env, session, app and key retained", flush=True)
        finally:
            # Both project names and their volumes belong only to this test.
            run(*old_agent_compose, "down", cwd=app, env=env)
            run(*compose, "down", "-v", cwd=app, env=env)


if __name__ == "__main__":
    scenario(False)
    scenario(True)
