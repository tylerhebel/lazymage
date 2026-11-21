# Phase 0 — ground truth

Status: **verified against the docker-magento source and a fake engine; real
OrbStack capture still pending.**

The build machine (WSL2 Linux) has no Docker engine and no sudo, so lazymage was
tested with:

1. The real `markshust/docker-magento` repository (compose files v53.2.0 and its
   `bin/*` scripts), copied into a demo project by `scripts/demo-project.sh`.
2. `testdata/fakebin/docker` and `testdata/fakebin/orb`, which emit the real
   output formats. The real `bin/start`, `bin/stop`, `bin/magento`,
   `bin/clinotty`, `bin/docker-compose` run against them unchanged, as
   subprocesses.

## Findings from the docker-magento source

| Topic | What docker-magento actually does | What lazymage does |
|---|---|---|
| Compose files | `bin/docker-compose` uses `compose.yaml` + `compose.healthcheck.yaml` + `compose.dev.yaml` (+ `compose.versions.yaml`, `compose.override.yaml` if present) | Direct `docker compose` calls pass the same `-f` set |
| Domain | No root `.env`. `bin/setup-domain` sets `web/*/base_url` in the database and adds `/etc/hosts` | `.env` → `env.php` → `bin/magento config:show web/secure/base_url` (cached) |
| Env files | `env/db.env`, `env/magento.env`, `env/phpfpm.env`, … (contain passwords) | Never read for display; the capture script records keys only |
| PHP service | `phpfpm` (plus `phpfpm-xdebug`) | `phpfpm`, picked from `config --services` |
| `bin/magento` | `bin/cli bin/magento` → `docker compose exec phpfpm` (TTY) | `bin/clinotty bin/magento` (exec `-T`) for piped output |
| `bin/start` | `up -d`, then backgrounds `cache-clean --watch` that holds stdout open | Run ends when the script exits (WaitDelay) |
| `bin/status` | `bin/docker-compose ps` | bin-only mode parses this text |
| `bin/log` (singular) | Tails Magento's `var/log` files, not container logs | Not used; container logs use `docker compose logs` |
| `bin/start` memory check | Needs `docker info` "Total Memory" ≥ 6 GB and `bc` on the host | Nothing to do; noted for OrbStack (it reports its own limit) |
| Search engine | OpenSearch (`opensearch` service) by default | Detected from service names / compose image |

## Capture on the Mac

On the Mac, with OrbStack running and the project's stack started:

```bash
cd ~/code/my-magento-project
/path/to/lazymage/scripts/capture-ground-truth.sh
orb stop && /path/to/lazymage/scripts/capture-ground-truth.sh ./lazymage-ground-truth-stopped; orb start
```

`.env` files are captured as keys only. From `env.php`, only the admin
`frontName` line is kept. Copy the output to `docs/ground-truth/raw/`
(gitignored).

## Checklist for the real-machine pass

- [ ] Project runs under OrbStack with no workarounds, or list each tweak here.
- [ ] `docker info --format '{{json .}}'` OperatingSystem / Name values under
      OrbStack (lazymage matches any "orbstack" in OS, name, context, or socket path).
- [ ] `docker compose ps --format json` form: NDJSON (v2.21+) vs. array (both parsed).
- [ ] `bin/clinotty bin/magento --version` prints `Magento CLI <version>`.
- [ ] Engine-stopped stderr from `docker compose ps` contains "Cannot connect to
      the Docker daemon" (used to flip the header to **engine stopped**).
- [ ] `docker compose logs -f` behaviour when a followed container restarts
      (lazymage reconnects either way).
- [ ] Time to first screen < 500 ms with a warm facts cache.

## Real-engine pass: WSL2 + Docker Desktop (2026-11-21)

Docker Desktop 29.9.3 (WSL integration, `default` context →
`/var/run/docker.sock`), Compose v5.7.0. The project was made by
`scripts/demo-project.sh` (docker-magento compose files, no Magento source) and
started with the real `bin/start`.

| Check | Result |
|---|---|
| `bin/start` | Works; memory check passes (Docker reports 31 GiB). It never exits on its own because the background `cache-clean --watch` keeps stdout open, which confirms the WaitDelay handling. |
| `docker compose ps --format json` | NDJSON (one object per line). |
| Header health | `◐ some down 8/9 up` was correct: `app` (nginx) exits on the demo's stub `nginx.conf`. |
| Runtime detection | Classified as Docker Desktop. **Fixed:** the Docker Engine row showed "installed, not running" because `/var/run/docker.sock` is Desktop's proxy socket under WSL. |
| Engine-stopped stderr | Docker CLI 29 says `failed to connect to the docker API at …`, not "Cannot connect to the Docker daemon". **Fixed:** this is now matched explicitly; before, it matched only through the `.sock` + "no such file" fallback. |
| Engine start on WSL | **Fixed:** this used to offer `systemctl --user start docker-desktop`, which does nothing under WSL. It is now a show-only "start Docker Desktop on Windows". |
| `docker compose logs -f` across a restart | Compose v5 keeps following on its own and prints `redis-1 exited with code 0 (restarting)`. No reconnect was needed. |
| `bin/clinotty bin/magento …` | Without Magento source it exits 127 with the OCI "no such file" error; the output view shows it and the exit code. `--version` is still unchecked until a real install exists. |
| Shell (`s`) | The login shell `/usr/bin/zsh` isn't in the container, so it falls back to bash with a note. `exit` returns to the TUI. |

