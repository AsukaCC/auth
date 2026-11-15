#!/usr/bin/env python3
"""Isolated real schema-1 -> schema-2 Docker migration and recovery checks.

Usage: python3 scripts/test_schema_upgrade.py OLD_BUNDLE NEW_BUNDLE
Only temporary projects/volumes are created and removed; supplied bundles remain
untouched. Run with Python 3.12+ and Docker Compose v2.20+.
"""
import http.cookiejar
import hashlib
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
    return subprocess.run(args, capture_output=True, text=True, errors="replace", **kwargs)


def extract(bundle, directory):
    with tarfile.open(bundle) as archive:
        archive.extractall(directory, filter="data")
    return directory / "auth-center"


def scenario(adopt=False):
    old_bundle, new_bundle = [Path(arg).resolve() for arg in sys.argv[1:3]]
    project = "auth-schema-test-" + secrets.token_hex(6)
    with tempfile.TemporaryDirectory(prefix="auth-schema-test-") as directory:
        root = Path(directory)
        app = extract(old_bundle, root)
        old_version = (app / "RELEASE").read_text().strip()
        old_meta = (app / "packages" / f"{old_version}.meta").read_text().splitlines()
        assert old_meta[2] == "1", "old bundle must contain actual schema 1"
        password = secrets.token_urlsafe(24)
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            port = sock.getsockname()[1]
        (app / ".env").write_text(f"AUTH_PORT={port}\nAUTH_PUBLIC_URL=http://127.0.0.1:{port}\nAUTH_ADMIN_EMAIL=migration@example.test\nAUTH_ADMIN_PASSWORD='{password}'\n")
        (app / ".env").chmod(0o600)
        env = {**os.environ, "AUTH_PROJECT_NAME": project}
        images = []
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))

        def ctl(action, expected=0, allow=False):
            result = run("sh", "./authctl.sh", action, cwd=app, env={**env, "AUTH_ALLOW_SCHEMA_UPGRADE": str(allow).lower()})
            assert (result.returncode == 0) == (expected == 0), result.stdout + result.stderr
            return result

        def api(path, body=None, method=None):
            data = json.dumps(body).encode() if body is not None else None
            request = urllib.request.Request(f"http://127.0.0.1:{port}{path}", data=data, method=method, headers={"Content-Type": "application/json", "Origin": f"http://127.0.0.1:{port}"})
            for attempt in range(15):
                try:
                    with opener.open(request, timeout=15) as response:
                        payload = response.read()
                        return json.loads(payload) if payload else None
                except (urllib.error.URLError, ConnectionError):
                    if body is not None or attempt == 14:
                        raise
                    time.sleep(1)

        def key_hash(image):
            result = run("docker", "run", "--rm", "--network", "none", "--entrypoint", "sha256sum", "-v", f"{project}_auth-data:/data:ro", image, "/data/master.key")
            assert result.returncode == 0, result.stderr
            return result.stdout

        try:
            if adopt:
                loaded = run("docker", "load", "-i", str(app / "packages" / f"{old_version}.tar"))
                assert loaded.returncode == 0, loaded.stderr
                started = run("docker", "compose", "-p", project, "--env-file", str(app / ".env"), "-f", str(app / "compose.yaml"), "up", "-d", "--no-build", "--pull", "never", "--wait", env={**env, "AUTH_IMAGE": old_meta[0]})
                assert started.returncode == 0, started.stderr
            else:
                ctl("install")
            assert api("/api/status")["schema_version"] == 1
            login = api("/api/auth/login", {"email": "migration@example.test", "password": password})
            uid = login["user"]["id"]
            api("/api/applications", {"name": "迁移保留应用", "description": "旧库升级验证"})
            saved_apps = api("/api/applications")
            original_key = key_hash(old_meta[0])
            state_file = app / ".deployment/state"
            original_state = state_file.read_text() if state_file.exists() else None
            extract(new_bundle, root)
            new_version = (app / "RELEASE").read_text().strip()
            new_meta = (app / "packages" / f"{new_version}.meta").read_text().splitlines()
            assert new_meta[2] == "2"
            action = "adopt" if adopt else "update"
            rejected = ctl(action, expected=1)
            assert "AUTH_ALLOW_SCHEMA_UPGRADE" in rejected.stderr, rejected.stdout + rejected.stderr
            assert (state_file.read_text() if state_file.exists() else None) == original_state
            assert not (app / ".deployment/pending").exists()
            assert api("/api/status")["schema_version"] == 1
            if adopt:
                # The new binary migrates the real old database, but its Docker
                # health probe fails. Automatic recovery must keep schema 2.
                base_tag, broken_tag = f"{project}:base", f"{project}:broken"
                tagged = run("docker", "tag", new_meta[0], base_tag)
                assert tagged.returncode == 0, tagged.stderr
                images.append(base_tag)
                broken_version = new_version + "-health-failure"
                platform = run("docker", "image", "inspect", "--format", "{{.Os}}/{{.Architecture}}", new_meta[0]).stdout.strip()
                built = run("docker", "build", "--platform", platform, "-t", broken_tag, "-", input=f"FROM {base_tag}\nLABEL org.opencontainers.image.version={broken_version}\nHEALTHCHECK --interval=1s --timeout=1s --start-period=0s --retries=1 CMD exit 1\n")
                assert built.returncode == 0, built.stderr
                images.append(broken_tag)
                broken_id = run("docker", "image", "inspect", "--format", "{{.Id}}", broken_tag).stdout.strip()
                package = app / "packages" / f"{broken_version}.tar"
                saved = run("docker", "save", "-o", str(package), broken_tag)
                assert saved.returncode == 0, saved.stderr
                (app / "packages" / f"{broken_version}.meta").write_text(f"{broken_id}\n{hashlib.sha256(package.read_bytes()).hexdigest()}\n2\n")
                (app / "RELEASE").write_text(broken_version + "\n")
                ctl(action, expected=1, allow=True)
                assert (app / ".deployment/pending").read_text().strip() == broken_version
                assert state_file.read_text().splitlines()[0] == "legacy"
                assert (app / ".deployment/schema-floor").read_text().strip() == "2"
                assert api("/api/status")["schema_version"] == 2
                container = run("docker", "ps", "-q", "--filter", f"label=com.docker.compose.project={project}", "--filter", "label=com.docker.compose.service=auth").stdout.strip()
                assert run("docker", "inspect", "--format", "{{.Image}}", container).stdout.strip() == broken_id
                # Model an operator selecting the already imported healthy new
                # image; recover still enforces its schema label before launch.
                (app / ".deployment/pending").write_text(new_version + "\n")
                ctl("recover")
                (app / "RELEASE").write_text(new_version + "\n")
                print("PASS migrated database with failed health: no old binary restart, journal retained, compatible forward recovery", flush=True)
            else:
                ctl(action, allow=True)
            original_state = original_state or f"legacy\n{old_meta[0]}\n\n\n"
            assert api("/api/status")["schema_version"] == 2
            assert api("/api/applications") == saved_apps
            assert original_key == key_hash(new_meta[0])
            stats = api("/api/admin/analytics")
            assert stats["summary"]["logins_30d"] == 1
            assert stats["summary"]["active_today"] == 1
            assert api(f"/api/admin/users/{uid}")["profile"]["revision"] == 0
            profile = api("/api/profile")
            values = {key: value for key, value in profile.items() if key not in {"revision", "updated_at"}}
            values.update(organization="迁移后资料", expected_revision=0)
            api("/api/profile", values, "PUT")
            new_state = (app / ".deployment/state").read_text()
            rejected = ctl("rollback", expected=1)
            assert "禁止" in rejected.stderr
            assert (app / ".deployment/state").read_text() == new_state
            assert api("/api/profile")["organization"] == "迁移后资料"
            assert (app / ".deployment/schema-floor").read_text().strip() == "2"
            assert list((app / "backups").glob("*.tar.gz"))
            print(f"PASS {action}: real 1→2 upgrade, explicit migration gate, historical backfill, credentials/key/app persistence and downgrade rejection", flush=True)

            # Simulate the exact journal state of a crash after schema migration
            # but before updating state. Recover must select the new image.
            (app / ".deployment/state").write_text(original_state)
            (app / ".deployment/pending").write_text(new_version + "\n")
            recovered = ctl("recover")
            assert "向前恢复" in recovered.stdout
            assert (app / ".deployment/state").read_text().splitlines()[0] == new_version
            assert not (app / ".deployment/pending").exists()
            assert api("/api/profile")["revision"] == 1
            assert api("/api/admin/analytics")["summary"]["logins_30d"] == 1
            result = run("docker", "compose", "-p", project, "--env-file", str(app / ".env"), "-f", str(app / "compose.yaml"), "up", "-d", "--no-build", "--pull", "never", "--force-recreate", "--wait", env={**env, "AUTH_IMAGE": new_meta[0]})
            assert result.returncode == 0, result.stderr
            assert api("/api/profile")["organization"] == "迁移后资料"
            assert api("/api/admin/analytics")["summary"]["logins_30d"] == 1
            assert original_key == key_hash(new_meta[0])
            print("PASS interrupted migration forward recovery, container recreation, profile/activity persistence and repeat migration safety", flush=True)
        finally:
            # This random project/volume belongs only to this test invocation.
            run("docker", "compose", "-p", project, "--env-file", str(app / ".env"), "-f", str(app / "compose.yaml"), "down", "-v")
            for image in reversed(images):
                run("docker", "image", "rm", image)


if __name__ == "__main__":
    scenario()
    scenario(adopt=True)
