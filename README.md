# WinMac

**Play Windows games on an Apple Silicon Mac, from the command line.**

I wanted to play games like Elden Ring and Spider-Man 2 on my MacBook using only
free, open tools. The pieces needed to do this already exist (Wine, Apple's
Game Porting Toolkit, Rosetta 2), but putting them together by hand is painful:
environment variables, prefixes, launchers that crash, logs that are hard to
read. WinMac is the tool that glues them together for me. You use it
through a single command, `cave`.

> **Status: early and experimental.** The command-line core works and is tested,
> but the tests run against a fake Wine. Running real games is still a work in
> progress; see [What doesn't work (yet)](#what-doesnt-work-yet).

## How it works

WinMac doesn't translate anything itself. It drives three existing layers:

| Layer | What it does |
|---|---|
| Rosetta 2 | Runs Intel (x86_64) code on Apple Silicon |
| Wine | Turns Windows system calls into macOS ones |
| D3DMetal (from Apple's GPTK) | Turns DirectX 11/12 graphics into Metal |

On top of that, WinMac gives you:

- **One isolated Windows environment (prefix) per game or launcher**, so one
  game's settings never break another.
- **A profile per game**: graphics backend, AVX, Metal HUD, extra variables.
- **The Windows version of Steam**, installed and launched for you. The Mac
  version of Steam can't install Windows-only games.
- **A log and a JSON summary for every run**, so when something crashes you can
  see exactly what was launched and how it ended.

## Getting a Wine engine

WinMac needs a Wine build to run games with. There are two options:

| Engine | Wine version | D3DMetal | Verdict |
|---|---|---|---|
| Apple's Game Porting Toolkit (GPTK) | 7.7, from 2022 | Included | Too old for today's 64-bit Steam client, which crashes on start |
| **Wine built from CrossOver's sources** | 11 (CrossOver 26) | Copied from your GPTK | **Recommended** |

CodeWeavers, the company behind CrossOver, publishes the Wine it ships under an
open-source licence (LGPL). This repository builds it for you:
[`engine/build.sh`](engine/build.sh) runs on a GitHub Actions macOS machine
(**Actions → Build engine**, about two hours the first time) and can publish
the result as a release, together with the exact sources it was built from.

D3DMetal belongs to Apple and is never shared by this project. Instead, WinMac
copies it from the Game Porting Toolkit installed on your own Mac:

```sh
brew install --cask gcenx/wine/game-porting-toolkit   # gives you D3DMetal
cave engine detect --register                          # registers the GPTK
cave engine install <URL or path of the release .tar.xz>
cave engine import-d3dmetal cx26.3.0-1                 # becomes your default engine
cave doctor                                            # checks that everything is in place
```

The build scripts are adapted from
[conversio-wine](https://github.com/theoron-apps/conversio-wine) (MIT licence);
see [`engine/NOTICE`](engine/NOTICE).

## Getting started

You need an Apple Silicon Mac with Rosetta 2 (`softwareupdate --install-rosetta`)
and Go 1.22 or later.

```sh
go generate ./internal/steam   # builds the small Windows helper embedded in cave (see below)
go build -o bin/cave ./cmd/cave
go test ./...
```

Then set up Steam and add a game:

```sh
cave prefix create steam                 # a fresh Windows environment
cave steam install steam                 # installs the Windows Steam client
cave steam run steam                     # opens Steam: sign in and install your games

cave steam add 2651280 --name spiderman2 --prefix steam --avx --hud
cave run spiderman2
```

A few commands that help when things go wrong:

```sh
cave run spiderman2 --dry-run            # show the exact command and environment, run nothing
cave run spiderman2 --debug +seh,+loaddll  # ask Wine for more detail
cave logs spiderman2                     # past runs with their exit codes
cave logs spiderman2 --tail 80           # end of the latest log
cave logs steam --prefix steam --tail 80 # Steam's own log
cave kill steam                          # stop everything running in a prefix
```

Installers and games that don't come from Steam work too:

```sh
cave exec mygames ~/Downloads/setup.exe
cave add mygame --prefix mygames --exe 'C:\Games\MyGame\game.exe' -- -windowed
```

## Game profiles

| Option | What it does |
|---|---|
| `--backend d3dmetal` | Apple's DirectX → Metal translation. The default when the engine has it |
| `--backend dxvk` | DirectX → Vulkan → Metal. You need to put the DXVK DLLs in the prefix |
| `--backend wined3d` | Wine's own DirectX → OpenGL, for older games |
| `--avx` | Tells games the CPU supports AVX. Some games refuse to start without it |
| `--hud` | Shows Apple's Metal performance overlay (FPS, GPU time) |
| `--env KEY=VALUE` | Any extra variable. Your values always win over WinMac's defaults |

## Where things are stored

Everything lives in `~/Library/Application Support/Cave` (or in `$CAVE_HOME`
if you set it):

```
cave.json                 your engines, prefixes and games
engines/                  installed Wine builds
prefixes/<name>/          one Windows environment each
cache/                    downloaded installers
logs/<game>/
  20261110-175318.log     everything Wine printed, starting with the exact command
  20261110-175318.json    a short summary: exit code, duration, backend, engine...
```

The summary only records the variables WinMac set itself, never your whole
environment.

## Inside the code

```
cmd/cave          the command-line interface
internal/config   storage paths and the JSON database (written atomically)
internal/engine   finding, installing and inspecting Wine builds; importing D3DMetal
internal/prefix   creating prefixes, mapping Windows paths to Mac paths
internal/launch   turning a profile into a command, running it and recording the log
internal/steam    the Windows Steam client and its web helper fix
tools/cefwrapper  the Windows program that replaces steamwebhelper.exe
internal/doctor   system checks
engine/           building Wine from CrossOver's sources (script + CI workflow)
```

`launch.Build` is a pure function: it turns a profile into a command and its
environment without running anything, so it can be tested on any system.
`launch.Run` is tested with a small shell script pretending to be Wine.

## What doesn't work (yet)

- **Games with kernel-level anti-cheat, such as Fortnite, can't run.** That
  anti-cheat doesn't work under Wine on macOS, and WinMac doesn't try to get
  around it.
- **The CrossOver-based engine hasn't been tested on real games yet.** Its
  first build on GitHub Actions is still to come.
- **Some in-game videos won't play** with that engine for now, because it is
  built without GStreamer.
- **Steam's window can stay black the very first time.** Steam's interface is a
  Chromium browser (`steamwebhelper.exe`) whose GPU process can't draw under
  Wine on Apple Silicon. Before every Steam launch, WinMac swaps in a tiny
  Windows program ([`tools/cefwrapper`](tools/cefwrapper)) that starts the real
  helper with `--disable-gpu --single-process`. Steam only downloads that helper
  on its first start, so if the window is black, quit Steam and launch it again.
- `cave run` waits until everything in the prefix has closed, because Steam
  restarts itself after updates and games run as children of Steam. Use
  `--no-wait` to get your terminal back straight away.
- Performance depends on the game, the engine and the Mac.

## Roadmap

1. ✅ Command-line core: engines, prefixes, profiles, logged runs
2. 🚧 A modern engine built from CrossOver's sources, and a working Steam
3. Epic Games support, through the open-source Legendary client
4. A desktop app: game library, profiles, engine updates
5. A crash-diagnosis assistant that reads the logs, finds known fixes and
   suggests a profile change for you to approve

## Legal

WinMac is not affiliated with CodeWeavers, Apple or Valve. CrossOver is a
trademark of CodeWeavers. You need to own the games you run.
