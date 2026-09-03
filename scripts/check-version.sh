#!/usr/bin/env bash
set -Eeuo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
version="$(tr -d '[:space:]' < "${repo_root}/VERSION")"
errors=()

if [[ ! "${version}" =~ ^[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ ]]; then
  echo "VERSION 형식이 SemVer가 아닙니다: ${version}" >&2
  exit 1
fi

expect_literal() {
  local file="$1"
  local literal="$2"
  if ! grep -Fq -- "${literal}" "${repo_root}/${file}"; then
    errors+=("${file}: '${literal}' 누락")
  fi
}

json_version() {
  awk -F'"' '/^[[:space:]]*"version"[[:space:]]*:/ { print $4; exit }' "$1"
}

package_version="$(json_version "${repo_root}/web/package.json")"
lock_version="$(json_version "${repo_root}/web/package-lock.json")"
openapi_version="$(awk '$1 == "version:" { print $2; exit }' "${repo_root}/openapi/openapi.yaml")"

[[ "${package_version}" == "${version}" ]] || errors+=("web/package.json: ${package_version:-<없음>} (예상 ${version})")
[[ "${lock_version}" == "${version}" ]] || errors+=("web/package-lock.json: ${lock_version:-<없음>} (예상 ${version})")
[[ "${openapi_version}" == "${version}" ]] || errors+=("openapi/openapi.yaml: ${openapi_version:-<없음>} (예상 ${version})")

expect_literal "internal/version/version.go" "Version   = \"${version}-dev\""
expect_literal "compose.example.yaml" "image: jupiq:v${version}"
expect_literal "README.md" "jupiq:v${version}"
expect_literal "README.md" "jupiq-v${version}.tar.gz"
expect_literal "docs/index.html" "\"softwareVersion\": \"${version}\""
expect_literal "docs/install/index.html" "jupiq-v${version}.tar.gz"
expect_literal "docs/install/index.html" "jupiq:v${version}"
expect_literal "docs/releases/index.html" "jupiq-v${version}.tar.gz"
expect_literal "docs/releases/index.html" "jupiq:v${version}"

while IFS= read -r page; do
  relative="${page#${repo_root}/}"
  expect_literal "${relative}" "Version ${version}"
done < <(find "${repo_root}/docs" -mindepth 1 -name index.html -type f | sort)

while IFS= read -r tagged; do
  [[ -z "${tagged}" || "${tagged}" == "jupiq:v${version}" ]] || errors+=("현재 버전과 다른 이미지 태그 발견: ${tagged}")
done < <(grep -Eho 'jupiq:v[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?' \
  "${repo_root}/README.md" "${repo_root}/compose.example.yaml" "${repo_root}"/docs/*.html "${repo_root}"/docs/*/index.html 2>/dev/null | sort -u)

while IFS= read -r archive; do
  [[ -z "${archive}" || "${archive}" == "jupiq-v${version}.tar.gz" ]] || errors+=("현재 버전과 다른 archive 이름 발견: ${archive}")
done < <(grep -Eho 'jupiq-v[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?\.tar\.gz' \
  "${repo_root}/README.md" "${repo_root}"/docs/*.html "${repo_root}"/docs/*/index.html 2>/dev/null | sort -u)

if ((${#errors[@]})); then
  printf '버전 정합성 검사 실패 (%d건):\n' "${#errors[@]}" >&2
  printf ' - %s\n' "${errors[@]}" >&2
  exit 1
fi

echo "버전 정합성 검사 완료: ${version}"
