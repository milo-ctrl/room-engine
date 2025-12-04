package rme

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"log/slog"
	"os"
	"reflect"
	"runtime/pprof"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"room-engine/consts"
	"room-engine/env"
	"room-engine/natsfx"
	"room-engine/serializer"

	"github.com/nats-io/nats.go"
	"github.com/redis/go-redis/v9"
	"github.com/spf13/cast"
	"go.uber.org/fx"
)

type Engine struct {
	envBase   *env.Base
	natsConn  *natsfx.Conn // nats连接
	redis     *redis.Client
	methodMap map[string]doFunc
	ctx       context.Context
	cancel    context.CancelFunc
	clearWg   *sync.WaitGroup
	helper    *Helper
}

var EnginModule = fx.Module("engin", fx.Provide(NewEngine),
	fx.Invoke(func(lc fx.Lifecycle, e *Engine) {
		lc.Append(fx.StartHook(e.Start))
		lc.Append(fx.StopHook(e.Stop))
	}),
)

type NewEngineIn struct {
	fx.In
	EnvBase    *env.Base
	NatsConn   *natsfx.Conn
	Redis      *redis.Client
	Helper     *Helper
	Components []Handler `group:"Components"`
	Handles    []Handler `group:"Handles"`
}

func NewEngine(p NewEngineIn) (*Engine, EngineContext) {
	e := &Engine{
		envBase:   p.EnvBase,
		natsConn:  p.NatsConn,
		redis:     p.Redis,
		helper:    p.Helper,
		methodMap: make(map[string]doFunc),
		clearWg:   &sync.WaitGroup{},
	}
	e.ctx, e.cancel = context.WithCancel(context.Background())
	if len(p.Components) > 0 {
		e.registerComponentHandle(p.Components...)
	}
	if len(p.Handles) > 0 {
		e.registerRpcHandle(p.Handles...)
	}
	return e, e.ctx
}

// Start 启动完毕之后 注册进程名
func (e *Engine) Start() (err error) {
	defer func() {
		if err != nil {
			e.cancel()
		}
	}()

	//启动时候检查同名进程是不是已经启动了
	expire := 120 * time.Second
	aliveKey := consts.RdsKeyProcAlive(e.envBase.ProcName)
	nx, _ := e.redis.SetNX(e.ctx, aliveKey, 0, expire).Result()
	if !nx {
		err = fmt.Errorf("procName %s already exists", e.envBase.ProcName)
		return
	}

	procDetail := consts.ProcDetail{
		ProcName:      e.envBase.ProcName,
		Version:       e.envBase.Version,
		ClientVersion: e.envBase.ClientVersion,
		StartTs:       time.Now().Unix(),
	}
	pip := e.redis.Pipeline()
	pip.HSet(e.ctx, consts.RdsKeyProcDetail(e.envBase.ProcName), procDetail)
	pip.HSet(e.ctx, consts.RdsKeyProcAll(e.envBase.GameName), e.envBase.ProcName, 1) //写入全部进程 不过期
	_, err = pip.Exec(e.ctx)
	if err != nil {
		return
	}

	//定时续约 ProcAlive
	Go(func() {
		tick := time.Tick(expire/2 - 10*time.Second) //一个生命周期 2次心跳
		for {
			select {
			case <-e.ctx.Done():
				return
			case <-tick:
				suc := e.redis.Expire(e.ctx, aliveKey, expire).Val()
				if !suc { //兜底机制: key不存在了 Expire会设置失败 此时setNx一下
					e.redis.SetNX(e.ctx, aliveKey, 0, expire)
				}
			}
		}
	})

	subErr := e.sysSub()
	if subErr != nil {
		e.cancel()
	}
	slog.Debug("eng Start", "err", err)
	return subErr
}

func (e *Engine) sysSub() error {
	//系统操作 目前就只有停止进程 后续可能:pporf? 性能指标?
	_, subErr := e.natsConn.Subscribe(consts.SubjectSys(e.envBase.GameName, e.envBase.ProcName), func(msg *nats.Msg) {
		opt := msg.Header.Get("opt")

		switch opt {
		case "kill":
			{
				slog.Info("receive kill msg")
				err := syscall.Kill(os.Getpid(), syscall.SIGINT)
				if err != nil {
					slog.Error("syscall.Kill", "err", err)
				}
			}
		case "pprof":
			e.doPProf(msg)
		}
	})
	return subErr
}

func (e *Engine) doPProf(msg *nats.Msg) {
	name := string(msg.Data)
	profile := pprof.Lookup(name)
	if profile == nil {
		return
	}
	buffer := bytes.Buffer{}
	writer := gzip.NewWriter(&buffer)
	defer writer.Close()
	err := profile.WriteTo(writer, 0)
	if err != nil {
		slog.Error("pprof  heap err", "err", err)
	}
	_ = msg.Respond(buffer.Bytes())
}

// Stop 停机 删除进程名
func (e *Engine) Stop() error {
	err := e.redis.Del(context.Background(), consts.RdsKeyProcAlive(e.envBase.ProcName)).Err()
	e.cancel()

	Go(e.clearWg.Wait) //等待clear的waitGroup可能是个很长的过程(例如在在线用户多的时候 强制重启)

	slog.Debug("eng Stop", "err", err)
	return err
}

// 注册组件
func (e *Engine) register(handlers ...Handler) {
	// 这里需要反射得到所有方法
	for _, handler := range handlers {
		if handler.Key() == "" || slices.ContainsFunc([]rune(handler.Key()), func(r rune) bool { return unicode.IsUpper(r) }) {
			panic("handler key() 不能为空,必须为全小写")
		}
		revTp := reflect.TypeOf(handler)
		for i := range revTp.NumMethod() {
			refFunc := revTp.Method(i)
			funcName := refFunc.Name
			if !strings.HasPrefix(funcName, "Handle") {
				continue
			}
			if refFunc.Type.NumIn() != 3 { //通过type反射 拿到的方法参数数量 包含方法接收者
				continue
			}
			numOut := refFunc.Type.NumOut()
			if numOut != 2 { //限定返回值个数
				continue
			}
			if !refFunc.Type.In(1).AssignableTo(ctxType) { //判断第一个参数是不是context 类型
				continue
			}
			if !refFunc.Type.Out(1).AssignableTo(errType) { //限定第二的返回值是err
				continue
			}

			r, _ := strings.CutPrefix(funcName, "Handle") //移除前缀
			router := fmt.Sprintf("%s.%s", handler.Key(), r)
			router = strings.ToLower(router)
			if _, ok := e.methodMap[router]; ok {
				panic("重复注册:" + funcName)
			}

			if r != consts.CmdGetComponentDetail && r != consts.CmdInnerDisband {
				//Inner特殊处理
				routerCheck := strings.ReplaceAll(router, "Inner", "")
				routerCheck = strings.ReplaceAll(router, "inner", "")

				//限定入参
				cut, found := strings.CutSuffix(refFunc.Type.In(2).String(), "Req")
				if !found {
					panic(fmt.Sprintf("Handle:[%s] 入参错误,未以Req结尾", funcName))
				}
				cut = strings.ReplaceAll(cut, "pb", "")
				cut = strings.ReplaceAll(cut, "*", "")
				if !strings.EqualFold(cut, routerCheck) {
					panic(fmt.Sprintf("Handle:[%s] 命名与参数不匹配", funcName))
				}

				//限定出参
				cut, found = strings.CutSuffix(refFunc.Type.Out(0).String(), "Resp")
				if !found {
					panic(fmt.Sprintf("Handle:[%s] 出参错误,未以Resp结尾", funcName))
				}
				cut = strings.ReplaceAll(cut, "pb", "")
				cut = strings.ReplaceAll(cut, "*", "")
				if !strings.EqualFold(cut, routerCheck) {
					panic(fmt.Sprintf("Handle:[%s] 命名返回值不匹配", funcName))
				}
			}

			reqTp := refFunc.Type.In(2)
			doFunc := func(ctx context.Context, serializer serializer.Serializer, reqData []byte, instance Handler) (any, error) {
				var value reflect.Value
				var reqVal any
				if reqTp.Kind() == reflect.Ptr {
					value = reflect.New(reqTp.Elem())
				} else {
					value = reflect.New(reqTp)
				}
				reqVal = value.Interface()

				if len(reqData) > 0 {
					err := serializer.Unmarshal(reqData, reqVal)
					if err != nil {
						slog.Error("Unmarshal", "err", err)
						return nil, &ResultError{
							Code: -500,
							Msg:  fmt.Sprintf("unmarshal req error: %v", err),
						}
					}
				}
				if reqTp.Kind() != reflect.Ptr {
					value = value.Elem()
				}
				rets := refFunc.Func.Call([]reflect.Value{reflect.ValueOf(instance), reflect.ValueOf(ctx), value})

				var ret1 error
				if !rets[1].IsNil() {
					ret1 = rets[1].Interface().(error)
				}
				var ret0 any
				if rets[0].Kind() != reflect.Ptr || !rets[0].IsNil() {
					ret0 = rets[0].Interface()
				}
				slog.Debug("reqResp", "gameName", e.envBase.GameName, "uid", GetUidFromContext(ctx), "cmd", router, "req", reqVal, "resp", ret0, "err", ret1)
				return ret0, ret1
			}
			e.methodMap[router] = doFunc
		}
	}
}

// registerRpcHandle 注册RPC服务 Rpc提供给服务间调用
// 需要传入真实的handle实例
func (e *Engine) registerRpcHandle(handlers ...Handler) {
	e.register(handlers...)

	for _, handler := range handlers {
		//同求请求回复队列 无装填服务 组件 共用  这里用通配符兼容
		sbj := consts.SubjectReqRet(e.envBase.GameName, handler.Key(), "*")
		sub, err := e.natsConn.QueueSubscribe(sbj, "rpc", func(msg *nats.Msg) {
			cmd := msg.Header.Get(consts.HeaderCmd)
			if cmd == "" {
				_ = msg.Respond(nil)
				return
			}
			Go(func() {
				e.onRpcReqeust(msg, handler)
			})
		})
		if err != nil {
			panic(err)
		}

		//停机前解除订阅
		e.clearWg.Add(1)
		context.AfterFunc(e.ctx, func() {
			defer e.clearWg.Done()
			sub.Unsubscribe()
			slog.Debug("Unsubscribe Rpc")
		})
	}
}

func (e *Engine) onRpcReqeust(msg *nats.Msg, handler Handler) {
	defer func() {
		if err := recover(); err != nil {
			slog.Error("rpc request panic", "err", err, "stack", Stack())
			msg.Data = nil
			msg.Header.Set("code", "-500")
			msg.Header.Set("msg", fmt.Sprintf("sever fatal:%v", err))
			_ = msg.RespondMsg(msg)
		}
	}()
	if msg.Header == nil { //header可能为nil
		msg.Header = nats.Header{}
	}
	cmd := msg.Header.Get(consts.HeaderCmd)
	if f, ok := e.methodMap[strings.ToLower(cmd)]; ok {
		uid := msg.Header.Get(consts.HeaderUid)
		liveId := msg.Header.Get(consts.HeaderLiveId)
		reqType := msg.Header.Get(consts.HeaderReqType)
		//ctx := context.WithValue(e.ctx, "uid", uid)
		//ctx = context.WithValue(ctx, "liveId", liveId)
		ctx := NewContextWith(e.ctx, uid, liveId)
		timeout, cancelFunc := context.WithTimeout(ctx, 5*time.Second) //默认handle 5s超时
		defer cancelFunc()
		seria := serializer.GetSerializer(cast.ToInt(reqType))
		data, rErr := f(timeout, seria, msg.Data, handler)
		msg.Data = nil
		if rErr != nil {
			resErr, ok := rErr.(*ResultError)
			errCode := -1
			if ok {
				errCode = resErr.Code
			}
			msg.Header.Set("code", cast.ToString(errCode))
			msg.Header.Set("msg", rErr.Error())
		}
		if data != nil {
			dataBts, _ := seria.Marshal(data)
			msg.Data = dataBts
		}
		err := msg.RespondMsg(msg)
		if err != nil {
			slog.Error("nats Respond err:", "err", err)
		}
	} else {
		slog.Error("method not found", "cmd", cmd)
		msg.Header.Set("code", "-404")
		msg.Header.Set("msg", fmt.Sprintf("router:%s not found handle", cmd))
		_ = msg.Respond(nil)
	}
}

// registerComponentHandle 注册组件handle
// 传入handle指针,需要包含公共单例对象
func (e *Engine) registerComponentHandle(handlers ...Handler) {
	e.register(handlers...)
	// 这里监听创建方法
	for _, handler := range handlers {
		//由匹配服选择进程 来调用
		sbj := consts.SubjectComponentCreate(e.envBase.GameName, e.envBase.ProcName, handler.Key())
		sub, err := e.natsConn.QueueSubscribe(sbj, "create", func(msg *nats.Msg) {
			Go(func() {
				onComponentCreate(msg, handler, e)
			}) //这里需要并发 nats消息回调是串行的
		})
		if err != nil {
			panic(err)
		}

		//停机前解除订阅
		e.clearWg.Add(1)
		context.AfterFunc(e.ctx, func() {
			defer e.clearWg.Done()
			slog.Debug("Unsubscribe ComponentCreate")
			sub.Unsubscribe()
		})
	}
}
