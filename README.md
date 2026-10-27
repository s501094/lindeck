# lindeck

A floating-window TUI package manager for apt, in the style of plugdeck.nvim. Browse and search the archive, health-scan what's installed, and install or remove packages without typing `sudo` — lindeck prompts for your password and runs apt in the background.

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

## Features

- **Installed** — packages you installed by hand (`◉`); press `m` to include dependencies (`◍`). Broken dpkg states show as `✗ ERROR`. `a` installs a package by name, `d` removes, `<CR>` shows details.
- **Scan** — health check with `● ok / ▲ warning / ✗ error`: broken installs, pending upgrades, held packages, and orphaned dependencies. `u` upgrades one, `U` upgrades all, `A` autoremoves, `R` runs `apt update`.
- **apt browser** — every apt section with package counts (like neovimcraft categories). `<CR>` opens a section, `<BS>` goes back, `/` searches names and descriptions. `i` installs, `d` removes, `o` opens the homepage, `✓` marks what's installed. Cursor position is remembered per view.
- **sudo handled for you** — root actions open a masked `sudo password:` prompt (skipped if sudo's credentials are cached), validate it, then stream apt's output into a log pane. The password is handed to sudo and never stored.

## Install

Requires Go ≥ 1.22 and an apt-based distro (Debian, Ubuntu, …).

```sh
go install github.com/s501094/lindeck@latest
```

## Usage

```sh
lindeck                  # open the TUI
lindeck ripgrep          # open the TUI with a search
lindeck search ripgrep   # print matches (✓ = installed)
lindeck install htop     # install (sudo prompts as usual)
lindeck remove htop
```

## Keys

| Key | Action |
|---|---|
| `1` `2` `3` | Installed / Scan / apt tabs |
| `j` `k` `g` `G` `<C-d>` `<C-u>` | move |
| `/` | search apt |
| `t` `-` `<BS>` | sections / back |
| `<CR>` | details or open section |
| `i` `d` | install / remove |
| `o` | open homepage |
| `c` | clear the output log |
| `q` `<Esc>` | quit |

## Roadmap

- No-sudo sources: GitHub release binaries and AppImages into `~/.local/bin`, Flatpak `--user`
- dnf / pacman backends
