# Contributing

Thanks for helping! Bug reports, new agent CLI support and screen fixtures are
the most valuable contributions.

## Develop

Requirements: Go (see `go.mod`), tmux 3.3+.

```sh
git clone https://github.com/kashym/tmux-agent-herd && cd tmux-agent-herd
go build -o bin/herd ./cmd/herd
go test ./...            # unit + integration tests (Linux), ~25 s
go test -short ./...     # skip the 10 s CPU check
```

Integration tests start throwaway tmux servers (`tmux -L herdtest-* -f /dev/null`)
with a temporary HOME. They never touch your tmux server or agent configs.

To try your build in your own tmux: `tmux run-shell "$PWD/agent-herd.tmux"`.

## Layout

| Path | What |
| --- | --- |
| `cmd/herd` | the single binary: daemon (`herd daemon`) and CLI |
| `internal/state` | state table and source priority merge |
| `internal/daemon` | herdd: polling, socket API, pane options, notifications |
| `internal/harness` | per-agent-CLI integration (`herd setup`) |
| `internal/hooks` | maps hook calls to states |
| `internal/poll`, `patterns/` | screen heuristics |
| `scripts/hooks/` | hook/plugin/extension templates installed into agent CLIs |
| `test/` | integration tests and screen fixtures |

## Adding an agent CLI

1. If it has hooks, plugins or extensions: add a template in `scripts/hooks/`
   that calls `herd-hook --agent <name> <state>`, and an entry in
   `internal/harness/harness.go`. Tests in `internal/harness` cover every
   entry automatically.
2. Add a screen pattern file `patterns/<name>.toml` with fixtures in
   `test/fixtures/<name>/<state>[.<kind>]-<desc>.txt` (real captures please).
3. Add the command name to the default `@herd-agents` list.

## Releasing (maintainers)

Bump `VERSION` and `var version` in `cmd/herd/main.go`, update
`CHANGELOG.md`, then tag: `git tag v0.2.0 && git push origin v0.2.0`. The
release workflow builds binaries for linux/darwin × amd64/arm64.

## Recording the demo and preview image

The README GIF and the social preview are generated, so they can be redone
whenever the UI changes:

```sh
go build -o bin/herd ./cmd/herd
scripts/media/record-demo.sh       # docs/demo.gif + stills: demo-still.png, demo-pinned.png (~1 min)
scripts/media/social-preview.sh    # docs/social-preview.png (1280x640)
```

They need tmux, python3, chromium (or Chrome) and ffmpeg, and run on a
throwaway tmux server. The story is scripted in `record-demo.sh`. Upload
`docs/social-preview.png` under Settings → General → Social preview.
