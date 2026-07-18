package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

// static 是构建阶段复制 WebUI dist 后的嵌入目录。
//
//go:embed all:static placeholder.html
var files embed.FS

func Handler() http.Handler {
	sub, err := fs.Sub(files, "static")
	if err != nil {
		panic(err)
	}
	staticFiles := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Vue 使用 History 路由；非静态资源路径应由前端入口接管，避免刷新页面时 404。
		if path := strings.TrimPrefix(r.URL.Path, "/"); path == "" || !strings.Contains(path, ".") {
			index, err := fs.ReadFile(sub, "index.html")
			if err != nil {
				index, err = files.ReadFile("placeholder.html")
				if err != nil {
					http.Error(w, "web entry is unavailable", http.StatusInternalServerError)
					return
				}
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(index)
			return
		}
		staticFiles.ServeHTTP(w, r)
	})
}
