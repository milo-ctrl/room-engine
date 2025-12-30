package rme

import (
	"context"
	"log/slog"
	"reflect"
	"time"

	"github.com/milo-ctrl/room-engine/env"
	"github.com/milo-ctrl/room-engine/logfx"
	"github.com/milo-ctrl/room-engine/natsfx"
	"github.com/milo-ctrl/room-engine/redisfx"
	"github.com/milo-ctrl/room-engine/rpc"

	"github.com/spf13/viper"
	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"
)

var Module = fx.Module("rme",
	EnginModule,
	fx.Provide(rpc.NewClient),
	fx.Provide(NewHelper),
	natsfx.Module,
	env.Module,
	logfx.Module,
	redisfx.Module,
)

func FxLogger(slogger *slog.Logger, v *viper.Viper) fxevent.Logger {
	//这里初始化比较早 在这里全局设置时区
	timezone := v.GetString("TIMEZONE")
	if timezone == "" {
		timezone = "Asia/Shanghai"
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		panic("LoadLocation错误!!")
	} else {
		time.Local = location
	}
	return &fxevent.SlogLogger{
		Logger: slogger,
	}
}

type option struct {
	//fx 的options 需要注入进fx容器中的fx.Option
	fxOptions []fx.Option
}

func Run(opts ...func(*option)) {
	o := &option{}
	for _, opt := range opts {
		opt(o)
	}

	fxOpts := []fx.Option{
		fx.WithLogger(FxLogger),
		Module,
	}
	fxOpts = append(fxOpts, o.fxOptions...)
	app := fx.New(fxOpts...)

	ctx, cancelFunc := context.WithTimeout(context.Background(), app.StartTimeout())
	defer cancelFunc()
	err := app.Start(ctx)
	if err != nil {
		panic(err)
	}

	<-app.Wait() //不能用run run在结束时候 直接exit了 后边逻辑无法执行了

	slog.Debug("stopping")
	ctx, cancelFunc = context.WithTimeout(context.Background(), app.StopTimeout())
	defer cancelFunc()
	_ = app.Stop(ctx)

	WaitStop(10 * time.Minute)

	slog.Debug("stopped")
}

// WithRpcHandlerConstructor 传入RpcHandler的构造函数
func WithRpcHandlerConstructor(handlerConstructors ...any) func(*option) {
	return func(inst *option) {
		for _, constructor := range handlerConstructors {
			if reflect.TypeOf(constructor).Kind() != reflect.Func {
				panic("WithRpcHandlerConstructor 传入的参数必须是函数")
			}
			inst.fxOptions = append(inst.fxOptions, fx.Provide(fx.Annotate(constructor, fx.As(new(Handler)), fx.ResultTags(`group:"Handles"`))))
		}
	}
}

// 最终 在请求创建创建房间的得处理中 会copy这个Component 作为源
func WithComponentConstructor(componentConstructor ...any) func(*option) {
	return func(inst *option) {
		for _, constructor := range componentConstructor {
			if reflect.TypeOf(constructor).Kind() != reflect.Func {
				panic("WithRpcHandlerConstructor 传入的参数必须是函数")
			}
			inst.fxOptions = append(inst.fxOptions, fx.Provide(fx.Annotate(constructor, fx.As(new(Handler)), fx.ResultTags(`group:"Components"`))))
		}
	}
}

// WithFxOptions 追加进fx容器中的options
func WithFxOptions(opts ...fx.Option) func(*option) {
	return func(inst *option) {
		inst.fxOptions = append(inst.fxOptions, opts...)
	}
}
