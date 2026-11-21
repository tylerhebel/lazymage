#!/usr/bin/env bash
# Phase 0 ground-truth capture for lazymage.
#
# Run from the root of a real docker-magento project on the Mac running OrbStack:
#
#   /path/to/lazymage/scripts/capture-ground-truth.sh [output-dir]
#
# It writes one file per command into output-dir (default: ./lazymage-ground-truth).
# Secrets: .env files are captured as KEYS ONLY — values are never written.
# The other outputs can still contain hostnames, paths, and image names, so look
# them over before you share or commit them.
#
# Works with the macOS system bash (3.2) and BSD tools.

set -u

out="${1:-./lazymage-ground-truth}"
mkdir -p "$out"

note() { printf '%s\n' "$*" >&2; }

# run NAME CMD... : capture stdout+stderr and the exit code of CMD into $out/NAME.txt
run() {
  name="$1"; shift
  file="$out/$name.txt"
  {
    printf '$ %s\n' "$*"
    "$@" 2>&1
    printf '\n[exit %s]\n' "$?"
  } >"$file"
  note "captured $name"
}

# keys_only FILE : print variable names from an env file, never values
keys_only() {
  sed -n 's/^[[:space:]]*\(export[[:space:]]\{1,\}\)\{0,1\}\([A-Za-z_][A-Za-z0-9_]*\)[[:space:]]*=.*/\2/p' "$1"
}

# --- Project markers (section 7) -------------------------------------------
{
  echo "pwd: $(pwd)"
  for f in compose.yaml compose.yml docker-compose.yaml docker-compose.yml; do
    [ -f "$f" ] && echo "compose file: $f"
  done
  [ -x bin/magento ] && echo "bin/magento: executable" || echo "bin/magento: MISSING or not executable"
  [ -d src ] && echo "src/: present" || echo "src/: MISSING"
  echo
  echo "bin/ scripts:"
  ls -1 bin 2>&1
} >"$out/project-markers.txt"
note "captured project-markers"

# --- Env keys (keys only, no values) ---------------------------------------
{
  for f in .env env/*.env; do
    [ -f "$f" ] || continue
    echo "## $f"
    keys_only "$f"
    echo
  done
} >"$out/env-keys.txt"
note "captured env-keys"

# Lines that mention a domain/host in compose files (domain detection, extra_hosts).
grep -nE 'extra_hosts|\.test|\.local|VIRTUAL_HOST|DOMAIN|server_name' \
  compose*.y*ml docker-compose*.y*ml 2>/dev/null >"$out/compose-domain-lines.txt"
note "captured compose-domain-lines"

# --- Docker / runtime (sections 6, 9) --------------------------------------
run docker-version          docker version
run docker-info             docker info
run docker-info-json        docker info --format '{{json .}}'
run docker-context-show     docker context show
run docker-context-ls       docker context ls
run docker-context-ls-json  docker context ls --format json
run orb-version             orb version
run orb-status              orb status
run compose-version         docker compose version
run compose-services        docker compose config --services
run compose-ps              docker compose ps
run compose-ps-json         docker compose ps --format json
run compose-ps-all-json     docker compose ps -a --format json

# --- Magento facts (section 7) ---------------------------------------------
run magento-version         bin/magento --version
run magento-deploy-mode     bin/magento deploy:mode:show
run magento-invalid-command bin/magento this:command:does-not-exist

# Admin path lives in src/app/etc/env.php ('backend' => ['frontName' => ...]).
# Capture only that line, not the file (env.php holds DB credentials).
grep -n "frontName" src/app/etc/env.php >"$out/admin-frontname.txt" 2>&1
note "captured admin-frontname"

# Composer data under src/ (fallback Magento version source).
grep -nE '"name"|"magento/product-[a-z]+-edition"|"version"' src/composer.json \
  >"$out/src-composer-json-lines.txt" 2>&1
note "captured src-composer-json-lines"

# --- Logs (section 8.2): a short, non-following sample ----------------------
run compose-logs-sample     docker compose logs --tail 20 --timestamps

note ""
note "Done. Review $out before sharing — then copy it into the lazymage repo under"
note "docs/ground-truth/raw/ (gitignored) or send the files over."
