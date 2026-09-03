#!/usr/bin/env bash
set -Eeuo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
version="$(tr -d '[:space:]' < "${repo_root}/VERSION")"
image="jupiq:v${version}"
postgres_image="postgres:16-alpine@sha256:cf78e76683b9ca8c5733cbbdce6c9262b45b6767934dd0a95e671f9a0fc20685"
network="jupiq-offline-test-${RANDOM}"
database="jupiq-postgres-test-${RANDOM}"
application="jupiq-app-test-${RANDOM}"
database_password="$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')"
bootstrap_password="$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')"
encryption_key="$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')"

cleanup() {
  docker rm -f "${application}" "${database}" >/dev/null 2>&1 || true
  docker network rm "${network}" >/dev/null 2>&1 || true
}
trap cleanup EXIT

fail() {
  docker logs "${application}" >&2 2>/dev/null || true
  echo "오프라인 기동 검증 실패: $*" >&2
  exit 1
}

docker image inspect "${image}" >/dev/null
docker image inspect "${postgres_image}" >/dev/null || {
  echo "검증용 PostgreSQL 16 이미지가 필요합니다. 온라인 환경에서 지정 digest를 미리 pull한 뒤 실행하세요." >&2
  exit 1
}

# --internal 네트워크로 서비스의 외부 통신을 차단해 오프라인 기동을 검증합니다.
docker network create --internal "${network}" >/dev/null
docker run -d \
  --name "${database}" \
  --network "${network}" \
  --network-alias postgres \
  -e POSTGRES_DB=jupiq \
  -e POSTGRES_USER=jupiq \
  -e "POSTGRES_PASSWORD=${database_password}" \
  "${postgres_image}" >/dev/null

for _ in $(seq 1 30); do
  if docker exec "${database}" pg_isready -h 127.0.0.1 -U jupiq -d jupiq >/dev/null 2>&1; then
    break
  fi
  sleep 1
done
docker exec "${database}" pg_isready -h 127.0.0.1 -U jupiq -d jupiq >/dev/null

docker run -d \
  --name "${application}" \
  --network "${network}" \
  --init \
  --read-only \
  --tmpfs /tmp:size=64m,mode=1777 \
  --cap-drop ALL \
  --security-opt no-new-privileges:true \
  -e "POSTGRES_DSN=postgres://jupiq:${database_password}@postgres:5432/jupiq?sslmode=disable" \
  -e "BOOTSTRAP_ADMIN=admin" \
  -e "BOOTSTRAP_ADMIN_PASSWORD=${bootstrap_password}" \
  -e "ENCRYPTION_KEY=${encryption_key}" \
  "${image}" >/dev/null

for _ in $(seq 1 60); do
  if docker exec "${application}" wget -q -T 2 -O /dev/null http://127.0.0.1:8080/readyz 2>/dev/null; then
    break
  fi
  sleep 1
done

docker exec "${application}" wget -q -T 3 -O /dev/null http://127.0.0.1:8080/readyz 2>/dev/null \
  || fail "/readyz가 준비 상태가 되지 않았습니다"
docker exec "${application}" wget -q -T 3 -O /dev/null http://127.0.0.1:8080/healthz 2>/dev/null \
  || fail "/healthz 확인에 실패했습니다"

version_payload="$(docker exec "${application}" wget -q -T 3 -O - http://127.0.0.1:8080/api/v1/version 2>/dev/null)" \
  || fail "버전 API 호출에 실패했습니다"
if ! grep -Fq "\"version\":\"${version}\"" <<<"${version_payload}"; then
  fail "버전 API가 이미지 버전 ${version}을 반환하지 않았습니다"
fi

openapi_payload="$(docker exec "${application}" wget -q -T 3 -O - http://127.0.0.1:8080/api/v1/openapi.yaml 2>/dev/null)" \
  || fail "OpenAPI 문서를 읽지 못했습니다"
if ! grep -Fq "version: ${version}" <<<"${openapi_payload}"; then
  fail "OpenAPI 버전이 이미지 버전 ${version}과 일치하지 않습니다"
fi

spa_payload="$(docker exec "${application}" wget -q -T 3 -O - http://127.0.0.1:8080/login 2>/dev/null)" \
  || fail "SPA 로그인 경로를 읽지 못했습니다"
if ! grep -Fq '<div id="root"></div>' <<<"${spa_payload}"; then
  fail "SPA root가 로그인 문서에 없습니다"
fi
spa_asset="$(grep -Eo '/assets/[^"[:space:]]+\.js' <<<"${spa_payload}" | head -n 1)"
if [[ -z "${spa_asset}" ]]; then
  fail "SPA JavaScript 자산 경로를 찾지 못했습니다"
fi
docker exec "${application}" wget -q -T 3 -O /dev/null "http://127.0.0.1:8080${spa_asset}" 2>/dev/null \
  || fail "SPA JavaScript 자산을 읽지 못했습니다"

# Cookie와 비밀번호가 host 로그나 명령 출력에 노출되지 않도록 인증 검증은
# 애플리케이션 컨테이너 내부의 임시 파일에서 한 번에 수행합니다.
if ! docker exec \
  -e "JUPIQ_SMOKE_PASSWORD=${bootstrap_password}" \
  -e "JUPIQ_SMOKE_VERSION=${version}" \
  "${application}" sh -eu -c '
    login_headers="$(wget -S -T 5 -O /tmp/login.json \
      --header "Content-Type: application/json" \
      --post-data "{\"username\":\"admin\",\"password\":\"${JUPIQ_SMOKE_PASSWORD}\"}" \
      http://127.0.0.1:8080/api/v1/auth/login 2>&1)"
    cookie="$(printf "%s\n" "${login_headers}" | sed -n "s/^[[:space:]]*[Ss]et-[Cc]ookie:[[:space:]]*\([^;]*\).*/\1/p" | head -n 1)"
    case "${cookie}" in jupiq_session=*) ;; *) exit 11 ;; esac
    grep -Fq "\"username\":\"admin\"" /tmp/login.json

    wget -q -T 5 -O /tmp/dashboard.json --header "Cookie: ${cookie}" \
      http://127.0.0.1:8080/api/v1/dashboard
    grep -Fq "\"summary\"" /tmp/dashboard.json

    wget -q -T 5 -O /tmp/mcp.json \
      --header "Cookie: ${cookie}" \
      --header "Content-Type: application/json" \
      --header "Accept: application/json" \
      --post-data "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{\"protocolVersion\":\"2025-03-26\",\"capabilities\":{},\"clientInfo\":{\"name\":\"offline-smoke\",\"version\":\"1\"}}}" \
      http://127.0.0.1:8080/mcp
    grep -Fq "\"protocolVersion\":\"2025-03-26\"" /tmp/mcp.json
    grep -Fq "\"version\":\"${JUPIQ_SMOKE_VERSION}\"" /tmp/mcp.json
    rm -f /tmp/login.json /tmp/dashboard.json /tmp/mcp.json
  '; then
  fail "Bootstrap 로그인, 대시보드 또는 MCP initialize 검증에 실패했습니다"
fi

echo "오프라인 전체 기동 검증 완료: ${image} (health/ready/version/SPA/OpenAPI/login/dashboard/MCP)"
