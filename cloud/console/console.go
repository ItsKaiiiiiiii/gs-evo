// Package console 内嵌 Web 控制台：静态资源 + 路由挂载。
// 设计原则：控制台是纯前端（调用既有 /api/v1/admin/* REST，Bearer 鉴权），
// 后端零新增逻辑；页面按模块注册，扩展 = static/js/ 下加一个模块文件（§4.5 运营化）。
package console

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static
var staticFS embed.FS

// Handler 返回控制台静态资源处理器（挂载在 /console/ 下）。
// 静态资源本身不做鉴权——所有数据操作都在 API 层走 Bearer token。
func Handler() http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err) // embed 内容编译期保证存在
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.StripPrefix("/console/", fileServer)
}

// Redirect 把根路径引到控制台。
func Redirect(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/console/", http.StatusFound)
}
