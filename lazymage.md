# Product requirements: lazymage — TUI for Mark Shust's docker-magento

**lazymage** is a lazygit-style terminal UI for Magento 2 projects running on Mark Shust's docker-magento. You `cd` into a project, run one command, and manage containers, logs, Magento CLI, and Composer from a single keyboard-driven screen — no memorized `bin/*` commands, no Docker dashboard.

The name is locked in: the app, binary, and command are all **lazymage**. Do not use a working name or rename it.

> **How to use this document with Claude:** Build in the phases in section 13, in order. Stop after each phase for review before starting the next. Do not add features from the v2 list into v1 without asking. Where a decision is marked **OPEN**, ask before coding it — do not pick silently.

---

## 1. Product overview

### Vision

```bash
cd ~/code/my-magento-project
lazymage
```

The app detects the docker-magento project from the current directory, shows project and Magento status in the header, and lets the developer run the commands they use every day from searchable panels and a command palette.

### Problem

docker-magento is powerful but command-heavy. Daily work means remembering dozens of wrappers — `bin/magento`, `bin/composer`, `bin/start`, `bin/stop`, `bin/logs`, `bin/mysql`, `bin/cli`, `bin/xdebug` — plus Docker Compose syntax, container names, and the project's domain and deploy mode. Context lives in five places at once: terminal history, `.env`, `docker compose ps`, log streams, and the browser.

### Solution in one line

One TUI, launched from inside the project, that holds that context on screen and turns the common commands into navigable, confirmable actions.

---

## 2. Goals and non-goals

### Goals for v1

- Launch from any directory inside a docker-magento project and find the project root automatically.
- Show overall stack health, project domain, Magento version, and deploy mode at a glance in the header.
- Cover the daily loop: start / stop / restart, follow logs, run Magento CLI and Composer commands, open the site and admin, and shell into the PHP container.
- Put every action in one fuzzy-searchable command palette with a shortcut shown for each, and let the user create, edit, pin, and delete their own custom commands in that palette.
- Run safely on OrbStack first, without breaking on Docker Desktop.
- Remember the user's runtime and integration choices, favorite commands, custom commands and their shortcuts, and command history between sessions.

### Non-goals for v1

- Replacing docker-magento, Docker Compose, OrbStack, or lazygit.
- Creating, scaffolding, or downloading a Magento project.
- Editing Magento code, PHP files, or Composer dependencies beyond running Composer commands.
- Managing multiple projects at once — one TUI instance manages one project.
- A graphical interface, a web UI, or a VS Code extension.

---

## 3. Target user and environment

### Target user

A Magento 2 developer working locally with Mark Shust's docker-magento, comfortable in a terminal, who prefers keyboard-driven tools in the style of lazygit. The initial user is the project owner; the design should still work for a teammate who clones the tool fresh.

### Primary environment

| Item | Primary target |
|---|---|
| OS | macOS, Apple silicon |
| Container runtime | OrbStack |
| Docker interface | `docker` and `docker compose` CLIs provided by OrbStack |
| Project type | Mark Shust docker-magento, Magento code under `src/` |
| Terminal | Modern terminal with 256-color / truecolor support, minimum usable size 100 x 30 |

### Secondary environment

- macOS with Docker Desktop instead of OrbStack — must work, is not the optimization target.
- Linux with Docker Engine / Docker Compose — best effort in v1, no OrbStack-specific code may block it.

### Assumptions to verify in Phase 0

- An existing docker-magento project runs under OrbStack with no project-specific workarounds. If a real project needs tweaks — compose file, `.env`, `/etc/hosts` for the local domain, or volume settings — document them in Phase 0 and add them to project detection.
- The `bin/*` scripts in the target project work unchanged when called from a subprocess.

---

## 4. First-time setup

This section is a required feature, not a one-time install note. The first run is a guided setup wizard inside the TUI. No main panels appear until setup is complete. Individual steps that say they may be skipped use their stated presets. The implementation stack is already decided — Go + Bubble Tea (see section 5) — so setup does not ask the user to pick a stack.

### 4.1 When setup runs

- On first launch, when no config file exists at `~/.config/lazymage/config.yaml`.
- Again on demand: `lazymage --setup`, pressing `S` from the help screen, or choosing **Re-run setup** in Settings.
- As a partial re-run when a saved choice becomes invalid — for example, the saved runtime is no longer installed. Only the broken step re-opens; completed steps keep their values.

### 4.2 Setup flow

Each step is one screen. `Enter` confirms and advances, `Esc` goes back, `Ctrl+C` exits without saving a partial config. A progress indicator shows step number and total.

**Step 1 — Project confirmation**

- Show the detected project root, project name (directory name), domain, Magento version, and deploy mode.
- If no project was detected, show the searched-for markers and let the user either navigate to a project and retry, or continue to the remaining steps in a project-less preview mode. Do not create a project.

**Step 2 — Container runtime**

- Auto-detect and list what was found: OrbStack, Docker Desktop, Docker Engine, or none.
- Show detection evidence for each: `docker info` result, active `docker context`, and CLI version.
- The user picks the runtime to use. Default to OrbStack when it is detected.
- If the chosen engine is not running, offer **Start it now** (`orb start` for OrbStack, opening Docker Desktop for Docker Desktop), **Continue anyway**, or **Pick a different runtime**.

**Step 3 — Integration mode**

- Pick how the TUI runs commands: **Hybrid (recommended)**, **bin scripts only**, or **Direct Docker only**. Explain each in one line, using the definitions in section 9.
- Default highlight: Hybrid.

**Step 4 — Daily commands and favorites**

- Show the preset favorite list from section 8.3. The user toggles favorites on or off with `Space`.
- Allow adding a custom command using the full custom-command form in section 8.7 — name, command, prompts, confirmation, optional shortcut, and scope.
- This step may be skipped; the preset list is used.

**Step 5 — Preferences**

- Shell to use for container shell sessions (default: detected login shell, falling back to `bash`).
- Editor for opening config and log files (default: `$EDITOR`, falling back to `vi`).
- Browser behavior: system default browser, or a named browser.
- Log defaults: lines shown on open (default 200), follow on open (default on).
- Refresh interval for the header health indicator (default 2 seconds).

**Step 6 — Review and save**

- Show every choice on one screen: project, runtime, integration mode, favorite count, and preferences.
- `Enter` saves config and opens the main TUI. `Esc` returns to any step to change it.
- After saving, show the config file path on the status line so the user knows where their choices live.

### 4.3 Setup acceptance criteria

- Deleting the config and relaunching runs the full wizard.
- Completing the wizard writes a valid config that a second launch reads without showing the wizard.
- Choosing each runtime option saves that exact value — no option falls back to a hidden default.
- Exiting mid-wizard leaves no partial config file.
- Re-running setup shows current values pre-selected, not factory defaults.

---

## 5. Tech stack — Go + Bubble Tea (chosen)

**Decision: Go + Bubble Tea.** This is locked in — do not build parallel implementations in another stack, and do not re-open the stack choice in setup or in Phase 0.

The alternatives considered were Python + Textual, Rust + Ratatui, and Node + Ink / OpenTUI. Bubble Tea was chosen for its single-binary distribution, fast startup, and lazygit-class TUI feel.

### Stack detail

- Language: Go, current stable release.
- TUI framework: Bubble Tea (Elm-style model / update / view).
- Styling: Lipgloss. Components: Bubbles (list, viewport, text input, spinner, help).
- YAML config parsing and Docker CLI output parsing with maintained Go libraries — check current versions at build time, do not pin from memory.
- Build targets for v1: macOS arm64 first, macOS amd64 second. Linux amd64 is a stretch target.
- Distribution for v1: build from source with `go build` / `go install`, plus a documented release-binary layout. Homebrew tap is v2.

---

## 6. Container runtime: OrbStack first

### Rules

- Talk to Docker only through the `docker` and `docker compose` CLIs. Never call Docker Desktop APIs, and never hard-code a Docker Desktop socket path.
- Detect the runtime at startup and on demand with `r`. Show the detected runtime and context in the header.
- Detection uses `docker info` and `docker context ls` / `docker context show`. OrbStack typically reports its own context and server identity — match on the returned values at runtime, not on a hard-coded string copied from documentation.
- Assume OrbStack manages CPU and memory itself. The TUI has no resource sliders and no links to a Docker Desktop dashboard.

### Engine not running

- If the engine is unreachable, the TUI still opens. The header shows **Engine: stopped** and the main panel shows a single clear action: **Start engine**.
- Starting uses `orb start` for OrbStack. For Docker Desktop, open the Docker Desktop application. For Linux Docker Engine, show the service command instead of running it.
- After starting, poll `docker info` until it succeeds or 30 seconds pass, then refresh the header and main panel.

### Performance note

OrbStack file sharing for the `src/` bind mount is fast, but log streaming and health polling must still be efficient: poll the header health indicator on the configured interval, stream logs, and never re-run a full compose listing on every keypress.

---

## 7. Project detection

### Finding the root

Starting from the current working directory, walk up parent directories until a directory contains all of:

- A Docker Compose file (`compose.yaml`, `compose.yml`, `docker-compose.yaml`, or `docker-compose.yml`)
- An executable `bin/magento`
- A `src/` directory

Stop at the filesystem root. If no match is found, show the not-a-project screen from setup step 1, listing the markers that were searched for and the starting directory.

### Reading project facts

Parse what is available; show **unknown** rather than guessing when a source is missing.

| Fact | Source, in priority order |
|---|---|
| Project name | Root directory name |
| Domain | `.env` in the project root |
| Magento version | `bin/magento --version` output, cached; then Composer data under `src/` |
| Deploy mode | `bin/magento deploy:mode:show` output, cached |
| PHP version | Running PHP container, or compose / image config |
| Search engine | Running containers — Elasticsearch or OpenSearch service present |
| Admin path | Magento config under `src/app/etc/` if readable |

- Cache slow facts (Magento version, deploy mode) and refresh them on `r` or after a command that can change them, such as `setup:upgrade` or `deploy:mode:set`.
- Never run a Magento command just to fill the header before the engine is confirmed running.

---

## 8. Functional requirements

### 8.1 Header and status

- Always visible: project name, domain, Magento version, deploy mode, runtime (for example, OrbStack), Docker context, and an overall health indicator — all up, some down, engine stopped, or unknown.
- Domain is selectable: pressing `o` on it opens the storefront in the browser; `O` opens the admin when the admin path is known.

### 8.2 Logs view

- Pick a log source from a searchable list of the project's services when opening the view; `a` shows all services combined. There is no containers panel — services are not listed in a standing panel.
- Follow mode on by default, toggle with `f`. Pause streaming with `Space` without losing the buffer.
- Search within the buffer with `/`, next / previous match with `n` / `N`.
- Filter by text, clear the buffer, and save the visible buffer to a file from the command palette.
- Handle container restarts: if the followed container restarts, resume following and note the restart in the view.

### 8.3 Magento commands

- A searchable list of Magento CLI commands. Preset favorites, shown first:

  - `cache:flush`
  - `cache:clean`
  - `indexer:reindex`
  - `setup:upgrade`
  - `setup:di:compile`
  - `setup:static-content:deploy`
  - `deploy:mode:show`
  - `config:show`
  - `module:status`
  - `cron:run`

- Free-form entry: type any `bin/magento` command with arguments. The TUI shows the full command that will run before it runs.
- Commands that prompt for input keep an interactive input line — the TUI must not swallow a prompt.
- Output streams into the output panel. Exit code is always shown. A failed command stays visible until dismissed — it does not scroll away silently.
- History: last 50 commands for the project, navigable with arrow keys in the command input, persisted per project.

### 8.4 Composer

- Run Composer through the project's wrapper (`bin/composer` where present, otherwise `docker compose exec` into the PHP container).
- Presets: `install`, `update`, `require`, `show` (outdated). `require` prompts for the package name.
- Long-running Composer commands show elapsed time and a cancel action (`Ctrl+C` cancels the child process, not the TUI).

### 8.5 Lifecycle actions

- Start, stop, and restart the whole stack, mapped to the project's own scripts where they exist (`bin/start`, `bin/stop`, `bin/restart`) and to `docker compose` equivalents otherwise.
- Show progress per service during start, not a single spinner with no detail.
- Destructive or slow actions ask for confirmation first: full stack stop, `cache:flush` is not destructive and does not confirm; volume removal, database import, and deploy-mode changes do confirm. Volume removal is not offered at all in v1.

### 8.6 Shell and browser shortcuts

- Shell into the PHP container with one key, using the shell chosen in setup. Exiting the shell returns to the TUI in the same state.
- Open storefront, open admin, and copy the storefront URL to the clipboard.

### 8.7 Command palette, shortcuts, and custom commands

The palette is the primary way to run things — not a hidden extra. If an action exists in the app, it is in the palette, searchable, and shows its shortcut.

**Opening and using the palette**

- `x` or `Ctrl+P` opens it from anywhere. `Esc` closes it, `Enter` runs the highlighted entry, arrows / `j` / `k` move, and typing fuzzy-filters by title, category, keywords, and the command that will run.
- Every row shows: title, category, the exact command or action, and its keyboard shortcut if it has one. Custom commands are tagged `custom`; favorites / pinned commands sort first, then most recently used, then grouped by category.
- The palette stays current: entries that cannot run right now (for example, logs while the engine is stopped) are shown disabled with the reason, not hidden.

**Built-in palette catalog — ship all of these in v1**

| Category | Palette entries |
|---|---|
| Stack | Start stack, Stop stack (confirm), Restart stack, Start engine, Refresh status and project facts |
| Logs | Logs: all services, Logs: pick a service…, Pause / resume stream, Toggle follow, Search logs, Clear log buffer, Save log buffer to file… |
| Magento — cache & index | `cache:flush`, `cache:clean`, `cache:status`, `indexer:reindex`, `indexer:status` |
| Magento — setup | `setup:upgrade`, `setup:di:compile`, `setup:static-content:deploy`, `deploy:mode:show`, `config:show`, `module:status`, `cron:run`, Run a Magento command… (free-form) |
| Composer | `composer install`, `composer update`, `composer require…`, `composer show` (outdated), `composer dump-autoload`, Run a Composer command… (free-form) |
| Project | Shell into PHP container, Open storefront, Open admin, Copy storefront URL, Open project `.env` in editor, Open TUI config in editor |
| App | Settings, Re-run setup, Help, Create a custom command…, Manage custom commands…, Quit |

- Add keywords so natural searches work: “flush”, “reindex”, “deploy”, “shell”, and “restart” must find the right entry in a few keystrokes.
- Every built-in entry above also works without the palette where section 10 gives it a direct key.

**Creating a new custom command**

- Start it three ways: choose **Create a custom command…** in the palette, press `Ctrl+N` while the palette is open, or use **New command** on the Manage custom commands screen (also reachable from Settings). Setup step 4 uses this same form.
- The form collects, in order:
  - **Name** — shown in the palette; must be unique in its scope.
  - **Category** — pick an existing category or type a new one.
  - **Kind** — Magento CLI, Composer, `bin/*` wrapper, Docker Compose, or host shell. Host shell is marked as such everywhere it appears and confirms by default.
  - **Command** — the command string, without the wrapper prefix for Magento / Composer kinds (the app adds `bin/magento` / `bin/composer`).
  - **Prompted arguments (optional)** — placeholders such as `{{package}}` or `{{module}}`; for each one: prompt label, default value, and whether it is required. Running the command asks for these first, then shows the fully built command before it runs.
  - **Needs confirmation?** — on / off. Destructive-looking commands default to on.
  - **Keyboard shortcut (optional)** — press the key or chord to assign it. If it is already taken, show what uses it and offer **Replace**, **Pick another**, or **Cancel** — never silently steal a shortcut.
  - **Scope** — **This project only** or **All projects**. Project commands override a global command with the same name in that project.
  - **Pin to top of palette?** — on / off.
- Validate before saving: non-empty name and command, unique name and shortcut in scope, and every `{{placeholder}}` in the command has a matching prompt definition. Show errors inline in the form, not after it closes.
- On save, the command appears in the palette immediately — no restart — and runs under the same rules as built-ins (section 9): exact command shown first, streamed output, exit code, cancellation, and redacted history.

**Managing custom commands**

- **Manage custom commands…** lists only custom commands, searchable like the palette. On a selected command: `Enter` run, `e` edit, `d` delete (with confirmation), `Space` pin / unpin, and an action to duplicate it.
- Editing uses the same form, pre-filled. Deleting a project command does not delete a global command it was overriding.
- Custom commands persist in the config file (section 11): global commands at the top level, project commands under that project's entry. They survive quitting and relaunching, and a malformed custom command is reported and skipped — it must not break startup or the built-in palette.
- No import / export format in v1; sharing a command means copying its YAML entry from the config file.

### 8.8 Help and settings

- `?` opens a help overlay listing every keybinding — built-in and user-assigned — grouped by category, generated from the same keymap and custom-command data the app actually uses.
- Settings screen edits every setup choice without deleting the config file, offers **Re-run setup**, and links to **Manage custom commands**.

---

## 9. Command execution model

The integration mode picked in setup step 3 controls this behavior.

### Hybrid — recommended and default

- Actions go through the project's own wrappers, so upstream docker-magento updates keep working: `bin/magento`, `bin/composer`, `bin/start`, `bin/stop`, `bin/restart`, and the other `bin/*` scripts in the appendix.
- The header health check and logs go through Docker directly, where structured or streaming output is needed: `docker compose ps --format json`, `docker compose logs -f`, and `docker compose exec` for shells.

### bin scripts only

- Every action, including the header health check, is derived from the `bin/*` scripts and plain `docker compose ps` text. Simpler and maximally upstream-faithful, with more fragile parsing.

### Direct Docker only

- Everything uses `docker compose` and `docker compose exec phpfpm bin/magento ...` directly, ignoring the `bin/*` wrappers except for project detection.

### Execution rules for all modes

- Run every command with the project root as the working directory.
- Show the exact command string in the output panel before or as it runs. No silent commands.
- Stream stdout and stderr interleaved, in arrival order, with stderr visually distinguished.
- Support cancellation of the running child process without quitting the TUI.
- Never build a shell string from user input without quoting it. Pass arguments as an argument list where Go allows it.
- Record each run — command, start time, duration, exit code — in history. Do not record secrets: if a command contains a password or token argument, store it redacted.

---

## 10. Layout and keybindings

### Layout

```text
┌ Project · domain · Magento version · mode · runtime · ● health ──────┐
├──────────────────────────────────────────────────────────────────────┤
│  Logs / Magento / Composer  (tabbed main panel, full width)          │
│                                                                      │
│  streaming output                                                    │
│                                                                      │
├──────────────────────────────────────────────────────────────────────┤
│ Output / status line · last command · exit code · config path      │
└──────────────────────────────────────────────────────────────────────┘
```

- There is no containers panel and no standing list of container statuses. The only at-a-glance status is the overall health indicator in the header; per-service detail is reached through the log-source picker and the command palette, not a panel.
- Main panel: tabbed — Logs, Magento, Composer, full width. The running command's output takes over the main panel while it runs.
- The layout must remain usable at 100 x 30 and reflow cleanly at larger sizes. No fixed pixel or character assumptions beyond the minimum.

### Keybindings

| Key | Action |
|---|---|
| `j` / `k`, arrows | Move within the focused view |
| `Tab` / `Shift+Tab`, `1`–`3` | Switch tab in the main panel |
| `Enter` | Run / open the selected item |
| `x` or `Ctrl+P` | Command palette |
| `Ctrl+N` (in palette) | Create a new custom command |
| `e` / `d` (on a custom command) | Edit it / delete it (with confirmation) |
| `l` | Logs — pick a service, or all services |
| `s` / `S` | Shell into PHP container / open Settings |
| `o` / `O` | Open storefront / open admin |
| `r` | Refresh header health and cached project facts |
| `f` | Toggle log follow |
| `/` | Search in the current view |
| `?` | Help overlay |
| `q` | Quit — confirm if a command is running |
| `Esc` | Back / close overlay |

This table is the default keymap, not the ceiling: a custom command may add its own shortcut (section 8.7), and palette entries without a direct key are still one `x` plus a few keystrokes away. Every keybinding — default and user-assigned — appears in the `?` help overlay, generated from the same keymap the app uses, not a hand-maintained list that can drift.

---

## 11. Configuration

One user-level config file, written by setup and editable in Settings. It holds per-project history and per-project custom commands (sections 8.7); broader project-level config overrides are v2.

```yaml
# ~/.config/lazymage/config.yaml — illustrative; field names are the contract
runtime: orbstack            # orbstack | docker-desktop | docker-engine
integration_mode: hybrid     # hybrid | bin-only | docker-only
shell: /bin/zsh
editor: ""
browser: default             # default, or a browser name
logs:
  tail_lines: 200
  follow_on_open: true
refresh_seconds: 2
favorites:
  - cache:flush
  - cache:clean
  - indexer:reindex
  - setup:upgrade
custom_commands:             # global — apply to all projects (section 8.7)
  - name: "Reindex catalog"
    category: "Magento — setup"
    kind: magento              # magento | composer | bin | compose | host-shell
    command: "indexer:reindex catalog_product_price"
    prompts: []                # [{key, label, default, required}]
    confirm: false
    shortcut: ""               # optional key / chord; empty = palette only
    pinned: true
projects:
  /path/to/project:
    history: []              # capped at 50, secrets redacted
    custom_commands: []      # project-only; same shape, override globals by name
```

- Unknown keys are preserved on save, not deleted.
- A malformed config is never overwritten. The app reports the parse error, shows the file path, and offers to start setup fresh or exit.
- No credentials, tokens, or database passwords are stored in config.

---

## 12. Non-functional requirements

- **Startup:** under 500 ms to the first screen when the engine is running and project facts are cached. First-run setup and uncached detection may take longer and must show progress.
- **Responsiveness:** keypresses never block on a Docker or Magento call. All external commands run asynchronously and stream results in.
- **Safety:** destructive actions confirm; quitting with a running command confirms; a cancelled command reports its exit state honestly.
- **Errors:** every external failure shows the command, its exit code, and its stderr in plain language. No stack traces in the normal UI.
- **Logging:** the app may keep a debug log under the config directory, off by default, enabled with `--debug`. Command secrets are redacted there too.
- **Accessibility:** color is never the only signal — the header health indicator also uses text (`all up`, `some down`, `engine stopped`, `unknown`). Respect `NO_COLOR` where supported.

---

## 13. Build phases for Claude

Build one phase at a time. Each phase ends with a demo the developer can run, plus the checks in section 14 that apply to it. Stop and wait for review after each phase.

### Phase 0 — Verify the ground truth

- Confirm a real docker-magento project runs under OrbStack. Record any required tweaks.
- Capture real sample output for: `docker compose ps --format json`, `docker info`, `bin/magento --version`, `bin/magento deploy:mode:show`, and the project's `.env` keys (keys only, no secret values).
- Scaffold the empty Go + Bubble Tea project with a runnable placeholder TUI.

### Phase 1 — Detection, config, and setup wizard

- Project-root walk-up detection and the not-a-project screen.
- Config load / save with the malformed-config behavior in section 11.
- The full first-time setup wizard from section 4, all six steps.

### Phase 2 — Status shell

- Header (with the overall health indicator) and footer with live refresh, over a single full-width main panel. No containers panel.
- Engine-stopped state and the Start engine action.
- Help overlay generated from the keymap, and quit / navigation keys.

### Phase 3 — Logs

- Log-source picker (pick a service, or all services) plus combined log streaming, follow toggle, pause, search, and save-buffer.

### Phase 4 — Magento and Composer commands

- Favorites list, free-form command entry, streaming output, exit codes, cancellation, interactive prompts, and per-project history.
- Composer presets, including the `require` package prompt.

### Phase 5 — Lifecycle and shortcuts

- Start / stop / restart with per-service progress and confirmations.
- PHP shell session, and storefront / admin browser shortcuts.

### Phase 6 — Palette, custom commands, settings, and polish

- Command palette with the full built-in catalog in section 8.7, fuzzy search, shortcut hints on every row, and disabled-with-reason entries.
- Custom commands end to end: create form (including prompted arguments, confirmation, optional shortcut with conflict handling, and project vs. global scope), manage screen (run / edit / duplicate / delete / pin), and persistence in config.
- Settings screen, re-run setup, layout at minimum terminal size, and error-message pass.

### v2 candidates — do not build in v1

- Xdebug enable / disable / status — **built (v2)**: cookie-routed phpfpm-xdebug readiness, bin/setup-nginx, bin/debug-cli; legacy bin/xdebug toggle
- Database tools: dump, import, `bin/mysql` shell, URL search-and-replace — **built (v2, 2026-11-21)**
- Maintenance-mode toggle and cron status — **built (v2)**
- Git status panel for `src/` — **built (v2)** as tab 4
- Multi-project switching, broader project-level config overrides (beyond per-project history and custom commands) — **built (v2)**; Homebrew distribution — not yet (needs a tap repository)

---

## 14. Acceptance criteria and testing

Test against a real docker-magento project under OrbStack, not mocks alone. Unit tests cover parsing; the criteria below are end-to-end.

- Launching inside a nested subdirectory of a project finds the same root as launching at the root.
- Launching outside any project shows the not-a-project screen and never crashes.
- With OrbStack stopped, the app opens, reports the engine as stopped, and Start engine recovers it.
- Setup criteria in section 4.3 all pass.
- Starting the stack from the TUI results in the header health indicator showing all up within one refresh interval, and no containers panel or standing container-status list appears anywhere in the UI.
- Following logs shows new lines from a running container without restarting the stream.
- The palette lists every built-in entry in the section 8.7 catalog, each showing its shortcut where it has one, and fuzzy search for “flush”, “reindex”, and “restart” finds the right entry.
- Creating a custom command in the palette makes it appear immediately; quitting and relaunching keeps it, its pin state, and its shortcut. A command with a `{{placeholder}}` prompts for that value and shows the built command before running. Assigning an already-used shortcut offers replace / pick another / cancel and never silently overwrites. Editing and deleting a custom command from the manage screen work, and deleting a project command leaves a global command of the same name intact.
- Running `cache:flush` from favorites shows streamed output and exit code 0; running an intentionally invalid Magento command shows a non-zero exit code and stderr, and the app stays open.
- Cancelling a long Composer command leaves the TUI responsive and the stack running.
- Quitting and relaunching preserves config, favorites, and per-project history.
- No command containing a password or token appears unredacted in history or the debug log.

---

## 15. Open decisions

Claude must ask about these before or during the named phase. The rest of this document can be built without them.

| Decision | Needed by | Options / notes |
|---|---|---|
| App and command name | Decided | **lazymage** — locked in. Binary, repo, and command are `lazymage`; config directory is `~/.config/lazymage/`. Not an open decision. |
| Repository home | Phase 0 | Its own new repository, not a fork of docker-magento. Confirm location and license. |
| Developer's actual top daily commands | Phase 4 | Preset list in section 8.3 is the starting point. Replace or reorder it with the commands used most in real work. Anything missing can also be added later as a custom command (section 8.7) without a code change. |
| Git panel timing | After v1 | v2 by default. Move into v1 only by explicit decision, with scope added to section 8. |
| Linux priority | After v1 | Best effort in v1. Promote to a tested target only by explicit decision. |

---

## Appendix A. docker-magento command inventory

The wrappers the TUI calls in Hybrid and bin-only modes, where the project provides them. Detect presence per project — do not assume every script exists in every version of docker-magento.

| Wrapper | Purpose |
|---|---|
| `bin/magento` | Magento CLI inside the PHP container |
| `bin/composer` | Composer inside the PHP container |
| `bin/start` / `bin/stop` / `bin/restart` | Lifecycle for the whole stack |
| `bin/status` | Service status, where provided |
| `bin/logs` | Container logs |
| `bin/cli` / `bin/bash` | Shell or one-off command in the PHP container |
| `bin/mysql` | MySQL client |
| `bin/redis` | Redis client |
| `bin/xdebug` | Xdebug enable / disable / status — v2 in this TUI |
| `bin/setup` / `bin/update` | Project setup and update flows, where provided |

## Appendix B. Glossary

- **docker-magento** — Mark Shust's Docker-based development environment for Magento 2, built around Docker Compose and a set of `bin/*` wrapper scripts, with Magento code under `src/`.
- **OrbStack** — A macOS container runtime that provides Docker-compatible `docker` and `docker compose` CLIs as an alternative to Docker Desktop.
- **TUI** — Text user interface: a full-screen, keyboard-driven interface inside a terminal.
- **PHP-FPM container** — The container running PHP for Magento, referred to in compose and `docker compose exec` commands as the PHP service (commonly `phpfpm`).

