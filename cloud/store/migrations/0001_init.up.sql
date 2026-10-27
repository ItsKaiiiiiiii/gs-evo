-- gs-evo 初始 schema（§8 数据模型）
CREATE TABLE device (
  id                 VARCHAR(64) PRIMARY KEY,
  owner              VARCHAR(128) NOT NULL DEFAULT 'admin',
  token_hash         CHAR(64) NOT NULL,
  token_expires_at   DATETIME(3) NOT NULL,
  hostname           VARCHAR(255) NOT NULL DEFAULT '',
  os                 VARCHAR(32)  NOT NULL DEFAULT '',
  arch               VARCHAR(32)  NOT NULL DEFAULT '',
  worker_version     VARCHAR(64)  NOT NULL DEFAULT '',
  labels             JSON NULL,
  next_cmd_seq       BIGINT NOT NULL DEFAULT 0,
  last_acked_cmd_seq BIGINT NOT NULL DEFAULT 0,
  agents             JSON NULL,
  last_seen          DATETIME(3) NULL,
  status             VARCHAR(16) NOT NULL DEFAULT 'offline',
  created_at         DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  UNIQUE KEY uq_device_token (token_hash)
) ENGINE=InnoDB;

CREATE TABLE enroll_token (
  id         BIGINT AUTO_INCREMENT PRIMARY KEY,
  token_hash CHAR(64) NOT NULL,
  label      VARCHAR(255) NOT NULL DEFAULT '',
  created_by VARCHAR(128) NOT NULL DEFAULT 'admin',
  expires_at DATETIME(3) NOT NULL,
  used_at    DATETIME(3) NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  UNIQUE KEY uq_enroll_hash (token_hash)
) ENGINE=InnoDB;

CREATE TABLE admin_token (
  token_hash CHAR(64) PRIMARY KEY,
  label      VARCHAR(255) NOT NULL DEFAULT '',
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
) ENGINE=InnoDB;

CREATE TABLE command (
  id         VARCHAR(64) PRIMARY KEY,
  cmd_seq    BIGINT NOT NULL,
  device_id  VARCHAR(64) NOT NULL,
  cmd_type   VARCHAR(64) NOT NULL,
  payload    JSON NOT NULL,
  status     VARCHAR(16) NOT NULL DEFAULT 'pending',
  created_by VARCHAR(128) NOT NULL DEFAULT 'admin',
  result     JSON NULL,
  issued_at  DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  expires_at DATETIME(3) NULL,
  acked_at   DATETIME(3) NULL,
  result_at  DATETIME(3) NULL,
  KEY idx_cmd_device (device_id, cmd_seq),
  KEY idx_cmd_status (device_id, status)
) ENGINE=InnoDB;

CREATE TABLE skill (
  id            BINARY(16) PRIMARY KEY,
  name          VARCHAR(255) NOT NULL,
  latest_stable VARCHAR(64) NULL,
  latest_any    VARCHAR(64) NULL,
  agent_compat  JSON NULL,
  created_at    DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  UNIQUE KEY uq_skill_name (name)
) ENGINE=InnoDB;

CREATE TABLE skill_version (
  id             BINARY(16) PRIMARY KEY,
  skill_id       BINARY(16) NOT NULL,
  version        VARCHAR(64) NOT NULL,
  manifest       JSON NOT NULL,
  content_sha256 CHAR(64) NOT NULL,
  size           BIGINT NOT NULL DEFAULT 0,
  signature      VARCHAR(256) NULL,
  origin_device  VARCHAR(64) NULL,
  source         VARCHAR(16) NOT NULL DEFAULT 'collected',
  parent_version VARCHAR(64) NULL,
  status         VARCHAR(16) NOT NULL DEFAULT 'draft',
  created_at     DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  UNIQUE KEY uq_sv (skill_id, version),
  UNIQUE KEY uq_sv_content (skill_id, content_sha256),
  KEY idx_sv_status (status)
) ENGINE=InnoDB;

CREATE TABLE device_skill_install (
  id           BIGINT AUTO_INCREMENT PRIMARY KEY,
  device_id    VARCHAR(64) NOT NULL,
  skill_ver_id BINARY(16) NOT NULL,
  skill_name   VARCHAR(255) NOT NULL,
  version      VARCHAR(64) NOT NULL,
  result       VARCHAR(32) NOT NULL,
  prev_ver_id  BINARY(16) NULL,
  installed_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  KEY idx_dsi (device_id, skill_name)
) ENGINE=InnoDB;

CREATE TABLE bundle_ticket (
  id         VARCHAR(64) PRIMARY KEY,
  token_hash CHAR(64) NOT NULL,
  device_id  VARCHAR(64) NOT NULL,
  sha256     CHAR(64) NOT NULL,
  action     VARCHAR(16) NOT NULL,
  expires_at DATETIME(3) NOT NULL,
  used_at    DATETIME(3) NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  UNIQUE KEY uq_bt_hash (token_hash),
  KEY idx_bt (sha256, action)
) ENGINE=InnoDB;

CREATE TABLE bundle (
  sha256     CHAR(64) PRIMARY KEY,
  size       BIGINT NOT NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
) ENGINE=InnoDB;
