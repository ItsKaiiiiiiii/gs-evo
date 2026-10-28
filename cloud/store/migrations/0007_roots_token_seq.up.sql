-- 安装目标白名单可视化 + token 生命周期管理
ALTER TABLE device ADD COLUMN roots JSON NULL AFTER labels;

ALTER TABLE admin_token ADD COLUMN seq BIGINT UNSIGNED NOT NULL AUTO_INCREMENT UNIQUE AFTER token_hash;
