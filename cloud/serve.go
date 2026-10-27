package main

import (
	"log/slog"

	"github.com/ItsKaiiiiiiii/gs-evo/cloud/api"
	"github.com/ItsKaiiiiiiii/gs-evo/cloud/store"
	"github.com/ItsKaiiiiiiii/gs-evo/pkg/proto"
)

// main → internal 桥接（Go 不允许 main 直接 import 同 module 的 internal？可以，但
// 保持 server/store 构造集中在此，便于 e2e 复用同一装配路径）。

func openStore(dsn string) (*store.Store, error) { return store.Open(dsn) }

func newAPIServer(cfg api.Config, st *store.Store, log *slog.Logger) (*api.Server, error) {
	return api.NewServer(cfg, st, log)
}

func newAdminToken() string          { return proto.NewAdminToken() }
func hashAdminToken(t string) string { return proto.HashToken(t) }
