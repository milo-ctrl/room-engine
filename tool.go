package rme

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/redis/go-redis/v9"
	"gitlab-code.v.show/bygame/room-engine/consts"
	"gitlab-code.v.show/bygame/room-engine/consts/baserpcpb"
	"gitlab-code.v.show/bygame/room-engine/env"
	"gitlab-code.v.show/bygame/room-engine/natsfx"
	"gitlab-code.v.show/bygame/room-engine/rpc"
	"gitlab-code.v.show/bygame/room-engine/serializer"
	"go.uber.org/zap"
)

var wg sync.WaitGroup

func Go(f func()) {
	wg.Add(1)
	go func() {
		defer func() {
			if err := recover(); err != nil {
				slog.Error("server panic", "err", err, "stack", Stack())
			}
		}()
		defer wg.Done()
		f()
	}()
}

// WaitStop 等待Go出的协程完成或者指定的时间后  退出进程
func WaitStop(d time.Duration) {
	ctx, cancelFunc := context.WithTimeout(context.Background(), d)
	defer cancelFunc()
	go func() {
		wg.Wait()
		cancelFunc()
	}()
	<-ctx.Done()
}

func Stack() string {
	return zap.StackSkip("", 4).String
}

type Helper struct {
	natsConn  *natsfx.Conn
	redis     *redis.Client
	rpcClient *rpc.Client
	envBase   *env.Base
}

func NewHelper(natsConn *natsfx.Conn, redis *redis.Client, client *rpc.Client, envBase *env.Base) *Helper {
	return &Helper{
		natsConn:  natsConn,
		redis:     redis,
		rpcClient: client,
		envBase:   envBase,
	}
}

// MsgToEventName
// EventXXX 获取消息名
// 适用于定义的 {room}pb.EventStart
func (h *Helper) MsgToEventName(msg any) string {
	refType := reflect.TypeOf(msg)
	if refType.Kind() == reflect.Pointer {
		refType = refType.Elem()
	}
	name := refType.String() //例如: roompb.EventStart
	name = strings.ReplaceAll(name, "pb", "")
	sb := strings.Builder{}
	sb.WriteString(h.envBase.GameName)
	sb.WriteString(".")
	sb.WriteString(name)
	return strings.ToLower(sb.String())
}

// PushMsgToClient 发消息给客户端
func (h *Helper) PushMsgToClient(uid string, msg any) error {
	eventName := h.MsgToEventName(msg)
	nMsg := &nats.Msg{
		Subject: consts.SubjectComponentEventUid(uid), //服务端往个人推送的subject 不带游戏名 (有限制不可能同时打开2个游戏)
		Header:  nats.Header{consts.HeaderCmd: []string{eventName}},
	}
	nMsg.Data, _ = serializer.Default.Marshal(msg)
	err := h.natsConn.PublishMsg(nMsg)
	if err != nil { //&& !errors.Is(err, nats.ErrNoResponders)
		slog.Error("PushMsgToClient  err", "uid", uid, "eventName", eventName, "err", err)
	}
	return err
}

// Broadcast 广播消息 包括观众 exceptUids指定的用户 不会收到推送
// 通常用法中 用PushMsgToClient 给当前玩家或者当前阵营发送像手牌这种敏感数据的消息  对其他人广播脱敏数据
func (h *Helper) Broadcast(liveIds []string, msg any, exceptUids ...string) {
	eventName := h.MsgToEventName(msg)
	nMsg := &nats.Msg{
		Header: nats.Header{
			consts.HeaderCmd:        []string{eventName},
			consts.HeaderExceptUids: exceptUids, //需要排除的Uids 在网关层 做校验进行排除
		},
	}
	nMsg.Data, _ = serializer.Default.Marshal(msg)
	for _, liveHouseId := range liveIds {
		nMsg.Subject = consts.SubjectComponentEventLiveHouse(liveHouseId)
		err := h.natsConn.PublishMsg(nMsg)
		if err != nil { //&& !errors.Is(err, nats.ErrNoResponders)
			slog.Error("Broadcast  err", "liveHoseId", liveHouseId, "eventName", eventName, "err", err)
		}
	}
}

// UidIsOnline uid是否在线
func (h *Helper) UidIsOnline(ctx context.Context, uid string) bool {
	return h.redis.Exists(ctx, consts.RdsKeyUserOnlineState(uid)).Val() > 0
}

// GetUserInfo 获取用户信息
// 该方法是请求rpc,上层能缓存内存需要缓存内存 如 游戏开局时候调用一次
func (h *Helper) GetUserInfo(uid string) (*baserpcpb.UserInfo, error) {
	resp := &baserpcpb.GetUserInfoResp{}
	err := h.rpcClient.Request("center", "user", "InnerGetUserInfo", &baserpcpb.GetUserInfoReq{Uid: uid}, resp)
	if err != nil {
		return nil, err
	}
	return resp.UserInfo, nil
}

// GetUserInfoList 获取用户信息
// 该方法是请求rpc,上层能缓存内存需要缓存内存 如 游戏开局时候调用一次
func (h *Helper) GetUserInfoList(uids []string) ([]*baserpcpb.UserInfo, error) {
	resp := &baserpcpb.GetUserInfoListResp{}
	err := h.rpcClient.Request("center", "baserpc", "InnerGetUserInfoList", &baserpcpb.GetUserInfoListReq{Uids: uids}, resp)
	if err != nil {
		return nil, err
	}
	return resp.List, nil
}

const (
	larkMsg = `{"msg_type":"interactive","card":{"config":{"wide_screen_mode":true},"header":{"template":"green","title":{"tag":"plain_text","content":"[%s]启动%s"}},"elements":[{"tag":"markdown","content":"**环境**：%s\n**进程**：%s\n**版本**：%s"},{"tag":"hr"},{"tag":"note","elements":[{"tag":"plain_text","content":"party  game"}]}]}}`
	larkUrl = "https://open.larksuite.com/open-apis/bot/v2/hook/52073365-e9a1-4fc4-b147-4a6fec98c1e7"
)

func larkNotifyStart(gameName, environment, procName, version string) {
	if environment != env.Prod {
		return
	}
	successStr := "成功"
	resp, err := http.Post(larkUrl, "application/json", strings.NewReader(fmt.Sprintf(larkMsg, gameName, successStr, environment, procName, version)))
	if err != nil {
		return
	}
	buffer := bytes.Buffer{}
	io.Copy(&buffer, resp.Body)
	slog.Debug(buffer.String())
	_ = resp.Body.Close()

}
