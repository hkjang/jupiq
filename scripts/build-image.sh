#!/usr/bin/env bash
set -Eeuo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
version="${1:-$(tr -d '[:space:]' < "${repo_root}/VERSION")}"
version="${version#v}"

if [[ ! "${version}" =~ ^[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ ]]; then
  echo "유효하지 않은 VERSION: ${version}" >&2
  exit 1
fi

if git -C "${repo_root}" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  commit="$(git -C "${repo_root}" rev-parse --short=12 HEAD 2>/dev/null || printf 'unknown')"
else
  commit="unknown"
fi
build_time="${SOURCE_DATE_EPOCH:-$(date -u +%s)}"
build_time="$(date -u -d "@${build_time}" +%Y-%m-%dT%H:%M:%SZ)"
image="jupiq:v${version}"

echo "이미지 빌드: ${image}"
docker build \
  --platform "linux/amd64" \
  --build-arg "VERSION=${version}" \
  --build-arg "COMMIT=${commit}" \
  --build-arg "BUILD_TIME=${build_time}" \
  --tag "${image}" \
  "${repo_root}"

docker image inspect "${image}" >/dev/null
echo "완료: ${image}"
