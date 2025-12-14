package main

import (
	"time"

	rme "github.com/milo-ctrl/room-engine"
	"github.com/milo-ctrl/room-engine/gatefx"

	"github.com/spf13/viper"
	"go.uber.org/fx"
)

// 网关进程
// 也按照rme方式启动 需要配置文件
// 这样可以被管理到
func main() {
	opts := fx.Options(
		fx.StopTimeout(10*time.Minute),
		fx.Provide(gatefx.NewGate),
		fx.Provide(fx.Annotate(func(v *viper.Viper) int {
			return v.GetInt("SERVE_PORT")
		}, fx.ResultTags(`name:"ServePort"`))),
		fx.Invoke(func(lc fx.Lifecycle, g *gatefx.Gate) {
			lc.Append(fx.StopHook(func() { g.Stop(10 * time.Minute) }))
			g.Run()
		}),
	)
	rme.Run(rme.WithFxOptions(opts))
}
