#!/usr/bin/env bash
set -Eeuo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
version="$(tr -d '[:space:]' < "${repo_root}/VERSION")"
expected_image="jupiq:v${version}"
archive="${1:-${repo_root}/dist/jupiq-v${version}.tar.gz}"

if [[ "$(basename "${archive}")" != "jupiq-v${version}.tar.gz" ]]; then
  echo "파일명이 릴리스 규칙과 다릅니다: $(basename "${archive}")" >&2
  echo "예상: jupiq-v${version}.tar.gz" >&2
  exit 1
fi
if [[ ! -s "${archive}" ]]; then
  echo "아카이브가 없거나 비어 있습니다: ${archive}" >&2
  exit 1
fi

gzip -t "${archive}"
load_output="$(gzip -dc "${archive}" | docker load)"
printf '%s\n' "${load_output}"
docker image inspect "${expected_image}" >/dev/null

actual_version="$(docker image inspect --format '{{ index .Config.Labels "org.opencontainers.image.version" }}' "${expected_image}")"
if [[ "${actual_version}" != "${version}" ]]; then
  echo "OCI 버전 라벨 불일치: ${actual_version} (예상 ${version})" >&2
  exit 1
fi

echo "검증 완료: ${expected_image}"
