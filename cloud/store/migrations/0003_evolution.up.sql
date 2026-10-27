-- P3 自进化闭环 + P4 运营化（§7.5、§8）
CREATE TABLE benchmark_set (
  id         VARCHAR(64) PRIMARY KEY,
  name       VARCHAR(255) NOT NULL,
  version    INT NOT NULL DEFAULT 1,
  cases      JSON NOT NULL,            -- [{name,prompt,expect_contains,timeout_s}]
  created_by VARCHAR(128) NOT NULL DEFAULT 'admin',
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  UNIQUE KEY uq_bs_name_ver (name, version)
) ENGINE=InnoDB;

CREATE TABLE evaluation_run (
  id               VARCHAR(64) PRIMARY KEY,
  skill_name       VARCHAR(255) NOT NULL,
  skill_version    VARCHAR(64) NOT NULL,
  benchmark_set_id VARCHAR(64) NOT NULL,
  status           VARCHAR(16) NOT NULL DEFAULT 'running', -- running|passed|failed|error
  device_ids       JSON NULL,
  metrics          JSON NULL,   -- {total,passed,pass_rate,per_case[...]}
  verdict          VARCHAR(16) NULL,
  created_by       VARCHAR(128) NOT NULL DEFAULT 'admin',
  created_at       DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  finished_at      DATETIME(3) NULL,
  KEY idx_er (skill_name, skill_version, created_at)
) ENGINE=InnoDB;

-- 进化理由强制留存（§7.5 ③：diff 与理由）
ALTER TABLE skill_version ADD COLUMN rationale TEXT NULL AFTER parent_version;

-- P4 自动发布策略（默认关闭；开启后达标自动 reviewed→stable，D9 闸门的受控豁免）
ALTER TABLE skill ADD COLUMN auto_policy JSON NULL AFTER agent_compat;

-- P4 客户端发布（client.update 的版本登记）
CREATE TABLE client_release (
  version    VARCHAR(64) PRIMARY KEY,
  sha256     CHAR(64) NOT NULL,
  size       BIGINT NOT NULL,
  notes      VARCHAR(512) NOT NULL DEFAULT '',
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
) ENGINE=InnoDB;

-- P4 RBAC 最小版：admin token 角色化（admin=全权，viewer=只读）
ALTER TABLE admin_token ADD COLUMN role VARCHAR(16) NOT NULL DEFAULT 'admin';

-- 评估用途的灰度安装留痕（candidate 受控安装的唯一通道）
ALTER TABLE device_skill_install ADD COLUMN evaluation_run_id VARCHAR(64) NULL AFTER prev_ver_id;
