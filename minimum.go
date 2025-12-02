package rme

import (
	"context"
	"log/slog"
	"reflect"
	"time"

	"github.com/spf13/viper"
	"gitlab-code.v.show/bygame/room-engine/env"
	"gitlab-code.v.show/bygame/room-engine/logfx"
	"gitlab-code.v.show/bygame/room-engine/natsfx"
	"gitlab-code.v.show/bygame/room-engine/redisfx"
	"gitlab-code.v.show/bygame/room-engine/rpc"
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

	//空结构的components
	emptyComponents []Handler
}

func Run(opts ...func(*option)) {
	o := &option{}
	for _, opt := range opts {
		opt(o)
	}

	var envBase *env.Base
	fxOpts := []fx.Option{
		fx.WithLogger(FxLogger),
		Module,
		fx.Invoke(func(base *env.Base) {
			envBase = base
		}),
	}
	fxOpts = append(fxOpts, o.fxOptions...)
	app := fx.New(fxOpts...)

	ctx, cancelFunc := context.WithTimeout(context.Background(), app.StartTimeout())
	defer cancelFunc()
	err := app.Start(ctx)
	if err != nil {
		panic(err)
	}
	larkNotifyStart(envBase.GameName, envBase.Environment, envBase.ProcName, envBase.Version)

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

// WithComponentConstructor 有状态组件的构造函数
// 注:这里的构造函数只能构造需要的单例对象
// 例如:
//
//	type Room struct {
//		  redis *redis.Client //redis 为需要的单例中间件
//		  natsConn *natsfx.Conn //natsConn 为需要的单例中间件
//	      currUid string   	// 业务数据 不能在Constructor中构造
//	      m map[string]string // 业务数据 不能在Constructor中构造
//	}
//
// Constructor如下:只能构造fx容器中的单例对象,返回的组件是作为单例保存的,最终创建单例组件的时候 会以这个单例为源头进行copy
//
//	func NewRoom(redis *redis.Client, natsConn *natsfx.Conn) *Room {
//			//只能初始化单例对象 业务数据 不能在Constructor中构造!!(业务数据的初始放到OnStart的回调中)
//			return &Room{redis: redis, natsConn: natsConn}
//			//以下是错误示例 m不能再这里进行初始化,需要将其放在Onstart中
//			return &Room{redis: redis, natsConn: natsConn,m: make(map[string]string)}
//	}
//
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
