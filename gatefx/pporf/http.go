package pporf

import (
	"net/http"
	"net/http/pprof"
)

func init() {
	http.HandleFunc("GET /party/debug/pprof/", auth(pprof.Index))
	http.HandleFunc("GET /party/debug/pprof/cmdline", auth(pprof.Cmdline))
	http.HandleFunc("GET /party/debug/pprof/profile", auth(pprof.Profile))
	http.HandleFunc("GET /party/debug/pprof/symbol", auth(pprof.Symbol))
	http.HandleFunc("GET /party/debug/pprof/trace", auth(pprof.Trace))
}

const (
	username = "rme"
	password = "rme"
)

func auth(handler http.HandlerFunc) http.HandlerFunc {
	f := func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != username || pass != password {
			// 设置响应头，浏览器会弹出认证对话框
			w.Header().Set("WWW-Authenticate", `Basic realm="请输入用户名密码"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		handler(w, r)
	}
	handlerFunc := http.HandlerFunc(f)

	return http.StripPrefix("/party", handlerFunc).(http.HandlerFunc)
}
