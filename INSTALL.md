# Package-Specific Installation

For most users, the recommended installation method is the install script documented in [README.md](README.md). The instructions below are for users who prefer to install via a system package manager.

## CLI

### macOS (Homebrew)

On Homebrew versions that support formula trust:

```sh
brew trust wendylabsinc/tap
brew trust --formula wendylabsinc/tap/wendy
brew install wendylabsinc/tap/wendy
```

On older Homebrew versions where `brew trust` is unavailable:

```sh
brew tap wendylabsinc/tap
brew install wendy
```

For the nightly (prerelease) version:

On Homebrew versions that support formula trust:

```sh
brew trust wendylabsinc/tap
brew trust --formula wendylabsinc/tap/wendy-nightly
brew install wendylabsinc/tap/wendy-nightly
```

On older Homebrew versions where `brew trust` is unavailable:

```sh
brew tap wendylabsinc/tap
brew install wendy-nightly
```

To update:

```sh
brew update && brew install wendy
```

If the tap is untrusted after a Homebrew update:

```sh
brew trust wendylabsinc/tap && brew install wendy
```

### Linux

Debian/Ubuntu (`.deb`):

```sh
sudo apt install ./wendy_<version>_<arch>.deb
```

Fedora/RHEL (`.rpm`):

```sh
sudo dnf install ./wendy-<version>.<arch>.rpm
```

Arch Linux (AUR):

```sh
yay -S wendy
```

### Windows (Winget)

```powershell
winget install WendyLabs.Wendy
```

To update:

```powershell
winget upgrade WendyLabs.Wendy
```

## Agent

### Linux

Debian/Ubuntu (`.deb`):

```sh
sudo apt install ./wendy-agent_<version>_<arch>.deb
```

Fedora/RHEL (`.rpm`):

```sh
sudo dnf install ./wendy-agent-<version>.<arch>.rpm
```

Arch Linux (AUR):

```sh
yay -S wendy-agent
```

## Pre-built Binaries

Pre-built CLI binaries for Linux, macOS, and Windows, and agent binaries for Linux, are available on the [Releases](https://github.com/wendylabsinc/wendy-agent/releases) page.
