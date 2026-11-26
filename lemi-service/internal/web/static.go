package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/LemonITCN/lemi/lemi-service/internal/apperr"
)

//go:embed all:dist
var distFS embed.FS

// serveStatic 提供前端静态文件；不存在的路径回落到 index.html（前端路由）。
func serveStatic(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/mcp") || strings.HasPrefix(r.URL.Path, "/oauth/") {
		WriteJSON(w, 404, map[string]any{"error": apperr.NotFound("接口")})
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		WriteJSON(w, 405, map[string]any{"error": apperr.New(405, "method_not_allowed", "不支持的请求方法")})
		return
	}
	sub, _ := fs.Sub(distFS, "dist")
	p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if p == "" {
		p = "index.html"
	}
	if f, err := sub.Open(p); err == nil {
		st, _ := f.Stat()
		f.Close()
		if st != nil && !st.IsDir() && !strings.HasPrefix(path.Base(p), ".") {
			if strings.HasPrefix(p, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			http.ServeFileFS(w, r, sub, p)
			return
		}
	}
	idx, err := fs.ReadFile(sub, "index.html")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	if err != nil {
		w.WriteHeader(200)
		w.Write([]byte("<!doctype html><meta charset=utf-8><title>Lemi</title><p>前端尚未构建：请在 lemi-console 目录执行 npm run build，再重新编译服务。</p>"))
		return
	}
	w.Write(idx)
}
