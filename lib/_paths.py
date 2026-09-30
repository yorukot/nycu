"""Standalone private storage; legacy environment overrides remain supported."""
import os
from pathlib import Path


def setting(name, default=None, env=None):
    env = os.environ if env is None else env
    return env.get("NYCU_E3P_" + name, env.get("UTILS_E3P_" + name, default))


def config_path(service, env=None):
    env = os.environ if env is None else env
    if service == "portal":
        override = env.get("NYCU_PORTAL_CONFIG") or env.get("UTILS_NYCU_CONFIG")
    elif service == "e3p":
        override = setting("CONFIG", env=env)
    else:
        raise ValueError("Unknown NYCU service")
    if override:
        return Path(override).expanduser()
    root = Path(env.get("XDG_CONFIG_HOME") or "~/.config").expanduser()
    return root / "nycu" / (service + ".json")
