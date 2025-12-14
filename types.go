package rme

import (
	"context"
	"errors"
	"reflect"
	"time"

	"github.com/milo-ctrl/room-engine/consts"
	"github.com/milo-ctrl/room-engine/serializer"
)

type Handler interface {
	// Key 唯一标识服 如room chat  等
	Key() string

	// ComponentDetail 组件详情 返回值为 GetComponentDetailResp
	ComponentDetail() any

	//Tick 主定时器回调 返回true 会后续调用 ComponentDetail 以更新观众缓存值
	Tick(time time.Time) bool

	// OnStart 启动前 如果返回的错误不为nil 则启动失败(用于检查初始参数是否合法)
	OnStart(args ...any) error

	// OnUserJoin 用户加入
	OnUserJoin(uid string, arg []byte) error

	// OnDestroy 销毁前
	OnDestroy(args ...any)

	// OnUserConnChanged 用户连接状态变更
	OnUserConnChanged(event *consts.EventUserConnChanged)
}

// BaseHandle 基础基础
// 无状态服务直接组合这个 方便使用
type BaseHandle struct {
}

func (*BaseHandle) OnStart(_ ...any) error {
	return nil
}
func (*BaseHandle) OnUserJoin(_ string, _ []byte) error {
	return nil
}
func (*BaseHandle) OnDestroy(_ ...any) {
}

func (*BaseHandle) ComponentDetail() any {
	return nil
}
func (*BaseHandle) OnUserConnChanged(_ *consts.EventUserConnChanged) {
}
func (*BaseHandle) Tick(_ time.Time) bool {
	return false
}

type doFunc func(context.Context, serializer.Serializer, []byte, Handler) (any, error)

var (
	ErrInvalidUid           = errors.New("invalid uid")
	ErrCauseComponentCancel = errors.New("component cancel")
)

var ctxType = reflect.TypeOf((*context.Context)(nil)).Elem()
var errType = reflect.TypeOf((*error)(nil)).Elem()

type EngineContext context.Context

type ResultError struct {
	Code int
	Msg  string
}

func (e *ResultError) Error() string {
	return e.Msg
}

type ErrCauseDisband struct {
	Uid string //被踢的uid
}

func (e *ErrCauseDisband) Error() string {
	return "ErrCauseDisband:" + e.Uid
}

type baseInfoCtxKey struct{}

type baseInfo struct {
	uid    string
	liveId string
}

func GetUidFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v := ctx.Value(baseInfoCtxKey{}); v != nil {
		if info, ok := v.(*baseInfo); ok {
			return info.uid
		}
	}
	return ""
}
func GetLiveIdFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v := ctx.Value(baseInfoCtxKey{}); v != nil {
		if info, ok := v.(*baseInfo); ok {
			return info.liveId
		}
	}
	return ""
}
func NewContextWith(ctx context.Context, uid, liveId string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, baseInfoCtxKey{}, &baseInfo{
		uid:    uid,
		liveId: liveId,
	})
}
