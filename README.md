# jupiq

[![CI](https://github.com/hkjang/jupiq/actions/workflows/ci.yml/badge.svg)](https://github.com/hkjang/jupiq/actions/workflows/ci.yml)
[![Pages](https://github.com/hkjang/jupiq/actions/workflows/pages.yml/badge.svg)](https://hkjang.github.io/jupiq/)
[![Release](https://img.shields.io/github/v/release/hkjang/jupiq?display_name=tag)](https://github.com/hkjang/jupiq/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-0b1730.svg)](LICENSE)

**jupiq**는 분리된 JupyterHub와 Kubernetes·Prometheus·GPU 인프라를 한 화면에서 운영하는 오프라인망용 AI Workspace Control Plane입니다.

- 최신 JupyterHub snapshot에서 실행 서버가 있는 사용자 수와 자원 사용량을 망·부서·사용자별로 조회
- Fresh·Stale 상태와 마지막 수집 시각 표시
- 일·주·월 이용 통계와 전체 → 망 → 부서 → 사용자 → 세션 드릴다운
- JupyterHub 사용자·서버 통합 관리와 정책·Quota·감사
- Keycloak OIDC, 변경 가능한 RBAC, 서비스/개인 키 회전
- 선택형 GPU·LLM Chat Completions 사용량 모니터링(초기 기본 OFF)
- 스트리밍 AI 운영 분석, REST/OpenAPI와 MCP
- 단일 Docker 이미지 tar.gz로 오프라인망 배포

홍보 사이트와 전체 가이드는 [jupiq GitHub Pages](https://hkjang.github.io/jupiq/)에서 볼 수 있습니다.

## 기술 스택

| 영역 | 기술 |
|---|---|
| Backend | Go |
| Frontend | React + TypeScript + Ant Design + ECharts |
| Database | PostgreSQL |
| 인증 | Keycloak OIDC + Bootstrap 관리자 |
| 연동 | JupyterHub REST, Prometheus, Kubernetes, NVIDIA DCGM |
| API | REST/OpenAPI, MCP Streamable HTTP/JSON-RPC |
| 배포 | 멀티스테이지 Docker, GitHub Actions |

## 빠른 시작: 릴리스 이미지

릴리스의 서비스 이미지는 다음 규칙을 따릅니다.

```text
Docker image   jupiq:v1.1.0
Release asset  jupiq-v1.1.0.tar.gz
```

```bash
# GitHub Release 본문의 64자리 SHA-256을 승인 기록과 대조
JUPIQ_ARCHIVE_SHA256='릴리스-본문의-SHA256'
printf '%s  %s\n' "${JUPIQ_ARCHIVE_SHA256}" jupiq-v1.1.0.tar.gz | sha256sum -c -
gzip -t jupiq-v1.1.0.tar.gz
gzip -dc jupiq-v1.1.0.tar.gz | docker load
# 소스 체크아웃에서는 .env.example을 복사하고 네 값을 안전하게 변경
cp .env.example .env
docker run -d --name jupiq --restart unless-stopped --init \
  --env-file .env -p 127.0.0.1:8080:8080 \
  --read-only --tmpfs /tmp:size=64m,mode=1777 \
  --cap-drop ALL --security-opt no-new-privileges:true \
  jupiq:v1.1.0
curl --fail http://127.0.0.1:8080/readyz
```

이미지 tar.gz만 반입하는 경우 설치 가이드의 네 줄로 `.env`를 직접 만들면 됩니다. Compose를 쓰려면 같은 태그 소스의 예제 파일 두 개를 별도로 반입하세요. GitHub Release에는 PostgreSQL이 아닌 **jupiq 서비스 이미지 tar.gz 하나만** 첨부합니다.

상세 절차: [설치 가이드](https://hkjang.github.io/jupiq/install/) · [오프라인 운영](https://hkjang.github.io/jupiq/offline/)

## 런타임 환경변수

jupiq 프로세스가 읽는 설정 환경변수는 정확히 네 개입니다.

| 이름 | 설명 |
|---|---|
| `POSTGRES_DSN` | PostgreSQL 연결 문자열. 운영은 `sslmode=verify-full` 권장 |
| `BOOTSTRAP_ADMIN` | 최초/긴급 로컬 관리자 ID |
| `BOOTSTRAP_ADMIN_PASSWORD` | Bootstrap 관리자 초기 비밀번호. 첫 로그인 후 변경 |
| `ENCRYPTION_KEY` | AES-256용 정확히 32바이트(raw 32자, 64자 hex 또는 base64) |

```bash
openssl rand -base64 32
```

Keycloak, Prometheus, Kubernetes, AI Provider, Webhook은 관리자 설정에서, JupyterHub는 전용 Hub 등록 Drawer에서 입력합니다. 저장 전에 현재 입력값으로 URL·TLS·응답시간과 연동별 실제 API(예: Hub/Kubernetes 버전·조회 권한, OIDC Discovery, AI 모델 조회)를 검사합니다. 비밀값은 `ENCRYPTION_KEY`로 암호화해 PostgreSQL에 저장합니다.

## 소스 빌드

필요 도구: `go.mod`가 지정한 Go, Node.js 22, npm, Docker.

```bash
make deps
make lint
make test
make build
```

개별 실행:

```bash
# Frontend
cd web
npm ci
npm run dev

# Backend (별도 터미널, 네 환경변수 필요)
go run ./cmd/jupiq
```

Frontend는 same-origin `/api/v1`을 사용합니다. 프로덕션 빌드의 `web/dist`는 Go 서버가 SPA fallback으로 제공합니다.

## 컨테이너와 오프라인 패키지

`VERSION`은 `v`를 제외한 SemVer입니다.

```bash
make image      # jupiq:v$(cat VERSION)
make package    # dist/jupiq-v$(cat VERSION).tar.gz
make verify     # gzip, docker load, OCI version label 검증
```

릴리스 Workflow는 원본 이미지를 삭제하고 archive에서 다시 load한 뒤, 외부 통신이 차단된 Docker `--internal` network에서 PostgreSQL과 `/readyz` Smoke Test를 수행합니다.

## API와 상태 확인

| 경로 | 용도 |
|---|---|
| `GET /healthz` | 프로세스 liveness |
| `GET /readyz` | DB를 포함한 readiness |
| `GET /api/v1/version` | version·commit·build time |
| `GET /api/v1/openapi.yaml` | 배포 버전 OpenAPI |
| `POST /api/v1/ai/chat` | SSE 스트리밍 AI 분석, `max_tokens <= 262144` |
| `POST /mcp`, `POST /api/v1/mcp` | MCP Streamable HTTP/JSON-RPC |

브라우저는 HttpOnly `jupiq_session` cookie를, 자동화는 `Authorization: Bearer <JWT|jqk_API_KEY>`를 사용합니다. 자세한 내용은 [API·MCP 가이드](https://hkjang.github.io/jupiq/api-mcp/)와 [`openapi/`](openapi/)를 참고하세요.

## 선택형 모니터링

GPU와 LLM Chat Completions 모니터링은 초기 **OFF**입니다.

- OFF: 관련 collector·Prometheus query·메뉴·KPI 비활성, 과거 데이터 보존
- GPU ON: Prometheus와 NVIDIA DCGM Exporter, Pod→사용자 매핑 검증 필요
- LLM ON: Gateway/Service Mesh가 Prometheus로 노출한 pod/path/status와 calls counter가 필요하며 token metric은 선택. 정규식 통과 후 현재 서버 인벤토리의 정확한 Pod 이름과 일치한 샘플만 사용자에게 귀속
- Notebook 코드, 프롬프트와 응답 본문은 수집하지 않음
- 선택 collector 실패는 로그인·Hub 관리 등 핵심 서비스에 영향 없음

## 문서

- [기능](https://hkjang.github.io/jupiq/features/)
- [아키텍처](https://hkjang.github.io/jupiq/architecture/)
- [보안](https://hkjang.github.io/jupiq/security/)
- [설치](https://hkjang.github.io/jupiq/install/)
- [오프라인 운영](https://hkjang.github.io/jupiq/offline/)
- [사용자 가이드](https://hkjang.github.io/jupiq/user-guide/)
- [관리자 가이드](https://hkjang.github.io/jupiq/admin-guide/)
- [API·MCP](https://hkjang.github.io/jupiq/api-mcp/)
- [릴리스 안내](https://hkjang.github.io/jupiq/releases/)

## 보안 원칙

- Notebook source, cell, 사용자 파일, AI prompt/response 본문을 수집하지 않습니다.
- Hub token, OIDC client secret, AI key는 평문으로 다시 표시하지 않습니다.
- 위험 작업은 대상·사유 재확인과 감사로그를 남깁니다.
- 팀장 검토/승인은 관리자가 범위별로 설정한 경우에만 활성화되며, 요청자는 자기 요청을 검토·승인·반려할 수 없고 선검토 시 검토자와 승인자도 분리됩니다.

취약점이나 민감한 보안 문제는 공개 Issue에 secret·내부 URL·로그를 첨부하지 마세요. 지원 범위와 비공개 제보 절차는 [보안 정책](SECURITY.md)을 따릅니다.

## 라이선스

[MIT](LICENSE) © 2026 jupiq contributors
