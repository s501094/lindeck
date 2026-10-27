# lindeck

A floating-window TUI package manager for apt, in the style of plugdeck.nvim.

You can browse and search the whole apt archive, health-scan what's installed, and install, remove or upgrade packages without typing `sudo`. When an action needs root, lindeck shows its own masked password prompt, then runs apt in the background and streams the output into the window.

```
╭─────────────────────────────── lindeck ────────────────────────────────╮
│  [1:Installed]  2:Scan   3:apt    source: apt                          │
│  a:add  d:remove  m:manual/all  r:refresh  <CR>:details  o:homepage    │
│────────────────────────────────────────────────────────────────────────│
│  ◉ btop                manual     1.3.0-1            Modern and colo…  │
│  ◉ build-essential     manual     12.10ubuntu1       Informational l…  │
│  ✗ broken-pkg          ERROR      2.0-1              half-configured   │
╰────────────────────────────────────────────────────────────────────────╯
```

## Contents

- [Features](#features)
- [Prerequisites](#prerequisites)
- [Installation](#installation)
- [Usage](#usage)
- [Keybindings](#keybindings)
- [How sudo is handled](#how-sudo-is-handled)
- [Project structure](#project-structure)
- [How it works](#how-it-works)
- [Troubleshooting](#troubleshooting)
- [Roadmap](#roadmap)
- [Contributing](#contributing)

## Features

lindeck has three tabs. Switch between them with `1`, `2` and `3`.

### 1: Installed
- Lists the packages you installed yourself (`◉ manual`). Press `m` to also show packages that came in as dependencies (`◍ auto`).
- Packages stuck in a broken dpkg state (half-configured, unpacked, etc.) show in red as `✗ ERROR`.
- Each row shows the name, status, version and short description.
- `a` installs a package by name, `d` removes the selected package, `<CR>` shows full details.

### 2: Scan
A health check of your system, with a summary line: `N ok   N warnings   N errors`.

| Icon | Meaning |
|---|---|
| `●` ok | Manually installed and healthy |
| `▲` warning | Upgrade available, held back with `apt-mark hold`, or an orphaned dependency that `autoremove` would clean up |
| `✗` error | Broken dpkg state, plus a hint to run `sudo dpkg --configure -a` |

Errors are listed first, then warnings, then everything that's fine. From this tab you can upgrade one package (`u`), upgrade everything (`U`), run autoremove (`A`), or refresh the package lists with `apt update` (`R`).

### 3: apt (browser and search)
- Opens on a list of **apt sections** (admin, devel, editors, games, utils, …) with a package count for each, like plugdeck's neovimcraft categories.
- `<CR>` opens a section. `<BS>`, `t` or `-` goes back to the sections.
- `/` searches package names and short descriptions. Exact name matches come first, then names starting with the query, then names containing it, then description matches.
- Each result is two lines: a `✓` if it's installed, then the name, version and section, with the description dimmed underneath.
- `i` installs, `d` removes, `o` opens the project homepage, and `<CR>` shows full `apt-cache show` details.
- Each view remembers where your cursor was, so backing out of a section puts you back on that section.

### Everywhere
- Root actions open a masked password prompt and stream apt's output into a log pane at the bottom of the window.
- Colors are solarized, to sit alongside solarized-osaka in Neovim.
- There's also a plain command-line mode for scripts and quick lookups (see [Usage](#usage)).

## Prerequisites

| Requirement | Notes |
|---|---|
| **An apt-based Linux distro** | Debian, Ubuntu, Pop!_OS, Mint, WSL2 Ubuntu, … lindeck calls `apt-get`, `apt-cache`, `apt-mark` and `dpkg-query`. |
| **sudo** | Your user must be in the sudoers file (on Ubuntu, the `sudo` group). |
| **Go 1.22 or newer** | Only needed to build. The result is a single binary with no runtime dependencies. |
| **A terminal with Unicode and 256 or truecolor** | For the box drawing, icons and colors. Most modern terminals (kitty, WezTerm, Alacritty, Windows Terminal, GNOME Terminal) work. |
| **`xdg-open` or `wslview`** *(optional)* | Used by `o` to open homepages. On WSL, install `wslu` to get `wslview`. |

Check what you have:

```sh
go version          # go1.22 or newer
apt-get --version
sudo -v             # confirms you have sudo rights
```

## Installation

### With `go install`

```sh
go install github.com/s501094/lindeck@latest
```

This puts the binary in `$(go env GOPATH)/bin`, which is usually `~/go/bin`. Make sure that directory is on your `PATH`:

```sh
# bash / zsh
export PATH="$HOME/go/bin:$PATH"

# fish
fish_add_path ~/go/bin
```

### From source

```sh
git clone https://github.com/s501094/lindeck.git
cd lindeck
go build -o ~/.local/bin/lindeck .
```

`~/.local/bin` is on the `PATH` by default on most distros.

### Uninstall

```sh
rm "$(command -v lindeck)"
```

lindeck doesn't write config files or caches anywhere, so removing the binary removes everything.

## Usage

### TUI

```sh
lindeck              # open on the Installed tab
lindeck ripgrep      # open on the apt tab, already searched for "ripgrep"
```

A typical install:

1. Run `lindeck`, press `/`, type `htop`, and press `<CR>`.
2. Move to `htop` with `j`/`k` and press `i`.
3. Type your sudo password at the `sudo password: ••••` prompt and press `<CR>`.
4. apt's output scrolls in the log pane. When it finishes, the status line shows `✓ install htop done` and the `✓` marker appears next to the package.

### Command line

```sh
lindeck search ripgrep        # print matches; ✓ marks installed packages
lindeck install htop btop     # install one or more packages
lindeck remove htop           # remove packages
lindeck help                  # usage
```

The `install` and `remove` subcommands run apt directly in your terminal. sudo asks for your password as usual, and apt asks for confirmation before making changes.

## Keybindings

### Global

| Key | Action |
|---|---|
| `1` `2` `3` | Switch to the Installed / Scan / apt tab |
| `j` / `k` (or arrow keys) | Move down / up |
| `g` / `G` | Jump to the top / bottom |
| `<C-d>` / `<C-u>` | Half page down / up |
| `/` | Search apt |
| `<CR>` | Details (or open a section) |
| `o` | Open the package homepage |
| `c` | Clear the output log and status line |
| `q` / `<Esc>` | Quit |
| `<C-c>` | Quit from anywhere |

### Installed tab

| Key | Action |
|---|---|
| `a` | Install a package by name |
| `d` | Remove the selected package |
| `m` | Toggle between your packages only and all packages, including dependencies |
| `r` | Reload the installed list |

### Scan tab

| Key | Action |
|---|---|
| `s` / `r` | Rescan |
| `u` | Upgrade the selected package |
| `U` | Upgrade everything (`apt-get upgrade`) |
| `A` | Remove orphaned dependencies (`apt-get autoremove`) |
| `R` | Refresh package lists (`apt-get update`), then reload |

### apt tab

| Key | Action |
|---|---|
| `t` / `-` | Show sections |
| `<BS>` | Go back to sections, or forward to the last results |
| `i` | Install the selected package |
| `d` | Remove the selected package |
| `r` | Reload the apt index |

### Prompts

| Key | Action |
|---|---|
| `<CR>` | Submit |
| `<Esc>` | Cancel |

## How sudo is handled

lindeck runs as your normal user. It never runs as root and never needs to be started with `sudo`. Only the apt command runs as root.

1. When you start an action that needs root, lindeck runs `sudo -n true` to check whether sudo already has your credentials cached. If it does, the action starts right away.
2. If not, lindeck shows a masked `sudo password:` prompt.
3. It checks the password with `sudo -S -k -v` before doing anything. A wrong password shows `incorrect password — try again`, and nothing runs.
4. It then runs the apt command with `sudo -S`, passing the password on sudo's standard input, with `DEBIAN_FRONTEND=noninteractive` so apt never stops to ask a question that the TUI can't answer.
5. Output is streamed line by line into the log pane.

The password lives in memory only for that one operation. It is never written to disk, logged, or kept for the next action. sudo's own rules apply as usual: failed attempts are logged, and lockout policies such as `faillock` still count.

Each action runs as one specific apt command, which you can see as the first line of the log pane:

| Action | Command run as root |
|---|---|
| Install | `apt-get install -y -o Dpkg::Progress-Fancy=0 -- <pkg>` |
| Remove | `apt-get remove -y -o Dpkg::Progress-Fancy=0 -- <pkg>` |
| Upgrade one | `apt-get install -y ... --only-upgrade -- <pkg>` |
| Upgrade all | `apt-get upgrade -y ...` |
| Autoremove | `apt-get autoremove -y ...` |
| Refresh lists | `apt-get update` |

## Project structure

```
lindeck/
├── main.go      Entry point: decides between the TUI and the search / install / remove commands
├── tui.go       The Bubble Tea interface: tabs, rendering, keys, prompts, the log pane
├── index.go     Loads the full apt package index into memory; search and sections
├── apt.go       Installed packages, the health scan, package details, opening homepages
├── sudo.go      Password check and running commands as root with streamed output
├── go.mod       Module definition and dependencies
└── go.sum
```

| File | What's in it |
|---|---|
| `main.go` | Parses arguments. With no arguments, or with a search term, it opens the TUI. `search`, `install`, `remove` and `help` run without the TUI. |
| `tui.go` | The `model` holds all UI state: current tab, installed list, apt index, scan results, cursor memory per view, the active prompt, the running job and its output. `Update` handles keys and background results, and `View` draws the window. Rows are built only for the items on screen, so a search with tens of thousands of results stays fast. |
| `index.go` | `loadIndex` parses `apt-cache dumpavail` (about 88k packages) into a map and a sorted list, and counts packages per section. `"universe/devel"` and `"devel"` are merged into one section. `Search` ranks matches, and `InSection` lists a section. |
| `apt.go` | `loadInstalled` combines `dpkg-query` (versions, descriptions, dpkg state) with `apt-mark showmanual`. `runScan` builds the health report from dpkg states, `apt list --upgradable`, `apt-mark showhold` and a dry run of `apt-get -s autoremove`. `pkgDetails` formats `apt-cache show` output. `openURL` uses `wslview` or `xdg-open`. |
| `sudo.go` | `sudoCached`, `sudoValidate`, `sudoStream` (runs a root command in the background and sends each output line back to the UI) and `sudoInteractive` (used by the command-line mode). |

### Dependencies

- [Bubble Tea](https://github.com/charmbracelet/bubbletea): the TUI framework
- [Bubbles](https://github.com/charmbracelet/bubbles): the text input used for search and password prompts
- [Lip Gloss](https://github.com/charmbracelet/lipgloss): colors and layout

## How it works

```
             ┌──────────────── tui.go (Bubble Tea) ────────────────┐
  keys ────▶ │  Update ──▶ model state ──▶ View (floating window)  │
             └──────┬──────────────┬──────────────────┬────────────┘
                    │ background   │ background       │ background
                    ▼              ▼                  ▼
              index.go         apt.go             sudo.go
         apt-cache dumpavail   dpkg-query         sudo -S apt-get …
                               apt-mark           (output streamed
                               apt list            back line by line)
                               apt-cache show
```

- Everything slow runs in the background: loading the index, reading the installed list, the scan, details, and root commands. Each one reports back to `Update` when it finishes, so the window never freezes.
- The apt index is loaded once at startup (about a second) and kept in memory, so searching and browsing sections are instant. It reloads after `R` (apt update) or `r` on the apt tab.
- When a root action finishes, the installed list reloads and the scan runs again, so the `✓` markers and the health report stay current.

## Troubleshooting

| Problem | Fix |
|---|---|
| `lindeck: command not found` | The install directory isn't on your `PATH`. See [Installation](#installation). |
| Search finds nothing, or packages seem missing | Your package lists are out of date. Run `R` on the Scan tab (or `sudo apt update`), then press `r` on the apt tab. |
| `Could not get lock /var/lib/dpkg/lock-frontend` in the log | Another apt process is running, for example unattended-upgrades or another terminal. Wait for it to finish and try again. |
| `incorrect password` but the password is right | Your account may be locked out after earlier failed attempts (`faillock`), or your user isn't in sudoers. Run `sudo -v` in a normal terminal to see the real error. |
| `o` doesn't open anything on WSL | Install `wslu` (`sudo apt install wslu`) so `wslview` is available. |
| Boxes, icons or colors look wrong | Use a terminal and font with Unicode support, and make sure `TERM` advertises 256 or truecolor, for example `xterm-256color`. |
| A package shows `✗ ERROR` | An install was interrupted. Run `sudo dpkg --configure -a`, then rescan with `s`. |

## Roadmap

- **No-sudo sources:** install GitHub release binaries and AppImages into `~/.local/bin`, plus Flatpak `--user` installs, all tracked in lindeck
- **More backends:** dnf and pacman, detected automatically like plugdeck detects the plugin manager
- **Lockfile and sync:** save your installed set to a file and rebuild it on another machine with `lindeck sync`
- **Configuration:** window size, colors and icons

## Contributing

Issues and pull requests are welcome. Before opening a PR:

```sh
gofmt -l .        # should print nothing
go vet ./...
go build ./...
```

Keep UI changes consistent with the existing layout: tab line, key-hint line, divider, then the list.
