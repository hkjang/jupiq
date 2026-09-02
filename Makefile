SHELL := /usr/bin/env bash
.DEFAULT_GOAL := help

VERSION := $(shell tr -d '[:space:]' < VERSION)
IMAGE := jupiq:v$(VERSION)
ARCHIVE := dist/jupiq-v$(VERSION).tar.gz

.PHONY: help deps lint test build image package verify compose-up compose-down clean

help: ## 사용 가능한 명령을 표시합니다.
	@awk 'BEGIN {FS = ":.*## "; printf "jupiq %s\n\n", "$(VERSION)"} /^[a-zA-Z_-]+:.*## / {printf "  %-14s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

deps: ## Go와 프런트엔드 의존성을 설치합니다.
	go mod download
	cd web && npm ci

lint: ## 정적 검사를 실행합니다.
	go vet ./...
	cd web && npm run lint

test: ## 백엔드와 프런트엔드 테스트를 실행합니다.
	go test ./...
	cd web && npm test

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
