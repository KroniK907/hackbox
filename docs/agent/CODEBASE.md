# Hackbox codebase

v1 layout and coding standards. Binding. Source: [CORE-HOST-GM-001 through GM-005](https://github.com/KroniK907/hackbox/issues/1) from [Grill: code layout and standards](https://github.com/KroniK907/hackbox/issues/4).

Read this before adding Go packages, templates, or static files. Operator and player docs belong in `docs/guide/`, not here.

## Jump list

- [Directory tree](#directory-tree)
- [Import rules](#import-rules)
- [Package names](#package-names)
- [Embed and CSS](#embed-and-css)
- [Comments](#comments)
- [Tests](#tests)
- [gofmt](#gofmt)
- [Changing this file](#changing-this-file)

## Directory tree

One Go module. No `pkg/`. No second module for shared code. Directories exist in git only when they contain a real file. Do not add `.gitkeep` placeholders.

```text
hackbox/
  go.mod
  AGENTS.md
  cmd/
    hackbox/
      main.go                 # process entry; almost no logic
  docs/
    agent/
      CODEBASE.md             # this file
    guide/                    # operator and player manual, later
  internal/
    host/                     # process, mux, register Lobby + games
    lobby/
      templates/
    ui/
      static/                 # host chrome CSS/JS (theme tokens + widgets)
      templates/              # shared chrome defines (ui-start, overlay)
    store/                    # host/Lobby persistence, game_kv, path helpers
    platform/                 # host-wide packages
      hub/                    # in-process named SSE broadcaster
      applog/                 # in-memory log ring + optional host.log
    games/                    # compile-time loader + Game/Helper contract
      game.go
      testing/                # Testing diagnostics; package testinggame
        templates/
        static/
  web/                        # host Tailwind source only; not embedded
```

`internal/ui` is one package in v1. Lobby vs game chrome can be files or subfolders inside it. Split into separate packages later if a second game or an external author API makes the cut obvious.

Shared widgets are CSS classes in `internal/ui/static/live.css` (`ui-btn`, `ui-field`, `ui-header`, `ui-board`, and the rest) plus `internal/ui/templates/chrome.html` (`ui-start`, `ui-start-quiet`, `ui-overlay`). Palettes are `html[data-theme]` token sets. v1 ships `neon-light` (default) and `neon-dark`. Pages pass `ui.Chrome`. Avatars and join QR are `ui.AvatarSVG` and `ui.QRCodeSVG`. Lobby page templates compose those widgets. They do not restyle each screen from scratch.

A later `cmd/hackbox-dev` would also import only `internal/host`. Tray details are [Research: Windows Go launch and tray](https://github.com/KroniK907/hackbox/issues/5). Host files on disk are [Research: host on-disk store](https://github.com/KroniK907/hackbox/issues/6). The game contract shape is [Grill: Lobby vs game package](https://github.com/KroniK907/hackbox/issues/9).

## Import rules

`cmd/hackbox` imports only `internal/host`. Host is the composition root. It may import lobby, games, ui, store, and platform.

| From | May import | Must not import |
|------|------------|-----------------|
| `cmd/hackbox` | `internal/host` | everything else |
| `internal/host` | lobby, games, ui, store, platform | - |
| `internal/lobby` | ui, platform, store | host, games |
| `internal/games` and `internal/games/<name>` | ui, platform, sibling-free game code | host, lobby, store, other games |
| `internal/ui` | platform | host, lobby, games, store |
| `internal/store` | (stdlib / SQLite only) | host, lobby, games, ui |
| `internal/platform/<name>` | other platform packages if needed | host, lobby, games, ui |

Games never open host/Lobby storage. Player and Lobby details reach a game through host. Host injects a namespaced data dir or handle. Opaque game KV is the `game_kv` table in `host.sqlite`, namespaced by game id. Host does not parse values. Each game may keep its own persistence code under `internal/games/<name>`.

`internal/games` owns compile-time loading. It imports child packages such as `internal/games/testing` and exposes the list plus start/stop to host. No runtime folder scan in v1. External or binary games are a later expansion.

## Package names

The `package` clause matches the last path element (`host`, `lobby`, `ui`, `store`, `games`, and `platform/<name>` as the folder name).

The only planned exception is `internal/games/testing`. Product name is Testing. Package name is `testinggame`. Do not use `package testing` (stdlib clash). Do not use `package diag` (likely clash with a later `internal/platform/diag`).

## Embed and CSS

HTML lives in `templates/` next to the embedding package. Compiled CSS/JS/images live in `static/`. Filenames are lowercase (`board.html`, `game.css`).

`//go:embed` patterns are relative to the package directory. They cannot reach `web/` from `internal/`. Shared chrome and compiled host CSS belong in `internal/ui`. A game's shipped assets (templates, prompts, art, metadata, CSS) live under `internal/games/<name>` and that package embeds them.

Host Tailwind input is `web/`. Its compiled output is `internal/ui/static/`. Each game that needs custom utilities compiles CSS in its own tree and checks in the result under that game's `static/`. The running host never runs Tailwind. A per-game `web/` folder exists only if that game actually compiles Tailwind there.

Commit the files `embed` reads. Gitignore `node_modules` and any Tailwind cache next to `web/` (`.turbo/` is listed as a start). Runtime operator data (SQLite, saves) is not in this repo.

## Comments

Godoc on every exported type, func, and const. Package comment on each package (`// Package lobby ...`). Extra comments only when unexported logic is non-obvious. No line-by-line narration. Update the comment in the same change as the behavior.

## Tests

`_test.go` next to the code it covers. No top-level `tests/` tree. Prefer `package foo_test` unless the test must see unexported details. Focused unit and integration tests. No broad smoke suites. Automated tray UI tests are out of scope. CI is not a v1 gate.

## gofmt

All Go files must be `gofmt`'d. Extra linters are optional locally. They are not required until CI exists.

## Changing this file

Fitting additions already have a home. A new `internal/platform/notice` when the first importer exists, or a new `internal/games/<name>`, does not need a new layout decision. Changing the homes (split `ui`, publish a public game API, let games import Lobby) should update this file in the same change. This file is the source of truth even if the planning process that wrote it is gone.
