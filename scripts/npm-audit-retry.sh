#!/usr/bin/env bash
# npm audit against the registry's quick-audit endpoint, retried when that
# endpoint itself is unavailable. A real vulnerability finding still fails on
# the first attempt; only endpoint errors (503 / "audit endpoint returned an
# error") are retried, and each attempt is bounded so an unresponsive endpoint
# cannot hang the job for minutes the way an unbounded call did.
set -uo pipefail

level="${1:-high}"
attempts="${NPM_AUDIT_ATTEMPTS:-4}"
per_attempt_timeout="${NPM_AUDIT_TIMEOUT_SECONDS:-90}"

for attempt in $(seq 1 "${attempts}"); do
  output="$(timeout "${per_attempt_timeout}" npm audit --audit-level="${level}" 2>&1)"
  status=$?
  printf '%s\n' "${output}"
  if [[ ${status} -eq 0 ]]; then
    exit 0
  fi
  if [[ ${status} -ne 124 ]] && ! grep -qiE 'audit endpoint returned an error|Service Unavailable|ECONNRESET|ETIMEDOUT|EAI_AGAIN|503' <<<"${output}"; then
    echo "npm audit: 취약점이 보고되어 실패합니다 (exit ${status})." >&2
    exit "${status}"
  fi
  if [[ ${attempt} -lt ${attempts} ]]; then
    wait_seconds=$((attempt * 20))
    echo "npm audit: registry audit endpoint에 접근하지 못했습니다 (attempt ${attempt}/${attempts}, exit ${status}). ${wait_seconds}초 후 재시도합니다." >&2
    sleep "${wait_seconds}"
  fi
done

echo "npm audit: registry audit endpoint가 ${attempts}회 모두 응답하지 않았습니다." >&2

# Outage fallback. `npm ci` on this same job already ran npm's own advisory
# scan (bulk endpoint) and printed its verdict; that log is handed in via
# NPM_CI_LOG. The step passes only when that verdict is exactly zero
# vulnerabilities, and it says so loudly in the job summary. Any reported
# vulnerability - from npm ci or from an audit attempt above - still fails.
ci_log="${NPM_CI_LOG:-}"
if [[ -n "${ci_log}" && -r "${ci_log}" ]] && grep -qE '^found 0 vulnerabilities' "${ci_log}" \
   && ! grep -qiE '[1-9][0-9]* (low|moderate|high|critical)' "${ci_log}"; then
  note="npm audit endpoint 장애로 quick-audit을 완료하지 못했지만, 같은 job의 npm ci 취약점 점검은 'found 0 vulnerabilities'였습니다. 이 결과를 근거로 통과합니다."
  echo "::warning title=npm audit endpoint unavailable::${note}"
  [[ -n "${GITHUB_STEP_SUMMARY:-}" ]] && printf '## ⚠ npm audit endpoint 장애\n\n%s\n' "${note}" >> "${GITHUB_STEP_SUMMARY}"
  echo "npm audit: ${note}" >&2
  exit 0
fi
echo "npm audit: npm ci 점검 결과를 확인할 수 없어 실패합니다 (NPM_CI_LOG='${ci_log}')." >&2
exit 1
