-- 运行时（Runtime）：本机 agent 实例一等实体（设备 1→N）
-- 注意：runtime_id=derive(type+path) 仅在单机内唯一（相同机器会派生相同 id），
-- 因此主键必须是 (device_id, id) 复合——否则多机会互相覆盖运行时记录。
CREATE TABLE runtime (
  id         VARCHAR(64) NOT NULL,      -- worker 侧派生的稳定身份 derive(type+path)
  device_id  VARCHAR(64) NOT NULL,
  type       VARCHAR(64) NOT NULL,
  version    VARCHAR(64) NOT NULL DEFAULT '',
  path       VARCHAR(512) NOT NULL DEFAULT '',
  ok         BOOLEAN NOT NULL DEFAULT FALSE,
  source     VARCHAR(16) NOT NULL DEFAULT 'probed', -- probed | registered
  last_seen  DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (device_id, id)
) ENGINE=InnoDB;

ALTER TABLE task ADD COLUMN runtime_id VARCHAR(64) NULL AFTER agent_type;
