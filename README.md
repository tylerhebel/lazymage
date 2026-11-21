# lazymage

A lazygit-style terminal UI for Magento 2 projects running on Mark Shust's
[docker-magento](https://github.com/markshust/docker-magento). Targets macOS
(OrbStack first, Docker Desktop too) and Linux (Docker Engine, best effort).

```bash
cd ~/code/my-magento-project     # or any directory inside it
lazymage
```

The full spec is in [`lazymage.md`](lazymage.md).

## Build and install

Requires Go 1.26+ (built and tested with Go 1.27.4; older toolchains auto-download via GOTOOLCHAIN).

```bash
go build -o lazymage .                 # local binary
go install .                           # into $(go env GOPATH)/bin
GOOS=darwin GOARCH=arm64 go build -o dist/lazymage-darwin-arm64 .
GOOS=darwin GOARCH=amd64 go build -o dist/lazymage-darwin-amd64 .
```

Release-binary layout (v1, no Homebrew yet): `dist/lazymage-<os>-<arch>`, one
static binary each, built with `-ldflags "-X main.version=vX.Y.Z"`.

Flags: `--setup` re-runs the setup wizard, `--debug` writes
`~/.config/lazymage/debug.log` (secrets redacted), `--version`.

## What it does

| Area | Summary |
|---|---|
| Setup | Six-step first-run wizard: project, runtime (with evidence and **Start it now**), integration mode, favorites and custom commands, preferences, review. Nothing is written until you confirm. A saved runtime that is no longer installed reopens only that step. |
| Header | Project, domain, Magento version, deploy mode, runtime and context, and health as **all up / some down / engine stopped / unknown** (symbol + text, not color alone). |
| Logs (`1`, `l`) | Pick a service or all services, follow (`f`), pause (`space`), search (`/`, `n`/`N`), filter / clear / save from the palette. If a followed container restarts, the stream resumes and the view notes it. |
| Magento (`2`) | Favorites first (`space` toggles), presets, free-form `bin/magento …` with per-project history (`↑`/`↓`). The exact command is shown before it runs; output streams; the exit code always shows; stdin is open so prompts work. |
| Composer (`3`) | install, update, require… (asks for the package), show --outdated, dump-autoload, free-form. Elapsed time is shown, and `ctrl+c` cancels the child process, not the app. |
| Stack | Start / stop (confirms) / restart through `bin/start`, `bin/stop`, `bin/restart`, with live per-service progress while they run. |
| Project | `s` shell into PHP, `o` / `O` storefront / admin, `y` copy URL, open `.env` or the config file in your editor. |
| Palette (`x` / `ctrl+p`) | Every action, fuzzy-searchable, with its command and shortcut. Unavailable entries are shown with the reason. `ctrl+n` creates a custom command. |
| Custom commands | Name, category, kind (Magento / Composer / bin / Compose / host shell), `{{placeholder}}` prompts, confirmation, shortcut (conflicts offer Replace / Pick another / Cancel), project or global scope, pin. Manage screen: run, edit, duplicate, delete, pin. |
| Help (`?`) | Generated from the live keymap plus your shortcuts. |

## Configuration

`~/.config/lazymage/config.yaml` (or `$XDG_CONFIG_HOME/lazymage/config.yaml`).
Field names follow section 11 of the spec. Unknown keys are kept on save. A
malformed file is never overwritten: lazymage shows the error and offers to
start setup fresh, which renames the broken file to `config.yaml.broken-<time>`.
A malformed custom command is skipped and reported, and the rest still loads.
Cached facts (Magento version, deploy mode, domain) are stored in
`~/.config/lazymage/cache/facts.json`.

## Integration modes

| Mode | Actions | Status and logs |
|---|---|---|
| Hybrid (default) | `bin/*` wrappers | `docker compose ps --format json`, `docker compose logs -f` |
| bin scripts only | `bin/*` wrappers | `bin/docker-compose ps` (text), `bin/docker-compose logs -f` |
| Direct Docker only | `docker compose exec -T phpfpm …` | `docker compose …` |

Direct `docker compose` calls use the same file set as docker-magento's
`bin/docker-compose`: `compose.yaml`, `compose.healthcheck.yaml`,
`compose.dev.yaml`, and `compose.versions.yaml` / `compose.override.yaml` when
present.

## Decisions made while building (please review)

These choices weren't settled by the spec, so I made a call on each:

1. **Magento and Composer run through `bin/clinotty`** (docker-magento's no-TTY
   wrapper) when it exists, for example `bin/clinotty bin/magento cache:flush`.
   lazymage streams output through pipes, and `bin/magento` → `bin/cli` →
   `docker compose exec` (without `-T`) can fail with "the input device is not a
   TTY". stdin stays open, so interactive prompts still work. If the project has
   no `bin/clinotty`, lazymage falls back to `bin/magento` / `bin/composer`.
2. **The domain does not live in `.env` in stock docker-magento.** There is no
   root `.env`; `bin/setup-domain` writes the domain to Magento's database via
   `config:set`. Lookup order: root `.env` (`DOMAIN`, `VIRTUAL_HOST`, `*BASE_URL`, …),
   then a `base_url` pinned in `env.php`, then (only once the engine is running)
   `bin/magento config:show web/secure/base_url`, cached.
3. **Deploy mode and version fall back to files** (`MAGE_MODE` in `env.php`,
   `magento2-base` in `composer.lock`) before `bin/magento` answers. That keeps
   the header useful before the engine is up. The `bin/magento` values replace
   the fallbacks and are cached.
4. **`bin/start` never fully exits.** It backgrounds a `cache-clean --watch`
   process that keeps the output pipe open. lazymage treats the script's own
   exit as the end of the run, and lets the watcher keep running.
5. **Cancelling** sends SIGINT to the whole process group, then SIGTERM after 3s and
   SIGKILL after 6s. `docker compose exec` does not forward the signal into the
   container, so the output view warns that the in-container process may finish
   on its own.
6. **Container shell:** the spec defaults the shell to your login shell (often
   `/bin/zsh`), which docker-magento's PHP image doesn't have. lazymage tries
   that shell in the container, falls back to bash, and says so. `bin/bash` is
   used when the shell is bash.
7. **Extra keys** not in the spec's table: `a` all-services logs, `n`/`N`
   search matches, `y` copy URL, `v` reopen the last output, `i`/`:` type a
   command, `space` toggle favorite. Reserved keys (`q`, `x`, `esc`, `enter`,
   `j`, `k`, `ctrl+c`, …) can't be assigned to custom commands. Other built-in
   keys can be, after confirming **Replace**: the custom command wins, and help
   marks the override.
8. **Palette `j`/`k`:** typing filters, so `j`/`k` move only after `tab`
   focuses the list. Arrows and `ctrl+j`/`ctrl+k` always move.
9. **`deploy:mode:set…`** is in the catalog (with a confirmation), because the
   spec requires deploy-mode changes to confirm.
10. Go module path is `lazymage`. Change it to your repo URL before publishing,
    so `go install github.com/<you>/lazymage@latest` works.

## Testing

```bash
go test ./...            # parsers, config, redaction, runner, command plans, UI flows
```

**Demo harness (no Docker needed):** `testdata/fakebin/` holds fake `docker` and
`orb` CLIs that speak the real output formats (NDJSON `compose ps`,
`docker info`, compose logs). `scripts/demo-project.sh` builds a project from a
real docker-magento checkout, so the real `bin/*` scripts run against the fake
engine:

```bash
scripts/demo-project.sh /tmp/lm-demo
export PATH="$PWD/testdata/fakebin:$PATH" FAKE_DOCKER_STATE=/tmp/fake-docker-state
cd /tmp/lm-demo && lazymage
# echo stopped > /tmp/fake-docker-state/engine     → engine-stopped state
# touch /tmp/fake-docker-state/restart-db           → simulate a container restart while following logs
# bin/magento lazymage:ask                          → an interactive prompt
```

**Still to verify on a real Mac with OrbStack:** see
[`docs/phase0.md`](docs/phase0.md), and run `scripts/capture-ground-truth.sh`.

## Layout

```
main.go                    flags, login-shell detection, program start
internal/config            config load/save, custom-command templates, redaction
internal/project           root detection, static facts, compose file set
internal/docker            runtime detection, engine start plans, ps parsing, health
internal/runner            async child processes, streaming, stdin, cancellation
internal/plan              integration mode → exact argv
internal/ui                Bubble Tea app: wizard, header, tabs, palette, forms, overlays
testdata/fakebin           fake docker / orb for the demo harness
scripts/                   ground-truth capture, demo project builder
```

## License

MIT — see [LICENSE](LICENSE).
