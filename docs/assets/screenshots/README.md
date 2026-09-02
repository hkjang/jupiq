# 화면 캡처 배치 규칙

프런트엔드 완성 후 [`manifest.json`](manifest.json)의 모든 항목을 데모 데이터로 캡처합니다. 홍보 페이지는 WebP 파일이 아직 없어도 대체 UI를 표시하므로 같은 경로에 이미지를 추가하면 HTML 수정 없이 실제 화면이 나타납니다.

갤러리 최상단 우선순위:

1. `realtime-usage.webp` — 실시간 사용자·서버·CPU/GPU 현황
2. `data-freshness.webp` — 망별 마지막 수집 시각과 Fresh/Stale/Degraded
3. `usage-drilldown.webp` — 일·주·월 통계와 사용자→망→세션 드릴다운

필수 캡처 범위에는 로그인, 모든 라우트, GPU/승인 기능 ON·OFF, 프로필 컨텍스트 메뉴, 404, 모바일 핵심 화면이 포함됩니다. `manifest.json`의 `condition`을 먼저 설정하고 `viewport`별로 캡처하세요.

권장 규격:

- Desktop: 1600×1000, WebP 품질 82
- Mobile: 390×844, WebP 품질 82
- 고정된 demo clock/seed를 사용해 재현 가능하게 생성
- 개인정보, 실제 token, 내부 hostname·IP, 조직명이 남지 않도록 확인
- skeleton이 아닌 API 로딩 완료 상태에서 캡처
- Stale/Degraded는 기준 시각과 배지가 보이게 캡처

## 자동 캡처

운영 데이터나 실제 계정이 아닌 별도의 로컬 데모 DB를 사용합니다. 앱을 `127.0.0.1:8080`에 실행한 뒤 다음처럼 캡처합니다. 비밀번호는 명령행 인자 대신 환경변수로 전달하며, 이미지에는 빈 로그인 폼과 마스킹된 설정만 남습니다.

```bash
export JUPIQ_CAPTURE_BASE_URL=http://127.0.0.1:8080
export JUPIQ_CAPTURE_USERNAME=admin
export JUPIQ_CAPTURE_PASSWORD='로컬-데모-비밀번호'
scripts/capture-screenshots.sh
unset JUPIQ_CAPTURE_PASSWORD
```

스크립트는 캐시된 `mcr.microsoft.com/playwright:v1.62.1-noble` 브라우저 이미지를 사용합니다. Node Playwright 패키지는 `/tmp/jupiq-playwright-1.62.1`에 한 번 준비하며 브라우저 바이너리를 다시 내려받지 않습니다. 첫 준비 시 npm 캐시 또는 인터넷 연결이 필요할 수 있습니다.

캡처 중 GPU·LLM 사용량·승인 메뉴를 임시 활성화하고 완료 후 원래 관리자 설정으로 되돌립니다. 실제 API에 목록 데이터가 있으면 그 데이터를 사용하고, 비어 있으면 `example.invalid` 주소와 `user01` 같은 비식별 샘플로 표를 채웁니다. Fresh/Stale/Degraded와 AI 스트리밍 답변에도 고정 데모 메타데이터를 사용합니다. Chromium DevTools Protocol이 품질 82의 WebP를 직접 생성하며, 각 파일의 RIFF/WEBP 시그니처도 확인합니다.

일부 파일만 다시 만들 수 있습니다.

```bash
scripts/capture-screenshots.sh --only realtime-usage.webp,dashboard-mobile.webp
```

localhost가 아닌 데모 서버는 우발적인 운영 화면 캡처를 막기 위해 기본 거부됩니다. 반드시 비식별 데모 환경임을 확인한 경우에만 `--allow-nonlocal`을 추가합니다.
