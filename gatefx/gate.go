package gatefx

import (
	"bytes"
	"context"
	"crypto/sha256"
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

	"room-engine/consts"
	"room-engine/consts/gatepb"
	"room-engine/env"
	"room-engine/eventbus"
	"room-engine/natsfx"
	"room-engine/serializer"

	credential "github.com/bytedance/douyin-openapi-credential-go/client"
	openApiSdkClient "github.com/bytedance/douyin-openapi-sdk-go/client"
	"github.com/coder/websocket"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/redis/go-redis/v9"
	"github.com/spf13/cast"
	"github.com/spf13/viper"
	"go.uber.org/fx"
	"golang.org/x/time/rate"
)

type Gate struct {
	natsConn    *natsfx.Conn
	servePort   int
	redis       *redis.Client
	wsWg        sync.WaitGroup
	ctx         context.Context
	cancel      context.CancelCauseFunc
	envBase     *env.Base
	jwtKey      string
	apiSalt     string
	baseMsgPool sync.Pool
	natsMsgPool sync.Pool
}

var Module = fx.Module("gate",
	fx.Provide(NewGate),
	fx.Provide(fx.Annotate(func(v *viper.Viper) (int, string, string) {
		return v.GetInt("SERVE_PORT"), v.GetString("JWT_KEY"), v.GetString("API_SALT")
	}, fx.ResultTags(`name:"ServePort"`, `name:"JwtKey"`, `name:"ApiSalt"`)), fx.Private),
	fx.Invoke(func(lc fx.Lifecycle, g *Gate, envBase *env.Base) {
		if envBase.Environment == env.Dev { //开发环境内嵌的网关 才会启动
			lc.Append(fx.StopHook(func() { g.Stop(10 * time.Minute) }))
			g.Run()
		}
	}),
)

type NewGateIn struct {
	fx.In
	NatsConn  *natsfx.Conn
	ServePort int `name:"ServePort"`
	Redis     *redis.Client
	EnvBase   *env.Base
	JwtKey    string `name:"JwtKey"`
	ApiSalt   string `name:"ApiSalt"`
}

func NewGate(p NewGateIn) *Gate {
	g := &Gate{
		natsConn:  p.NatsConn,
		servePort: p.ServePort,
		redis:     p.Redis,
		envBase:   p.EnvBase,
		jwtKey:    p.JwtKey,
		apiSalt:   p.ApiSalt,
	}
	g.baseMsgPool.New = func() interface{} {
		return &gatepb.BaseMsg{}
	}
	g.natsMsgPool.New = func() interface{} {
		return &nats.Msg{}
	}
	g.ctx, g.cancel = context.WithCancelCause(context.Background())
	return g
}

func (g *Gate) Run() {
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
		uid = token
	} else {
		var err error
		//TODO 这里是登陆修改这里就好
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
	pingCheckTick := time.Tick(35 * time.Second)
	go func() {
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
			case <-pingCheckTick:
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
		baseMsg := g.baseMsgPool.Get().(*gatepb.BaseMsg)
		if err := serializer.Default.Unmarshal(msg, baseMsg); err != nil {
			slog.Error("Unmarshal err", "err", err)
			errResp(baseMsg, -403, "Unmarshal fail")
			continue
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
			// if pingErr := g.ping(baseMsg, conn, liveId); pingErr != nil {
			// 	cancelFunc(fmt.Errorf("ping Write:%w", pingErr))
			// 	return
			// }
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
			consts.HeaderCmd: []string{strings.Join(sp[1:], ".")},
			consts.HeaderUid: []string{uid},
		}

		retMag, err := g.natsConn.RequestMsg(nasMsg, 5*time.Second)

		nasMsg.Subject = ""
		nasMsg.Data = nil
		clear(nasMsg.Header)
		g.natsMsgPool.Put(nasMsg)

		baseMsg.Data = nil
		baseMsg.ErrMsg = ""
		if err != nil {
			if errors.Is(err, nats.ErrNoResponders) {
				baseMsg.Code = -404
			} else if errors.Is(err, nats.ErrTimeout) {
				baseMsg.Code = -504
			} else {
				baseMsg.Code = -500
			}
			baseMsg.ErrMsg = "reqeust fail!"
			slog.Error("RequestMsg err", "uid", uid, "cmd", baseMsg.Cmd, "err", err)
		} else {
			baseMsg.Code = cast.ToInt32(retMag.Header.Get("code"))
			baseMsg.ErrMsg = retMag.Header.Get("msg")
			baseMsg.Data = retMag.Data
		}

		bts, _ := serializer.Default.Marshal(baseMsg)
		baseMsg.Reset()
		g.baseMsgPool.Put(baseMsg)

		err = conn.Write(ctx, websocket.MessageBinary, bts)
		if err != nil {
			cancelFunc(fmt.Errorf("conn.Read Write:%w", err))
			return
		}
	}
}

func (g *Gate) ping(baseMsg *gatepb.BaseMsg, conn *websocket.Conn, liveId string) error {
	now := time.Now()

	reqData := baseMsg.Data
	pinResp := &gatepb.PingResp{Ts: now.UnixMilli(), ForwardTs: now.UnixMilli()}

	pinReq := &gatepb.PingReq{}
	_ = serializer.Default.Unmarshal(reqData, pinReq)
	if pinReq.ForwardToGameName != "" { //指定了转发游戏
		comKey := "room" //这里不写死怎么办??
		suj := consts.SubjectReqRetLiveHouse(pinReq.ForwardToGameName, comKey, liveId)
		_, err := g.natsConn.Request(suj, nil, 5*time.Second) //只需要探测下监听是否存在
		if errors.Is(err, nats.ErrNoResponders) {             //没有监听 则说明游戏不存在了
			pinResp.ForwardTs = time.Now().UnixMilli()
			pinResp.ForwardState = 1 //没有游戏房了
		} else if err != nil {
			baseMsg.Code = -500
			baseMsg.ErrMsg = "server error"
		}
	}

	baseMsg.Data, _ = serializer.Default.Marshal(pinResp)
	bts, _ := serializer.Default.Marshal(baseMsg)
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

func (g *Gate) verifyJwt(tokenString, jwtKey string) (string, error) {
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return []byte(jwtKey), nil
	})
	if err != nil {
		return "", err
	}
	if !token.Valid {
		return "", errors.New("invalid token")
	}
	if m, ok := token.Claims.(jwt.MapClaims); ok {
		if uid, ok := m["uid"]; ok {
			return cast.ToStringE(uid)
		}
		if uid, ok := m["jti"]; ok {
			return cast.ToStringE(uid)
		}
	}
	return "", errors.New("token payload parse fail")
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
	sign := header.Get("sign")
	if ts < time.Now().Unix()-60 || sign == "" || randStr == "" {
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

	//验签规则
	//拼接body+ts+randStr+salt (全部是UTF-8 string)
	//sha256 得到sign
	buffer.WriteString(cast.ToString(ts))
	buffer.WriteString(randStr)
	buffer.WriteString(g.apiSalt)

	mySign := fmt.Sprintf("%X", sha256.Sum256(buffer.Bytes()))
	if mySign != sign {
		http.Error(w, "", http.StatusForbidden)
		return
	}

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
	// 初始化SDK client
	opt := new(credential.Config).
		SetClientKey(g.envBase.DouyinAppId).       // 改成自己的app_id
		SetClientSecret(g.envBase.DouyinAppSecret) // 改成自己的secret
	sdkClient, err := openApiSdkClient.NewClient(opt)
	if err != nil {
		slog.Error("sdk init err:", "err", err)
		return "", errors.New("sdk parse fail")
	}

	/* 构建请求参数，该代码示例中只给出部分参数，请用户根据需要自行构建参数值
	   	token:
	   	   1.若用户自行维护token,将用户维护的token赋值给该参数即可
	          2.SDK包中有获取token的函数，请根据接口path在《OpenAPI SDK 总览》文档中查找获取token函数的名字
	            在使用过程中，请注意token互刷问题
	       header:
	          sdk中默认填充content-type请求头，若不需要填充除content-type之外的请求头，删除该参数即可
	*/
	sdkRequest := &openApiSdkClient.AppsJscode2sessionRequest{}
	sdkRequest.SetAnonymousCode(g.envBase.DouyinAppId)
	sdkRequest.SetCode(loginCode) // 前端获取的code
	sdkRequest.SetAppid(g.envBase.DouyinAppId)
	sdkRequest.SetSecret(g.envBase.DouyinAppSecret)
	// sdk调用
	sdkResponse, err := sdkClient.AppsJscode2session(sdkRequest)
	if err != nil {
		slog.Error("sdk call err:", "err", err)
		return "", errors.New("sdk call fail")
	}
	slog.Error("sdk call sucess:", "sdkResponse", sdkResponse)
	return *sdkResponse.Openid, nil
}
