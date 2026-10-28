-- 运行时 v2：专属 workspace（client 目录下），生命周期管理
ALTER TABLE runtime ADD COLUMN workspace VARCHAR(512) NOT NULL DEFAULT '' AFTER path;
