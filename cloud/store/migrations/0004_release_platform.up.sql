-- P4 自更新补强：release 按平台（version+os+arch）维度发布，rollout 按设备平台精确匹配
DROP TABLE IF EXISTS client_release;
CREATE TABLE client_release (
  version    VARCHAR(64) NOT NULL,
  os         VARCHAR(32) NOT NULL,
  arch       VARCHAR(64) NOT NULL,
  sha256     CHAR(64) NOT NULL,
  size       BIGINT NOT NULL,
  notes      VARCHAR(512) NOT NULL DEFAULT '',
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (version, os, arch)
) ENGINE=InnoDB;
