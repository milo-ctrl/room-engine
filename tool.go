package rme

import (
	"context"
	"encoding/json"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/milo-ctrl/room-engine/consts"
	"github.com/milo-ctrl/room-engine/consts/baserpcpb"
	"github.com/milo-ctrl/room-engine/env"
	"github.com/milo-ctrl/room-engine/natsfx"
	"github.com/milo-ctrl/room-engine/rpc"

	"github.com/nats-io/nats.go"
	"github.com/redis/go-redis/v9"
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
// 消息以 JSON 格式序列化发送给前端
func (h *Helper) PushMsgToClient(uid string, msg any) error {
	eventName := h.MsgToEventName(msg)

	// 将 proto 消息转为 JSON 字节数组
	msgBytes, err := json.Marshal(msg)
	if err != nil {
		slog.Error("PushMsgToClient Marshal err", "uid", uid, "eventName", eventName, "err", err)
		return err
	}

	nMsg := &nats.Msg{
		Subject: consts.SubjectComponentEventUid(uid), //服务端往个人推送的subject 不带游戏名 (有限制不可能同时打开2个游戏)
		Header:  nats.Header{consts.HeaderCmd: []string{eventName}},
		Data:    msgBytes,
	}
	err = h.natsConn.PublishMsg(nMsg)
	if err != nil { //&& !errors.Is(err, nats.ErrNoResponders)
		slog.Error("PushMsgToClient  err", "uid", uid, "eventName", eventName, "err", err)
	}
	return err
}

// Broadcast 广播消息给指定用户，exceptUids指定的用户不会收到推送
// 通常用法中 用PushMsgToClient 给当前玩家或者当前阵营发送像手牌这种敏感数据的消息  对其他人广播脱敏数据
// 消息以 JSON 格式序列化发送给前端
func (h *Helper) Broadcast(uids []string, msg any, exceptUids ...string) {
	eventName := h.MsgToEventName(msg)

	// 将 proto 消息转为 JSON 字节数组
	msgBytes, err := json.Marshal(msg)
	if err != nil {
		slog.Error("Broadcast Marshal err", "eventName", eventName, "err", err)
		return
	}

	nMsg := &nats.Msg{
		Header: nats.Header{
			consts.HeaderCmd:        []string{eventName},
			consts.HeaderExceptUids: exceptUids, //需要排除的Uids 在网关层 做校验进行排除
		},
		Data: msgBytes,
	}
	for _, uid := range uids {
		nMsg.Subject = consts.SubjectComponentEventUid(uid)
		err := h.natsConn.PublishMsg(nMsg)
		if err != nil { //&& !errors.Is(err, nats.ErrNoResponders)
			slog.Error("Broadcast err", "uid", uid, "eventName", eventName, "err", err)
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
