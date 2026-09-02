#!/usr/bin/env bash
set -Eeuo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
version="$(tr -d '[:space:]' < "${repo_root}/VERSION")"
image="jupiq:v${version}"
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

docker image inspect "${image}" >/dev/null
docker image inspect postgres:16-alpine >/dev/null || {
  echo "postgres:16-alpine 이미지가 필요합니다. 온라인 환경에서 미리 pull한 뒤 실행하세요." >&2
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
  postgres:16-alpine >/dev/null

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
  -e "POSTGRES_DSN=postgres://jupiq:${database_password}@postgres:5432/jupiq?sslmode=disable" \
  -e "BOOTSTRAP_ADMIN=admin" \
  -e "BOOTSTRAP_ADMIN_PASSWORD=${bootstrap_password}" \
  -e "ENCRYPTION_KEY=${encryption_key}" \
  "${image}" >/dev/null

for _ in $(seq 1 60); do
  if docker exec "${application}" wget -q -T 2 -O /dev/null http://127.0.0.1:8080/readyz 2>/dev/null; then
    echo "오프라인 기동 검증 완료: ${image}"
    exit 0
  fi
  sleep 1
done

docker logs "${application}" >&2
echo "오프라인 기동 검증 실패" >&2
exit 1
