package gatefx

import (
	"bytes"
	"context"
	"encoding/json"

	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/milo-ctrl/room-engine/consts"
	"github.com/milo-ctrl/room-engine/consts/gatepb"
	"github.com/milo-ctrl/room-engine/env"
	"github.com/milo-ctrl/room-engine/eventbus"
	"github.com/milo-ctrl/room-engine/natsfx"
	"github.com/milo-ctrl/room-engine/serializer"

	"github.com/coder/websocket"
	"github.com/milo-ctrl/room-engine/douyinclient"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/redis/go-redis/v9"
	"github.com/spf13/cast"
	"github.com/spf13/viper"
	"go.uber.org/fx"
	"golang.org/x/time/rate"
)

// JsonBaseMsg 用于前端 JSON 通信的消息结构
type JsonBaseMsg struct {
	Cmd    string         `json:"cmd"`
	Data   map[string]any `json:"data,omitempty"`
	ErrMsg string         `json:"errMsg,omitempty"`
	Code   int32          `json:"code,omitempty"`
	ReqId  int32          `json:"reqId,omitempty"`
}

type Gate struct {
	natsConn  *natsfx.Conn
	servePort int
	redis     *redis.Client
	wsWg      sync.WaitGroup
	ctx       context.Context
	cancel    context.CancelCauseFunc
	envBase   *env.Base

	baseMsgPool sync.Pool
	natsMsgPool sync.Pool
}

var Module = fx.Module("gate",
	fx.Provide(NewGate),
	fx.Provide(fx.Annotate(func(v *viper.Viper) int {
		return v.GetInt("SERVE_PORT")
	}, fx.ResultTags(`name:"ServePort"`)), fx.Private),
	fx.Invoke(func(lc fx.Lifecycle, g *Gate, envBase *env.Base) error {
		if g == nil {
			return fmt.Errorf("gate not initialized")
		}
		if envBase.Environment == env.Dev { //开发环境内嵌的网关 才会启动
			lc.Append(fx.StopHook(func() { g.Stop(10 * time.Minute) }))
			g.Run()
		}
		return nil
	}),
)

type NewGateIn struct {
	fx.In
	NatsConn  *natsfx.Conn
	ServePort int `name:"ServePort"`
	Redis     *redis.Client
	EnvBase   *env.Base
}

func NewGate(p NewGateIn) (*Gate, error) {
	// 参数校验
	if p.ServePort <= 0 || p.ServePort > 65535 {
		return nil, fmt.Errorf("invalid SERVE_PORT: %d, must be between 1 and 65535", p.ServePort)
	}

	if p.NatsConn == nil {
		return nil, fmt.Errorf("NATS connection not initialized")
	}
	if p.Redis == nil {
		return nil, fmt.Errorf("Redis connection not initialized")
	}
	if p.EnvBase == nil {
		return nil, fmt.Errorf("environment base config not initialized")
	}

	g := &Gate{
		natsConn:  p.NatsConn,
		servePort: p.ServePort,
		redis:     p.Redis,
		envBase:   p.EnvBase,
	}
	g.baseMsgPool.New = func() interface{} {
		return &gatepb.BaseMsg{}
	}
	g.natsMsgPool.New = func() interface{} {
		return &nats.Msg{}
	}
	g.ctx, g.cancel = context.WithCancelCause(context.Background())
	return g, nil
}

func (g *Gate) Run() {

	slog.Info("Gate Run() called", "env", g.envBase.Environment)
	http.HandleFunc("GET /party/health", g.health)
	http.HandleFunc("GET /party", g.wsHandle)
	http.HandleFunc("POST /party/api/{gameName}/{comKey}/{handle}", g.apiHandle)
	slog.Info("gate started", "port", g.servePort)
	go func() {
		defer g.cancel(errors.New("stop server defer"))
		server := &http.Server{
			Addr: fmt.Sprintf(":%d", g.servePort),
			BaseContext: func(listener net.Listener) context.Context {
				return g.ctx
			}}
		if err := server.ListenAndServe(); err != nil {
			panic(err)
		}
	}()
}
func (g *Gate) Stop(wait time.Duration) {
	slog.Info("gate stopping")
	g.cancel(errors.New("stop server cancel"))
	timeoutCtx, cancel := context.WithTimeout(context.Background(), wait)
	go func() {
		g.wsWg.Wait()
		cancel()
	}()
	<-timeoutCtx.Done()
	slog.Info("gate stopped")
}

var wsAcceptOpt = &websocket.AcceptOptions{
	OriginPatterns: []string{"*"},
}

func (g *Gate) wsHandle(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	token := query.Get("token")

	connUuid, _ := uuid.NewV7()
	connId := connUuid.String()
	slog.Debug("websocket get", "token", token, "connId", connId)

	var uid string
	if g.envBase.Environment != env.Prod && strings.HasPrefix(token, "test") { //非正式服 允许test开头的的uid直接连接
		// 生成伪uid用于测试，格式：test-{uuid}
		testUuid, _ := uuid.NewV7()
		uid = fmt.Sprintf("test-%s", testUuid.String())
		slog.Debug("test mode uid generated", "uid", uid)
	} else {
		var err error
		uid, err = g.AppsJscode2session(token)
		if err != nil {
			http.Error(w, "token parser fail", http.StatusForbidden)
			slog.Error("douyin login fail", "err", err)
			return
		}
	}

	ctx, cancelFunc := context.WithCancelCause(r.Context())
	defer cancelFunc(errors.New("defer"))

	//处理顶号的问题
	multiConnChan := make(chan struct{}, 1)
	eSub, _ := eventbus.Subscribe(g.natsConn, connId, consts.EventTopicUserConnChanged(uid), func(event *consts.EventUserConnChanged) {
		if event.ConnId == connId || event.State == 0 {
			return
		}
		slog.Debug("multi conn", "uid", uid, "oldConnId", event.ConnId, "newConnId", connId)
		multiConnChan <- struct{}{}
	})
	defer eSub.Unsubscribe()

	msgChan := make(chan *nats.Msg, 2048)

	singleSub, err := g.natsConn.ChanSubscribe(consts.SubjectComponentEventUid(uid), msgChan) //单人event
	if err != nil {
		slog.Error("SubjectComponentEventUid fail", "err", err, "uid", uid)
		http.Error(w, "SubjectComponentEventUid fail", http.StatusBadRequest)
		return
	}
	defer singleSub.Unsubscribe()

	conn, err := websocket.Accept(w, r, wsAcceptOpt)
	if err != nil {
		slog.Error("websocket.Accept fail", "err", err, "uid", uid)
		http.Error(w, "websocket.Accept err", http.StatusExpectationFailed)
		return
	}

	// 设置消息大小限制为256k
	conn.SetReadLimit(256 * 1024)

	defer func() {
		cause := context.Cause(ctx)
		conn.CloseNow()
		slog.Debug("websocket close", "uid", uid, "connId", connId, "cause", cause)
	}()
	slog.Debug("websocket accept", "uid", uid, "connId", connId)

	g.wsWg.Add(1)
	defer g.wsWg.Done()

	//上线下线发布一个事件
	g.publishUserLifeEvent(uid, connId, 1)       //上线
	defer g.publishUserLifeEvent(uid, connId, 0) //下线

	var errResp = func(baseMsg *gatepb.BaseMsg, code int32, errMsg string) {
		baseMsg.Data = nil
		baseMsg.ErrMsg = errMsg
		baseMsg.Code = code
		bts, _ := serializer.Default.Marshal(baseMsg)
		err = conn.Write(ctx, websocket.MessageBinary, bts)
		if err != nil {
			cancelFunc(fmt.Errorf("errResp:%w", err))
		}
	}
	//限速
	pingLimiter := rate.NewLimiter(rate.Every(50*time.Second), 1)
	lastPingTime := time.Now()
	pingCheckTicker := time.NewTicker(35 * time.Second)
	go func() {
		defer pingCheckTicker.Stop()
		for {
			select {
			case msg := <-msgChan:
				exceptUids := msg.Header.Values(consts.HeaderExceptUids)
				if slices.Contains(exceptUids, uid) { //被排除的id不发到客户端
					continue
				}
				cmd := msg.Header.Get(consts.HeaderCmd)
				baseMsg := g.baseMsgPool.Get().(*gatepb.BaseMsg)
				baseMsg.Data = msg.Data
				baseMsg.Cmd = cmd
				bts, _ := serializer.Default.Marshal(baseMsg)
				err := conn.Write(ctx, websocket.MessageBinary, bts)
				if err != nil {
					cancelFunc(fmt.Errorf("msgChan Write:%w", err))
					return
				}
				baseMsg.Reset()
				g.baseMsgPool.Put(baseMsg)
			case <-multiConnChan: //顶号 发送最后一个消息
				{
					baseMsg := g.baseMsgPool.Get().(*gatepb.BaseMsg)
					baseMsg.Cmd = "gate.gate.eventdisconnect"
					bts, _ := serializer.Default.Marshal(baseMsg)
					baseMsg.Reset()
					g.baseMsgPool.Put(baseMsg)
					err = conn.Write(context.Background(), websocket.MessageBinary, bts)
					if err != nil {
						slog.Error("eventdisconnect  err", "err", err)
					}
					cancelFunc(fmt.Errorf("multiConnChan Write:%w", err))
					return
				}
			case <-pingCheckTicker.C:
				{
					if time.Since(lastPingTime) >= 30*time.Second { //客户端3次心跳没收到
						cancelFunc(errors.New("ping checker timeout"))
						return
					}
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	for {
		_, msg, err := conn.Read(ctx)
		if err != nil {
			cancelFunc(fmt.Errorf("conn.Read:%w", err))
			return
		}

		// 使用 JSON 反序列化前端消息
		var jsonMsg JsonBaseMsg
		if err := json.Unmarshal(msg, &jsonMsg); err != nil {
			slog.Error("JSON Unmarshal err", "err", err, "raw", string(msg))
			baseMsg := g.baseMsgPool.Get().(*gatepb.BaseMsg)
			errResp(baseMsg, -403, "JSON Unmarshal fail")
			g.baseMsgPool.Put(baseMsg)
			continue
		}

		baseMsg := g.baseMsgPool.Get().(*gatepb.BaseMsg)
		baseMsg.Cmd = jsonMsg.Cmd
		baseMsg.ReqId = jsonMsg.ReqId

		// 将 JSON data 转为 proto 字节数组
		if jsonMsg.Data != nil {
			dataBytes, err := json.Marshal(jsonMsg.Data)
			if err != nil {
				slog.Error("Marshal data err", "err", err)
				errResp(baseMsg, -403, "Marshal data fail")
				g.baseMsgPool.Put(baseMsg)
				continue
			}
			baseMsg.Data = dataBytes
		}

		//完全不允许内部rpc通过网关 网关遇到此类请求 直接断开
		if strings.Contains(baseMsg.Cmd, "inner") || strings.Contains(baseMsg.Cmd, "Inner") {
			cancelFunc(errors.New("not inner request"))
			return
		}

		sp := strings.Split(baseMsg.Cmd, ".")
		if len(sp) != 3 {
			errResp(baseMsg, -403, "cmd illegal")
			return
		}
		gameName, comKey, handle := sp[0], sp[1], sp[2] //三段式 gameName.comKey.handle

		//特殊心跳消息
		if handle == "ping" {
			lastPingTime = time.Now()
			//TODO 这里确实需要一个房间ID, 心跳到哪一个房间内, 知道房间是否还存在, 后面想想怎么处理
			if pingErr := g.ping(&jsonMsg, conn, ""); pingErr != nil {
				cancelFunc(fmt.Errorf("ping Write:%w", pingErr))
				return
			}
			// baseMsg.Reset()
			// g.baseMsgPool.Put(baseMsg)
			allowed := pingLimiter.AllowN(time.Now(), 1)
			if allowed {
				g.redis.Expire(g.ctx, consts.RdsKeyUserOnlineState(uid), 130*time.Second)
				eventbus.Publish(g.natsConn, consts.EventTopicUserHeartbeat, &consts.EventUserHeartbeat{
					Uid: uid,
				})
			}
			continue
		}

		suj := consts.SubjectReqRet(gameName, comKey, uid)

		nasMsg := g.natsMsgPool.Get().(*nats.Msg)
		nasMsg.Subject = suj
		nasMsg.Data = baseMsg.Data
		nasMsg.Header = nats.Header{
			consts.HeaderCmd:     []string{strings.Join(sp[1:], ".")},
			consts.HeaderUid:     []string{uid},
			consts.HeaderReqType: []string{cast.ToString(serializer.KindJson)},
		}

		retMag, err := g.natsConn.RequestMsg(nasMsg, 5*time.Second)

		nasMsg.Subject = ""
		nasMsg.Data = nil
		clear(nasMsg.Header)
		g.natsMsgPool.Put(nasMsg)

		// 构造 JSON 响应消息
		jsonResp := JsonBaseMsg{
			Cmd:   jsonMsg.Cmd,
			ReqId: jsonMsg.ReqId,
		}

		if err != nil {
			if errors.Is(err, nats.ErrNoResponders) {
				jsonResp.Code = -404
			} else if errors.Is(err, nats.ErrTimeout) {
				jsonResp.Code = -504
			} else {
				jsonResp.Code = -500
			}
			jsonResp.ErrMsg = "request fail!"
			slog.Error("RequestMsg err", "uid", uid, "cmd", jsonMsg.Cmd, "err", err)
		} else {
			jsonResp.Code = cast.ToInt32(retMag.Header.Get("code"))
			jsonResp.ErrMsg = retMag.Header.Get("msg")

			// 将内部服务返回的 proto 字节数组转为 JSON map
			if len(retMag.Data) > 0 {
				var dataMap map[string]any
				if err := json.Unmarshal(retMag.Data, &dataMap); err != nil {
					slog.Error("Unmarshal response data err", "err", err)
					jsonResp.Code = -500
					jsonResp.ErrMsg = "parse response fail"
				} else {
					jsonResp.Data = dataMap
				}
			}
		}

		// 将 JSON 响应序列化并发送
		bts, _ := json.Marshal(jsonResp)
		baseMsg.Reset()
		g.baseMsgPool.Put(baseMsg)

		err = conn.Write(ctx, websocket.MessageBinary, bts)
		if err != nil {
			cancelFunc(fmt.Errorf("conn.Read Write:%w", err))
			return
		}
	}
}

func (g *Gate) ping(jsonMsg *JsonBaseMsg, conn *websocket.Conn, liveId string) error {
	now := time.Now()

	// 构造 JSON 格式的 ping 响应
	pingResp := map[string]any{
		"ts":           now.UnixMilli(),
		"forwardTs":    now.UnixMilli(),
		"forwardState": 0,
	}

	// 如果前端指定了转发游戏名，尝试探测该游戏是否存在
	if jsonMsg.Data != nil {
		if forwardGameName, ok := jsonMsg.Data["forwardToGameName"].(string); ok && forwardGameName != "" {
			comKey := "room" //这里不写死怎么办??
			suj := consts.SubjectReqRetLiveHouse(forwardGameName, comKey, liveId)
			_, err := g.natsConn.Request(suj, nil, 5*time.Second) //只需要探测下监听是否存在
			if errors.Is(err, nats.ErrNoResponders) {             //没有监听 则说明游戏不存在了
				pingResp["forwardTs"] = time.Now().UnixMilli()
				pingResp["forwardState"] = 1 //没有游戏房了
			} else if err != nil {
				pingResp["forwardState"] = -1 //错误
			}
		}
	}

	// 构造 JSON 响应消息
	jsonResp := JsonBaseMsg{
		Cmd:   jsonMsg.Cmd,
		ReqId: jsonMsg.ReqId,
		Code:  0,
		Data:  pingResp,
	}

	bts, _ := json.Marshal(jsonResp)
	err := conn.Write(g.ctx, websocket.MessageBinary, bts)
	if err != nil {
		return err
	}
	return nil
}

// 发送用户生命周期事件 tp:0断开 1连接
func (g *Gate) publishUserLifeEvent(uid, connId string, state int) {
	switch state {
	case 1:
		g.redis.HSet(context.Background(), consts.RdsKeyUserOnlineState(uid), "connectTs", time.Now().Unix())
		g.redis.Expire(context.Background(), consts.RdsKeyUserOnlineState(uid), 130*time.Second) //和客户端约定60s发送一次ping
	case 0:
		g.redis.Del(context.Background(), consts.RdsKeyUserOnlineState(uid))
	}

	//发送上线下线事件
	e := consts.EventUserConnChanged{
		Uid:    uid,
		State:  state,
		ConnId: connId,
	}
	eventbus.Publish(g.natsConn, consts.EventTopicUserConnChanged(uid), e)
}

func (g *Gate) apiHandle(w http.ResponseWriter, r *http.Request) {
	//if r.Method != http.MethodPost {
	//	http.Error(w, "", http.StatusMethodNotAllowed)
	//	return
	//}
	//path := strings.Trim(r.URL.Path, "/")
	//spl := strings.Split(path, "/") // /party/api/center/public/getXXX
	//if len(spl) != 5 {
	//	http.Error(w, "", http.StatusNotFound)
	//	return
	//}
	gameName, comKey, handle := r.PathValue("gameName"), r.PathValue("comKey"), r.PathValue("handle")

	//只允许调用inner
	if !strings.Contains(handle, "inner") && !strings.Contains(handle, "Inner") {
		http.Error(w, "", http.StatusNotFound)
		return
	}
	header := r.Header
	ts := cast.ToInt64(header.Get("ts")) //秒级别字符串
	randStr := header.Get("randStr")     //随机字符串 每次请求唯一
	if ts < time.Now().Unix()-60 || randStr == "" {
		http.Error(w, "", http.StatusBadRequest)
		return
	}
	buffer := &bytes.Buffer{}
	_, err := io.Copy(buffer, r.Body)
	if err != nil {
		http.Error(w, "", http.StatusNotAcceptable)
		return
	}
	bodyBts := make([]byte, buffer.Len())
	copy(bodyBts, buffer.Bytes())

	//防重放检测
	nx := g.redis.SetNX(r.Context(), consts.RdsKeyOpenapiNoReplay(randStr), 1, time.Second*60).Val()
	if !nx {
		http.Error(w, "", http.StatusForbidden)
		return
	}

	suj := consts.SubjectReqRet(gameName, comKey, "x")

	nasMsg := &nats.Msg{
		Subject: suj,
		Data:    bodyBts,
		Header: nats.Header{
			consts.HeaderCmd:     []string{strings.Join([]string{comKey, handle}, ".")},
			consts.HeaderReqType: []string{cast.ToString(serializer.KindJson)}, //外部请求 采用json序列化方式
		},
	}
	slog.Debug("apiHandle", "body", string(bodyBts))
	retMag, err := g.natsConn.RequestMsg(nasMsg, 5*time.Second)
	if err != nil {
		if errors.Is(err, nats.ErrNoResponders) || retMag.Header.Get("code") == "-404" {
			http.Error(w, "", http.StatusNotFound)
			return
		} else {
			slog.Error("apiHandle ServerError err", "err", err)
			http.Error(w, "", http.StatusInternalServerError)
			return
		}
	}

	h := w.Header()
	h.Set("code", cast.ToString(cast.ToInt(retMag.Header.Get("code"))))
	h.Set("msg", retMag.Header.Get("msg"))
	h.Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(retMag.Data)
}

func (g *Gate) health(w http.ResponseWriter, r *http.Request) {
	err := g.natsConn.Publish("health", nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	_, _ = w.Write([]byte("ok"))
}

/**
 * 抖音登陆
 */
func (g *Gate) AppsJscode2session(loginCode string) (string, error) {
	slog.Info("抖音相关的信息", "id", g.envBase.DouyinAppId, "secret", g.envBase.DouyinAppSecret, "code", loginCode)
	return douyinclient.AppsJscode2session(g.envBase.DouyinAppId, g.envBase.DouyinAppSecret, loginCode)
}
