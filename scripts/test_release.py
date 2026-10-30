#!/usr/bin/env python3
"""Opt-in Docker integration test. Uses only a temporary project, port and volume.

Usage: python3 scripts/test_release.py dist/auth-center-1.0.0-amd64.tar.gz
The two upgrade images reuse the binary and change release labels; a third
fixture deliberately fails health checks to exercise automatic recovery.
"""
import hashlib
import http.client
import http.cookiejar
import json
import os
from pathlib import Path
import re
import secrets
import shutil
import socket
import subprocess
import sys
import tarfile
import tempfile
import time
import urllib.error
import urllib.request


def run(*args, ok=True, **kwargs):
    result = subprocess.run(args, capture_output=True, text=True, **kwargs)
    if ok and result.returncode:
        # Test configuration uses only disposable credentials, but never print it.
        raise AssertionError(f"command failed: {args[0:2]}\n{result.stdout}\n{result.stderr}")
    return result


def main():
    bundle = Path(sys.argv[1]).resolve()
    project = "auth-release-test-" + secrets.token_hex(5)
    images = []
    with tempfile.TemporaryDirectory(prefix="auth-release-test-") as tmp:
        root = Path(tmp)
        with tarfile.open(bundle) as archive:
            assert not any(Path(name).name.startswith("._") for name in archive.getnames()), "release contains macOS metadata sidecars"
            assert not any(Path(name).name in {".env", "auth.db", "master.key"} for name in archive.getnames()), "release includes deployment secrets"
            archive.extractall(root, filter="data")
        app = root / "auth-center"
        base_version = (app / "RELEASE").read_text().strip()
        base_id = (app / "packages" / f"{base_version}.meta").read_text().splitlines()[0]
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            port = sock.getsockname()[1]
        password = secrets.token_urlsafe(24)
        (app / ".env").write_text(
            f"AUTH_PORT={port}\nAUTH_PUBLIC_URL=http://127.0.0.1:{port}\n"
            f"AUTH_ADMIN_EMAIL=release@example.test\nAUTH_ADMIN_PASSWORD='{password}'\n"
        )
        (app / ".env").chmod(0o600)
        env = {**os.environ, "AUTH_PROJECT_NAME": project}
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))

        def ctl(action, expected=0):
            result = run("sh", "./authctl.sh", action, cwd=app, env=env, ok=False)
            if expected == 0:
                assert result.returncode == 0, result.stdout + result.stderr
            else:
                assert result.returncode != 0, "failure was accepted"
            return result

        def api(path, body=None):
            headers = {"Connection": "close", "Origin": f"http://127.0.0.1:{port}"}
            data = None
            if body is not None:
                headers["Content-Type"] = "application/json"
                data = json.dumps(body).encode()
            req = urllib.request.Request(f"http://127.0.0.1:{port}{path}", data=data, headers=headers)
            # Docker Desktop's host port forwarding may lag the container health
            # probe. Retry only reads; never replay a mutating request.
            for attempt in range(15):
                try:
                    with opener.open(req, timeout=15) as response:
                        payload = response.read()
                        return json.loads(payload) if "application/json" in response.headers.get("Content-Type", "") else payload
                except (http.client.RemoteDisconnected, urllib.error.URLError):
                    if body is not None or attempt == 14:
                        raise
                    time.sleep(1)

        def state():
            return (app / ".deployment/state").read_text().splitlines()

        def fixture(version, broken=False):
            tag = f"{project}:{version}"
            platform = run("docker", "image", "inspect", "--format", "{{.Os}}/{{.Architecture}}", base_id).stdout.strip()
            # BuildKit resolves named local images reliably across image stores.
            base_tag = f"{project}:fixture-base"
            if base_tag not in images:
                run("docker", "tag", base_id, base_tag)
                images.append(base_tag)
            dockerfile = (
                f"FROM {base_tag}\nLABEL org.opencontainers.image.version={version}\n"
                + ("HEALTHCHECK --interval=1s --timeout=1s --start-period=0s --retries=1 CMD exit 1\n" if broken else "")
            )
            run("docker", "build", "--platform", platform, "-t", tag, "-", input=dockerfile)
            images.append(tag)
            image_id = run("docker", "image", "inspect", "--format", "{{.Id}}", tag).stdout.strip()
            package = app / "packages" / f"{version}.tar"
            run("docker", "save", "-o", str(package), tag)
            checksum = hashlib.sha256(package.read_bytes()).hexdigest()
            (app / "packages" / f"{version}.meta").write_text(f"{image_id}\n{checksum}\n1\n")
            (app / "RELEASE").write_text(version + "\n")
            return image_id

        try:
            ctl("install")
            assert api("/api/status")["version"] == base_version
            assert api("/healthz") == b""
            html = api("/").decode()
            asset = re.search(r'src="([^"]+\.js)"', html).group(1)
            assert len(api(asset)) > 1000
            api("/api/auth/login", {"email": "release@example.test", "password": password})
            api("/api/applications", {"name": "升级持久化测试", "description": "保留应用与主密钥"})
            saved_apps = api("/api/applications")
            key_hash = run("docker", "run", "--rm", "--network", "none", "--entrypoint", "sha256sum",
                           "-v", f"{project}_auth-data:/data:ro", base_id, "/data/master.key").stdout
            duplicate = run("docker", "run", "--rm", "--network", "none", "-v", f"{project}_auth-data:/data", base_id, ok=False)
            assert duplicate.returncode != 0 and "数据目录已被其他实例占用" in duplicate.stderr
            ctl("install", expected=1)
            ctl("update")
            assert state()[0] == base_version
            print("PASS install, version, assets, duplicate protection", flush=True)

            first = f"{base_version}-test.1"
            second = f"{base_version}-test.2"
            fixture(first)
            ctl("update")
            assert state()[0] == first and state()[2] == base_version
            fixture(second)
            ctl("update")
            assert state()[0] == second and state()[2] == first
            kept = {p.stem for p in (app / "packages").glob("*.tar")}
            assert kept == {first, second}, f"unexpected retained packages: {kept}, state: {state()}"
            ctl("rollback")
            assert state()[0] == first and state()[2] == second
            assert api("/api/applications") == saved_apps
            assert key_hash == run("docker", "run", "--rm", "--network", "none", "--entrypoint", "sha256sum",
                                   "-v", f"{project}_auth-data:/data:ro", base_id, "/data/master.key").stdout
            backups = list((app / "backups").glob("*.tar.gz"))
            assert len(backups) >= 3
            assert all(p.stat().st_mode & 0o077 == 0 for p in backups), "backup permissions expose account data"
            with tarfile.open(backups[0]) as backup:
                assert {"./auth.db", "./master.key"} <= set(backup.getnames())
            print("PASS update, two-package retention, rollback, data and backup", flush=True)

            old_state = state()
            fixture(f"{base_version}-test.bad", broken=True)
            ctl("update", expected=1)
            assert state() == old_state and not (app / ".deployment/pending").exists()
            assert api("/healthz") == b"" and api("/api/applications") == saved_apps
            package = app / "packages" / f"{base_version}-test.bad.tar"
            with package.open("ab") as out:
                out.write(b"corruption")
            ctl("update", expected=1)
            assert state() == old_state
            (app / ".deployment/pending").write_text("interrupted\n")
            ctl("recover")
            assert not (app / ".deployment/pending").exists()
            lock = app / ".deployment/operation.lock"
            lock.mkdir()
            ctl("update", expected=1)
            lock.rmdir()
            print("PASS failed health recovery, corrupt package rejection, journal recovery and operation lock", flush=True)

            # Adopt an existing deployment without deleting its container or data.
            shutil.move(app / ".deployment", app / ".prior-state")
            (app / "RELEASE").write_text(second + "\n")
            ctl("install", expected=1)
            ctl("adopt")
            assert state()[0] == second and state()[2] == "legacy"
            ctl("rollback")
            assert state()[0] == "legacy" and api("/api/applications") == saved_apps
            ctl("status")
            print("PASS adoption and rollback to original image", flush=True)
        finally:
            # The project name was randomly created above, never a user project.
            run("docker", "compose", "-p", project, "--env-file", str(app / ".env"),
                "-f", str(app / "compose.yaml"), "down", "-v", ok=False)
            for image in reversed(images):
                run("docker", "image", "rm", image, ok=False)


if __name__ == "__main__":
    main()
