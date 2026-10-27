-- P2 任务通道：任务表 + 事件表（§8；events 也落库便于查询/回放审计）
CREATE TABLE task (
  id             VARCHAR(64) PRIMARY KEY,
  device_id      VARCHAR(64) NOT NULL,
  prompt         TEXT NOT NULL,
  agent_type     VARCHAR(64) NOT NULL DEFAULT '',
  max_permission VARCHAR(16) NOT NULL DEFAULT '',
  status         VARCHAR(16) NOT NULL DEFAULT 'queued', -- queued|running|completed|failed|cancelled|timeout|expired
  created_by     VARCHAR(128) NOT NULL DEFAULT 'admin',
  cmd_id         VARCHAR(64) NULL,
  result         JSON NULL,
  created_at     DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  finished_at    DATETIME(3) NULL,
  KEY idx_task_device (device_id, created_at)
) ENGINE=InnoDB;

CREATE TABLE task_event (
  id         BIGINT AUTO_INCREMENT PRIMARY KEY,
  task_id    VARCHAR(64) NOT NULL,
  seq        BIGINT NOT NULL,
  kind       VARCHAR(16) NOT NULL,
  data       JSON NULL,
  ts         DATETIME(3) NOT NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  UNIQUE KEY uq_task_event (task_id, seq)
) ENGINE=InnoDB;
