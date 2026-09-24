-- 사내 SMTP 릴레이로 나간 알림 메일의 발송 기록. 시도마다 한 행이며 성공과
-- 실패를 모두 남겨 "안 왔다"는 문의에 답할 수 있게 한다. 본문은 담지 않는다 —
-- 제목과 수신자면 충분하고, 본문까지 담으면 기록이 유출 경로가 된다.
CREATE TABLE IF NOT EXISTS mail_deliveries (
    id bigserial PRIMARY KEY,
    event text NOT NULL,
    recipient text NOT NULL,
    subject text NOT NULL,
    -- 같은 대상의 기록을 묶어 보는 표시(approval:12, hub:3, api_key:7).
    reference text NOT NULL DEFAULT '',
    -- 알림을 일으킨 사람. 사용자가 지워져도 기록은 남는다.
    actor_user_id bigint REFERENCES users(id) ON DELETE SET NULL,
    status text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','sent','failed')),
    attempts integer NOT NULL DEFAULT 0,
    error_message text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS mail_deliveries_created_idx ON mail_deliveries(created_at DESC);

-- 만료 임박 안내를 키마다 한 번만 보내기 위한 표시. 발송 기록을 뒤지는 대신
-- 키 행에 두어 기록이 정리된 뒤에도 같은 키에 다시 보내지 않는다.
ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS expiry_notified_at timestamptz;
