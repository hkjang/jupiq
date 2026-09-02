#!/usr/bin/env bash
set -Eeuo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
version="${1:-$(tr -d '[:space:]' < "${repo_root}/VERSION")}"
version="${version#v}"

if [[ ! "${version}" =~ ^[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ ]]; then
  echo "유효하지 않은 VERSION: ${version}" >&2
  exit 1
fi

image="jupiq:v${version}"
archive_dir="${repo_root}/dist"
archive="${archive_dir}/jupiq-v${version}.tar.gz"
temporary="${archive}.tmp"

docker image inspect "${image}" >/dev/null
mkdir -p "${archive_dir}"
trap 'rm -f "${temporary}"' EXIT

echo "오프라인 이미지 저장: ${archive}"
docker save "${image}" | gzip -n -9 > "${temporary}"
gzip -t "${temporary}"
mv "${temporary}" "${archive}"
trap - EXIT

echo "완료: ${archive} ($(du -h "${archive}" | awk '{print $1}'))"
