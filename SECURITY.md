# jupiq 보안 정책

## 지원 범위

보안 수정은 원칙적으로 GitHub Releases에 게시된 최신 안정 버전에 제공합니다. 오프라인망 운영자는 현재 버전과 직전 승인 이미지를 보관하고, 새 릴리스의 SHA-256·migration·알려진 제한을 검토한 뒤 반입하세요.

## 취약점 제보

민감한 보안 문제를 공개 Issue에 등록하지 마세요. 저장소의 **Security → Report a vulnerability**가 제공되면 GitHub 비공개 보안 권고로 제보하고, 사용할 수 없다면 저장소 소유자와 합의한 비공개 채널로 먼저 연락하세요.

다음 정보를 비밀값을 제거한 상태로 포함해 주세요.

- 영향받는 jupiq 버전과 Docker 이미지 revision
- 재현 조건, 예상 동작과 실제 동작
- 영향 범위와 가능한 완화 조치
- 필요한 경우 최소 재현 요청·응답. 실제 token, 비밀번호, 내부 URL, 개인정보, Notebook 또는 프롬프트 본문은 제외

제보를 공개하거나 제3자에게 전달하기 전에 maintainer와 공개 일정을 조율해 주세요.

## 즉시 대응이 필요한 경우

API key, Hub token, OIDC client secret 또는 `ENCRYPTION_KEY` 노출이 의심되면 관련 접근을 먼저 차단하고 다음 순서로 대응하세요.

1. 노출된 개인 API key와 외부 연동 credential을 폐기·회전합니다.
2. 영향 시간대의 감사로그와 reverse proxy 로그를 보존합니다.
3. `ENCRYPTION_KEY`가 노출된 경우 DB에 저장된 모든 연동 secret을 유출된 것으로 간주하고 다시 입력합니다.
4. 승인된 새 이미지로 교체하기 전까지 영향 endpoint를 제한합니다.

## 릴리스 검증

GitHub Release에는 오프라인 반입용 `jupiq-vX.Y.Z.tar.gz` 하나만 첨부합니다. Release 본문에 게시된 SHA-256을 연결망에서 확인하고 그 값을 별도 승인 기록으로 오프라인망에 함께 반입하세요.

```bash
echo "릴리스-본문의-SHA256  jupiq-vX.Y.Z.tar.gz" | sha256sum -c -
gzip -t jupiq-vX.Y.Z.tar.gz
gzip -dc jupiq-vX.Y.Z.tar.gz | docker load
docker image inspect jupiq:vX.Y.Z
```

운영 보안 강화와 키 백업 절차는 [보안 설계](https://hkjang.github.io/jupiq/security/) 및 [오프라인망 운영 가이드](https://hkjang.github.io/jupiq/offline/)를 참고하세요.
