SHELL := /usr/bin/env bash
.DEFAULT_GOAL := help

VERSION := $(shell tr -d '[:space:]' < VERSION)
IMAGE := jupiq:v$(VERSION)
ARCHIVE := dist/jupiq-v$(VERSION).tar.gz

.PHONY: help deps check-version check-screenshots lint test test-integration release-check build image package verify compose-up compose-down clean

help: ## 사용 가능한 명령을 표시합니다.
	@awk 'BEGIN {FS = ":.*## "; printf "jupiq %s\n\n", "$(VERSION)"} /^[a-zA-Z_-]+:.*## / {printf "  %-17s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

deps: ## Go와 프런트엔드 의존성을 설치합니다.
	go mod download
	cd web && npm ci

check-version: ## VERSION과 코드·문서·배포 파일의 버전을 검사합니다.
	./scripts/check-version.sh

check-screenshots: ## manifest·WebP·갤러리·핵심 경로 캡처를 검사합니다.
	node scripts/check-screenshots.mjs

lint: check-version check-screenshots ## 버전·스크린샷과 정적 검사를 실행합니다.
	go vet ./...
	cd web && npm run lint

test: ## 백엔드와 프런트엔드 테스트를 실행합니다.
	go test ./...
	cd web && npm test

# `go test`는 DSN이 없으면 통합 테스트를 조용히 skip하므로, 이 타깃은 DSN이 없을 때
# 통과처럼 보이지 않도록 안내를 출력하고 실패합니다.
test-integration: ## JUPIQ_INTEGRATION_TEST_DSN의 PostgreSQL로 store·api 통합 테스트를 실행합니다.
	@if [[ -z "$${JUPIQ_INTEGRATION_TEST_DSN:-}" ]]; then \
		echo "JUPIQ_INTEGRATION_TEST_DSN 없음: 통합 테스트를 실행하려면 PostgreSQL이 필요합니다." >&2; \
		echo "  예: docker run -d --name jupiq-it -e POSTGRES_DB=jupiq_test -e POSTGRES_USER=jupiq -e POSTGRES_PASSWORD=it -p 5432:5432 postgres:16-alpine" >&2; \
		echo "      export JUPIQ_INTEGRATION_TEST_DSN='postgres://jupiq:it@127.0.0.1:5432/jupiq_test?sslmode=disable'" >&2; \
		exit 1; \
	fi
	go test -count=1 -p=1 -run Integration ./internal/store ./internal/api

# release.yml의 "소스 검사와 테스트" 단계와 같은 명령을 같은 순서로 실행합니다.
# 첫 실패에서 멈추며, govulncheck는 취약점 DB 조회를 위해 네트워크가 필요합니다.
release-check: ## 릴리스 Workflow의 소스 검사·테스트 단계를 로컬에서 그대로 재현합니다.
	go mod verify
	go vet ./...
	@if command -v govulncheck >/dev/null 2>&1; then \
		govulncheck ./...; \
	else \
		go run golang.org/x/vuln/cmd/govulncheck@v1.7.0 ./...; \
	fi
	go test ./...
	$(MAKE) test-integration
	node scripts/check-screenshots.mjs
	set -euo pipefail; \
		log="$$(mktemp "$${TMPDIR:-/tmp}/jupiq-npm-ci.XXXXXX")"; \
		cd web; \
		npm ci 2>&1 | tee "$${log}"; \
		NPM_CI_LOG="$${log}" bash ../scripts/npm-audit-retry.sh high; \
		npm run lint; \
		npm test; \
		npm run build
	@echo "release-check OK"

build: ## 프런트엔드와 Go 실행파일을 빌드합니다.
	cd web && npm run build
	mkdir -p bin
	CGO_ENABLED=0 go build -trimpath \
		-ldflags="-s -w -X github.com/hkjang/jupiq/internal/version.Version=$(VERSION)" \
		-o bin/jupiq ./cmd/jupiq

image: ## jupiq:vVERSION 이미지를 빌드합니다.
	./scripts/build-image.sh "$(VERSION)"

package: image ## 오프라인 반입용 tar.gz를 만듭니다.
	./scripts/package-offline.sh "$(VERSION)"

verify: ## tar.gz 무결성과 이미지 태그를 검증합니다.
	./scripts/verify-offline.sh "$(ARCHIVE)"

compose-up: ## .env의 네 환경변수로 서비스를 시작합니다.
	docker compose --env-file .env -f compose.example.yaml up -d

compose-down: ## 예제 Compose 서비스를 종료합니다.
	docker compose --env-file .env -f compose.example.yaml down

clean: ## 로컬 빌드 산출물을 제거합니다.
	rm -rf bin dist web/dist
