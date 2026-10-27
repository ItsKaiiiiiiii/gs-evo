package e2e

import (
	"context"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/ItsKaiiiiiiii/gs-evo/client/worker"
)

// e2e 小工具：补齐 e2e_test.go 引用的辅助函数。

func contextWithCancel() (context.Context, context.CancelFunc) {
	return context.WithCancel(context.Background())
}

// cfgRoots 读取 device home 的白名单目录。
func (d *device) cfgRoots(t *testing.T) []string {
	t.Helper()
	cfg, err := d.home.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Roots
}

func readTokenFromConfig(t *testing.T, home worker.Home) string {
	t.Helper()
	cfg, err := home.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Token
}

// wsDialStatus 只关心 WS 升级的 HTTP 响应码。
func wsDialStatus(t *testing.T, req *http.Request) int {
	t.Helper()
	u := *req.URL
	u.Scheme = "ws"
	h := http.Header{}
	for k, vs := range req.Header {
		h[k] = vs
	}
	d := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	ws, resp, _ := d.Dial(u.String(), h)
	if ws != nil {
		_ = ws.Close()
	}
	if resp != nil {
		return resp.StatusCode
	}
	return 0 // 网络层失败（非 HTTP 拒绝）
}

func clientRollback(home worker.Home, name string) error {
	eng, err := worker.NewEngine(home, slog.New(slog.NewTextHandler(debugWriter(), nil)))
	if err == nil {
		err = eng.Rollback(name)
	}
	return err
}
