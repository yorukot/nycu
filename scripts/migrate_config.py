#!/usr/bin/env python3
"""Move default token files to standalone storage, without reading them into logs."""
import json
import os
from pathlib import Path
import sys
import time

sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "lib"))
from _paths import config_path


def migrate(env=None):
    env = os.environ if env is None else env
    root = Path(env.get("XDG_CONFIG_HOME") or "~/.config").expanduser()
    results = []
    for service, old_name, overrides in [
        ("portal", "nycu", ("NYCU_PORTAL_CONFIG", "UTILS_NYCU_CONFIG")),
        ("e3p", "e3p", ("NYCU_E3P_CONFIG", "UTILS_E3P_CONFIG")),
    ]:
        if any(env.get(key) for key in overrides):
            results.append({"service": service, "status": "explicit path retained"})
            continue
        source = root / "utils" / (old_name + ".json")
        target = config_path(service, env)
        if target.exists() or not source.exists():
            results.append({"service": service, "status": "already configured" if target.exists() else "no legacy file"})
            continue
        data = source.read_bytes()
        parsed = json.loads(data)
        if not isinstance(parsed, dict) or not isinstance(parsed.get("token"), str) or not parsed["token"]:
            raise ValueError(f"{service}: legacy file does not contain a token")
        target.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        backup_root = root / "nycu" / "migration-backups"
        backup_root.mkdir(parents=True, exist_ok=True, mode=0o700)
        backup = backup_root / (old_name + "." + str(time.time_ns()) + ".json")
        # Exclusive creation never overwrites another configuration. Restrict mode
        # before writing any token bytes, including backups.
        for path in [backup, target]:
            fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
            with os.fdopen(fd, "wb") as file:
                file.write(data)
        source.unlink()
        results.append({"service": service, "status": "migrated"})
    return results


if __name__ == "__main__":
    print(json.dumps(migrate()))
