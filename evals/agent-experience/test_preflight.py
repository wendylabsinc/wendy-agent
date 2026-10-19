import os
import shutil
import sys
import tempfile
import unittest
from unittest import mock
from pathlib import Path

import eval_wendy
import preflight


SLEEP = shutil.which("sleep")


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
        return {"PATH": path, "HOME": str(self.home), "SHELL": str(self.bin / "zsh"), **extra}

    def check(self, env, known=(), cwd=None):
        return preflight.check_host(self.shim, env, home=self.home, known_paths=known,
                                    cwd=cwd or self.tmp)

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

    def reach(self, path):
        return f"another wendy is reachable at {path}; run as an account that can't see it"

    def test_empty_path_element_is_cwd(self):
        cwd = self.tmp / "cwd"
        cwd.mkdir()
        fake(cwd, "wendy", "exit 0")
        env = self.env()
        env["PATH"] = ":" + env["PATH"]
        self.assertIn(self.reach(cwd / "wendy"), self.check(env, cwd=cwd))

    def test_nmcli_failure_is_a_failure(self):
        fake(self.stray, "nmcli", "exit 1")
        msgs = self.check(self.env(self.stray))
        self.assertTrue(any("nmcli general permissions failed" in m for m in msgs), msgs)

    def test_nmcli_without_permission_lines_is_a_failure(self):
        fake(self.stray, "nmcli", "exit 0")
        msgs = self.check(self.env(self.stray))
        self.assertTrue(any("nmcli general permissions failed" in m for m in msgs), msgs)

    def test_nmcli_hang_is_a_failure(self):
        fake(self.stray, "nmcli", f"exec {SLEEP} 30")
        with mock.patch.object(preflight, "NETWORK_TIMEOUT", 0.3):
            msgs = self.check(self.env(self.stray))
        self.assertTrue(any("nmcli general permissions failed" in m for m in msgs), msgs)

    def test_login_shell_path_differs_from_env(self):
        fake(self.stray, "wendy", "exit 0")
        fake(self.bin, "zsh", f'printf %s "{self.stray}:/usr/bin"')
        self.assertIn(self.reach(self.stray / "wendy"), self.check(self.env()))

    def test_fish_login_path_parsed(self):
        fake(self.stray, "wendy", "exit 0")
        fake(self.bin, "fish", f'echo "{self.stray}:/usr/bin"')
        env = self.env(SHELL=str(self.bin / "fish"))
        self.assertIn(self.reach(self.stray / "wendy"), self.check(env))
        paths = preflight.login_shell_paths(env)
        self.assertEqual(paths["fish"], f"{self.stray}:/usr/bin")

    def test_symlink_to_shim_ok_real_reported(self):
        link = self.stray / "wendy"
        link.symlink_to(self.shim)
        other = self.tmp / "other"
        other.mkdir()
        fake(other, "wendy", "exit 0")
        fake(self.bin, "zsh", f'printf %s "{self.stray}:{other}"')
        msgs = self.check(self.env())
        self.assertIn(self.reach(other / "wendy"), msgs)
        self.assertNotIn(self.reach(link), msgs)

    def test_non_executable_wendy_not_reported(self):
        path = fake(self.stray, "wendy", "exit 0")
        path.chmod(0o644)
        self.assertEqual(self.check(self.env(self.stray)), [])

    def test_hung_login_shell_is_unreadable(self):
        fake(self.bin, "zsh", f"exec {SLEEP} 30")
        with mock.patch.object(preflight, "SHELL_TIMEOUT", 0.3):
            msgs = self.check(self.env())
        self.assertIn("could not read the login PATH of zsh; fix that shell or remove it", msgs)

    def test_profile_banner_is_ignored(self):
        fake(self.bin, "zsh", "printf 'Welcome!\\n/a:/b'")
        self.assertEqual(preflight.login_shell_paths(self.env())["zsh"], "/a:/b")

    def test_own_shell_absolute_path_is_run(self):
        fake(self.stray, "wendy", "exit 0")
        outside = self.tmp / "outside"
        outside.mkdir()
        fake(outside, "zsh", f'printf %s "{self.stray}"')
        env = self.env(SHELL=str(outside / "zsh"))
        self.assertIn(self.reach(self.stray / "wendy"), self.check(env))


if __name__ == "__main__":
    unittest.main()
