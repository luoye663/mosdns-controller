package web

import (
	"embed"
	"io/fs"
	"net/http"
)

// static 是构建阶段复制 WebUI dist 后的嵌入目录；当前保留最小占位页面。
//
//go:embed static/*
var files embed.FS

func Handler() http.Handler {
	sub, err := fs.Sub(files, "static")
	if err != nil {
		panic(err)
	}
	return http.FileServer(http.FS(sub))
}
