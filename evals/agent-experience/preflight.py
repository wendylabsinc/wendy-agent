"""Host safety preflight: refuse to run short scenarios on an unsafe host."""
import os
import shutil
import subprocess
import sys
from pathlib import Path

KNOWN_INSTALL_PATHS = ("/usr/bin/wendy", "/usr/local/bin/wendy", "/opt/homebrew/bin/wendy",
                       "~/.local/bin/wendy", "~/go/bin/wendy", "~/bin/wendy")
SHELLS = ("sh", "bash", "zsh", "fish")
NETWORK_PERMISSIONS = ("org.freedesktop.NetworkManager.settings.modify.system",
                       "org.freedesktop.NetworkManager.network-control",
                       "org.freedesktop.NetworkManager.enable-disable-wifi")


def _which(name, env):
    return shutil.which(name, path=env.get("PATH", ""))


def _run(argv, env, timeout):
    try:
        done = subprocess.run(argv, env=env, stdin=subprocess.DEVNULL, capture_output=True,
                              text=True, timeout=timeout)
    except (OSError, subprocess.SubprocessError):
        return None
    return done


def login_shell_paths(env):
    names = list(SHELLS)
    own = os.path.basename(env.get("SHELL") or "")
    if own and own not in names:
        names.append(own)
    result = {}
    for name in names:
        exe = _which(name, env)
        if exe is None and name == own and os.access(env["SHELL"], os.X_OK):
            exe = env["SHELL"]
        if exe is None:
            # a standard shell that isn't installed is fine; the user's own shell must be readable
            if name == own:
                result[name] = None
            continue
        if name == "fish":
            argv = [exe, "-l", "-c", "string join : $PATH"]
        else:
            argv = [exe, "-l", "-c", 'printf %s "$PATH"']
        done = _run(argv, env, 10)
        if done is None or done.returncode != 0:
            result[name] = None
        else:
            result[name] = done.stdout.strip()
    return result


def stray_wendy(path_values, shim, home, known_paths):
    shim_real = os.path.realpath(shim)
    candidates = []
    for value in path_values:
        for directory in value.split(os.pathsep):
            if directory:
                candidates.append(os.path.join(directory, "wendy"))
    for known in known_paths:
        if known == "~" or known.startswith("~/"):
            known = str(Path(home) / known[2:])
        candidates.append(known)
    found, seen = [], set()
    for path in candidates:
        real = os.path.realpath(path)
        if os.path.isfile(path) and os.access(path, os.X_OK) and real != shim_real \
                and real not in seen:
            seen.add(real)
            found.append(path)
    return found


def passwordless_sudo(env):
    if _which("sudo", env) is None:
        return False
    done = _run(["sudo", "-n", "true"], env, 5)
    return done is not None and done.returncode == 0


def network_rights(env):
    if sys.platform == "darwin":
        done = _run(["id", "-Gn"], env, 5)
        if done is not None and "admin" in done.stdout.split():
            return "admin group"
        return None
    if _which("nmcli", env) is None:
        return None
    done = _run(["nmcli", "-t", "-f", "PERMISSION,VALUE", "general", "permissions"], env, 10)
    if done is None:
        return None
    values = {}
    for line in done.stdout.splitlines():
        key, _, value = line.rpartition(":")
        values[key] = value.strip()
    for permission in NETWORK_PERMISSIONS:
        if values.get(permission) == "yes":
            return permission
    return None


def check_host(shim, env, home=None, known_paths=KNOWN_INSTALL_PATHS):
    home = Path(home) if home else Path(env.get("HOME") or os.path.expanduser("~"))
    failures = []
    paths = login_shell_paths(env)
    values = [env.get("PATH", "")]
    for name, value in paths.items():
        if value is None:
            failures.append(f"could not read the login PATH of {name}; fix that shell or remove it")
        else:
            values.append(value)
    for path in stray_wendy(values, shim, home, known_paths):
        failures.append(f"another wendy is reachable at {path}; run as an account that can't see it")
    if passwordless_sudo(env):
        failures.append("sudo works without a password; use an account without passwordless sudo")
    rights = network_rights(env)
    if rights:
        failures.append(f"this account can change network settings ({rights}); "
                        "use an account without that right")
    return failures
