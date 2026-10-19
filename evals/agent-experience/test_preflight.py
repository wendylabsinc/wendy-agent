import os
import sys
import tempfile
import unittest
from pathlib import Path

import eval_wendy
import preflight


def fake(directory, name, body):
    path = Path(directory) / name
    path.write_text("#!/bin/sh\n" + body + "\n")
    path.chmod(0o755)
    return path


class PreflightTest(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.tmp = Path(tmp.name).resolve()
        self.home = self.tmp / "home"
        self.home.mkdir()
        self.shimdir = self.tmp / "shim"
        self.bin = self.tmp / "bin"
        self.stray = self.tmp / "stray"
        for d in (self.shimdir, self.bin, self.stray):
            d.mkdir()
        self.shim = eval_wendy.write_shim(self.shimdir)
        # a fake zsh that reports the PATH it was given, standing in for a login shell
        fake(self.bin, "zsh", 'printf %s "$PATH"')

    def env(self, *dirs, **extra):
        path = os.pathsep.join(str(d) for d in (self.shimdir, *dirs, self.bin))
        return {"PATH": path, "HOME": str(self.home), "SHELL": "/bin/zsh", **extra}

    def check(self, env, known=()):
        return preflight.check_host(self.shim, env, home=self.home, known_paths=known)

    def test_only_shim_passes(self):
        self.assertEqual(self.check(self.env()), [])

    def test_stray_wendy_on_path_fails(self):
        fake(self.stray, "wendy", "exit 0")
        msgs = self.check(self.env(self.stray))
        self.assertIn(f"another wendy is reachable at {self.stray / 'wendy'}; "
                      "run as an account that can't see it", msgs)

    def test_known_install_path_fails(self):
        known = self.home / ".local" / "bin"
        known.mkdir(parents=True)
        fake(known, "wendy", "exit 0")
        msgs = self.check(self.env(), known=("~/.local/bin/wendy",))
        self.assertIn(f"another wendy is reachable at {known / 'wendy'}; "
                      "run as an account that can't see it", msgs)

    def test_passwordless_sudo_fails(self):
        sudo = fake(self.stray, "sudo", "exit 0")
        msg = "sudo works without a password; use an account without passwordless sudo"
        self.assertIn(msg, self.check(self.env(self.stray)))
        sudo.write_text("#!/bin/sh\nexit 1\n")
        self.assertNotIn(msg, self.check(self.env(self.stray)))

    @unittest.skipIf(sys.platform == "darwin", "nmcli is Linux only")
    def test_nmcli_permission_yes_fails(self):
        perm = "org.freedesktop.NetworkManager.network-control"
        fake(self.stray, "nmcli", f"echo 'org.freedesktop.NetworkManager.settings.modify.system:no'\n"
                                  f"echo '{perm}:yes'")
        msgs = self.check(self.env(self.stray))
        self.assertIn(f"this account can change network settings ({perm}); "
                      "use an account without that right", msgs)

    def test_unreadable_login_shell_is_a_failure(self):
        env = self.env(SHELL="/nonexistent/zsh")
        env["PATH"] = str(self.shimdir)  # no zsh anywhere
        self.assertIn("could not read the login PATH of zsh; fix that shell or remove it",
                      self.check(env))


if __name__ == "__main__":
    unittest.main()
