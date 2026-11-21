#!/usr/bin/env bash
# Build a throwaway docker-magento project wired to the fake docker / orb CLIs in
# testdata/fakebin, so lazymage can be exercised without a container engine.
#
#   scripts/demo-project.sh /tmp/lm-demo            # clones markshust/docker-magento
#   scripts/demo-project.sh /tmp/lm-demo ~/src/docker-magento
#
# Then:  export PATH="$(pwd)/testdata/fakebin:$PATH"; cd /tmp/lm-demo && lazymage
set -euo pipefail

dest="${1:?usage: demo-project.sh DEST [docker-magento checkout]}"
src="${2:-}"
here="$(cd "$(dirname "$0")/.." && pwd)"

if [ -z "$src" ]; then
  src="$(mktemp -d)/docker-magento"
  git clone -q --depth 1 https://github.com/markshust/docker-magento "$src"
fi

mkdir -p "$dest"
cp -R "$src/compose/." "$dest/"
cd "$dest"

# The files compose.dev.yaml bind-mounts (bin/start checks they exist).
mkdir -p src/app/code src/app/design src/app/etc src/generated src/var/log src/dev/tools/grunt/configs
for f in composer.json composer.lock grunt-config.json.sample Gruntfile.js.sample nginx.conf.sample package.json.sample; do
  [ -e "src/$f" ] || echo '{}' > "src/$f"
done
cat > src/composer.json <<'JSON'
{"name": "magento/project-community-edition", "require": {"magento/product-community-edition": "2.4.7-p3"}}
JSON
cat > src/composer.lock <<'JSON'
{"packages": [{"name": "magento/magento2-base", "version": "2.4.7-p3"}]}
JSON
cat > src/app/etc/env.php <<'PHP'
<?php
return [
    'backend' => [
        'frontName' => 'admin_demo'
    ],
    'db' => ['connection' => ['default' => ['password' => 'not-a-real-secret']]],
    'MAGE_MODE' => 'developer',
];
PHP
echo "Demo project ready at $dest"
echo "Run:  export PATH=\"$here/testdata/fakebin:\$PATH\" FAKE_DOCKER_STATE=/tmp/fake-docker-state; cd $dest && lazymage"
