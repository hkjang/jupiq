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
if [[ "${load_output}" != "Loaded image: ${expected_image}" ]]; then
  echo "아카이브는 정확히 하나의 예상 태그만 포함해야 합니다." >&2
  echo "예상 load 결과: Loaded image: ${expected_image}" >&2
  exit 1
fi
docker image inspect "${expected_image}" >/dev/null

actual_version="$(docker image inspect --format '{{ index .Config.Labels "org.opencontainers.image.version" }}' "${expected_image}")"
if [[ "${actual_version}" != "${version}" ]]; then
  echo "OCI 버전 라벨 불일치: ${actual_version} (예상 ${version})" >&2
  exit 1
fi

actual_os="$(docker image inspect --format '{{ .Os }}' "${expected_image}")"
actual_architecture="$(docker image inspect --format '{{ .Architecture }}' "${expected_image}")"
if [[ "${actual_os}/${actual_architecture}" != "linux/amd64" ]]; then
  echo "이미지 플랫폼 불일치: ${actual_os}/${actual_architecture} (예상 linux/amd64)" >&2
  exit 1
fi

actual_title="$(docker image inspect --format '{{ index .Config.Labels "org.opencontainers.image.title" }}' "${expected_image}")"
actual_revision="$(docker image inspect --format '{{ index .Config.Labels "org.opencontainers.image.revision" }}' "${expected_image}")"
actual_created="$(docker image inspect --format '{{ index .Config.Labels "org.opencontainers.image.created" }}' "${expected_image}")"
actual_source="$(docker image inspect --format '{{ index .Config.Labels "org.opencontainers.image.source" }}' "${expected_image}")"
if [[ "${actual_title}" != "jupiq" || "${actual_source}" != "https://github.com/hkjang/jupiq" ]]; then
  echo "OCI 식별 라벨이 jupiq 릴리스 계약과 다릅니다." >&2
  exit 1
fi
if git -C "${repo_root}" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  expected_revision="$(git -C "${repo_root}" rev-parse --short=12 HEAD)"
  if [[ "${actual_revision}" != "${expected_revision}" ]]; then
    echo "OCI revision 라벨 불일치: ${actual_revision} (예상 ${expected_revision})" >&2
    exit 1
  fi
elif [[ "${actual_revision}" != "unknown" && ! "${actual_revision}" =~ ^[0-9a-f]{7,40}$ ]]; then
  echo "OCI revision 라벨이 유효하지 않습니다: ${actual_revision}" >&2
  exit 1
fi
if [[ ! "${actual_created}" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$ ]]; then
  echo "OCI created 라벨이 UTC RFC3339 형식이 아닙니다: ${actual_created}" >&2
  exit 1
fi

echo "검증 완료: ${expected_image} (${actual_os}/${actual_architecture}, revision ${actual_revision})"
