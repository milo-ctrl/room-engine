package examples

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	rme "gitlab-code.v.show/bygame/room-engine"
	"gitlab-code.v.show/bygame/room-engine/examples/userpb"
	"gitlab-code.v.show/bygame/room-engine/gatefx"
	"gitlab-code.v.show/bygame/room-engine/walletfx"
)

// 样例
func TestRpcV2(t *testing.T) {
	rme.Run(
		rme.WithFxOptions(walletfx.Module, gatefx.Module),
		rme.WithRpcHandlerConstructor(NewUserHandle),
		rme.WithFxOptions(),
	)
}

// 对handle提供 构造方法
func NewUserHandle(redis *redis.Client, wallet *walletfx.Wallet) *UserHandle {
	coins := wallet.Coins(context.Background(), []string{"1"})
	slog.Info("查余额", "coins", coins["1"])
	return &UserHandle{
		redis: redis,
	}
}

type UserHandle struct {
	rme.BaseHandle

	redis *redis.Client
}

func (u *UserHandle) Key() string {
	return "user" //定义分分类   如 user  match  order 等等
}

// 定义handle的名称 以  Handle开头   以及ctx第一个参数 参数 和 返回值 推荐全用是指针类型(因为用到了反射,一定会逃逸)
// 如 HandleGetUserInfo 路由就是 user.GetUserInfo (框架识别会忽略大小写差异)
//
// proto文件命名: gameName.componentKey.proto (严格要求)
// proto包名 {componentKey}pb (严格要求)
// 消息结构体  方法名为HandleGetUserInfo 结构体就是GetUserInfo开头 成对出现
// 请求以Req结尾  响应以Resp结尾
// 以上规则约定均是严格要求  不匹配将会找不到路由或者序列化反序列化错误
// 不需要定义路由  只需定义出入参即可
func (u *UserHandle) HandleInnerGetUserInfo(ctx context.Context, req *userpb.GetUserInfoReq) (*userpb.GetUserInfoResp, error) {
	//slog.Info("reqqqq", "req", req.String())
	//u.redis.Get(ctx, "test") //样例redis操作

	return &userpb.GetUserInfoResp{
		Name: rme.GetUidFromContext(ctx),
		Age:  18,
	}, nil

}

func (o *OrderHandle) Key() string {
	return "order"
}
func NewOrderHandle() *OrderHandle {
	return &OrderHandle{}
}

type OrderHandle struct {
	rme.BaseHandle
}

//func (o *OrderHandle) RpcHandleGetOrderInfo(ctx context.Context, req ReqPing) (RetPing, error) {
//	return RetPing{
//		Time: time.Now().Unix(),
//	}, nil
//}

func TestSS(t *testing.T) {
	rme.Go(func() {
		h44()
	})

	time.Sleep(time.Second * 10)
}

func h44() {
	h55()
}
func h55() {
	var s any = "sss"
	i := s.(int)
	fmt.Println(i)
}
