#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
project_dir="$(cd "${script_dir}/.." && pwd)"
playwright_version="${JUPIQ_CAPTURE_PLAYWRIGHT_VERSION:-1.62.1}"
playwright_image="${JUPIQ_CAPTURE_PLAYWRIGHT_IMAGE:-mcr.microsoft.com/playwright:v${playwright_version}-noble}"
dependency_dir="${JUPIQ_CAPTURE_DEPENDENCY_DIR:-/tmp/jupiq-playwright-${playwright_version}}"

if [[ ! -f "${dependency_dir}/node_modules/playwright/package.json" ]]; then
  mkdir -p "${dependency_dir}"
  npm install --prefix "${dependency_dir}" --ignore-scripts --no-audit --no-fund "playwright@${playwright_version}"
fi

docker run --rm --network host --ipc=host \
  --user "$(id -u):$(id -g)" \
  -e NODE_PATH=/capture-deps/node_modules \
  -e JUPIQ_CAPTURE_BASE_URL \
  -e JUPIQ_CAPTURE_USERNAME \
  -e JUPIQ_CAPTURE_PASSWORD \
  -v "${project_dir}:/work" \
  -v "${dependency_dir}:/capture-deps:ro" \
  -w /work \
  "${playwright_image}" \
  node scripts/capture-screenshots.mjs "$@"
