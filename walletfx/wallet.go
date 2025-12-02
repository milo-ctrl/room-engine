package walletfx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/samber/lo"
	"github.com/spf13/cast"
	"github.com/spf13/viper"
	"gitlab-code.v.show/bygame/room-engine/consts"
	"gitlab-code.v.show/bygame/room-engine/env"
	"gitlab-code.v.show/bygame/room-engine/eventbus"
	"gitlab-code.v.show/bygame/room-engine/natsfx"
	delayqueue "gitlab-code.v.show/bygame/room-engine/walletfx/delayquque"
	"gitlab-code.v.show/bygame/room-engine/walletfx/rpc"
	"go.uber.org/fx"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// 支持方法

// 单人扣款
// 单人结算
// 批量扣款
// 批量结算
// 流局
// 查询单人余额
// 批量查询余额

const (
	// TradeCodeIdempotent 幂等CODE
	TradeCodeIdempotent = 20001

	// TradeCodeIdSubFail 扣款失败
	TradeCodeIdSubFail = 30001

	//TradeCodeIdParamErr 只要服务端这边正常处理，中间出现了业务异常 就会返回 40000
	// 此后 再次调用也会是同样的错误 故不再进行重试调用了
	TradeCodeIdParamErr = 40000

	// TradeCodeIdAddErr 加钱失败
	TradeCodeIdAddErr = 60001
)

var (
	ErrActionEmpty        = errors.New("action is empty")
	ErrActionDuplicateUid = errors.New("action duplicate uid")
	ErrActionAllUidNotSub = errors.New("action all uid not sub")
	ErrOrderNotExit       = errors.New("order not exit")
	ErrOrderAddThanSub    = errors.New("order add than sub")
)

type Wallet struct {
	redis    *redis.Client
	natsConn *natsfx.Conn
	envBse   *env.Base

	// 交易grpc
	walletRpcClient *walletRpc

	// 扣款队列
	subQueue *delayqueue.DelayQueue
	// 保底防止异常状态未结束
	backoffQueue *delayqueue.DelayQueue
	// 结算队列
	addQueue *delayqueue.DelayQueue
	// 退单队列
	refundQueue *delayqueue.DelayQueue
}

var Module = fx.Module("wallet-fx", fx.Provide(NewWallet),
	fx.Provide(fx.Annotate(func(v *viper.Viper) string {
		return v.GetString("PLATFORM_WALLET_URL")
	}, fx.ResultTags(`name:"WalletUrl" optional:"true"`)), fx.Private))

// ActionItem 操作
type ActionItem struct {
	// uid
	Uid string `json:"uid"`
	// 变动值
	Amount uint64 `json:"amount"`

	//交易流水号 统一 方便后续对账
	UserOrderNo string `json:"userOrderNo"`
}

// Order 订单
type Order struct {
	// 订单号
	OrderId string `json:"orderId"`
	// 扣款操作
	Actions []ActionItem `json:"actions"`
}
type NewWalletParams struct {
	fx.In
	Redis     *redis.Client
	NatsConn  *natsfx.Conn
	Env       *env.Base
	WalletUrl string `name:"WalletUrl" optional:"true"`
}

func NewWallet(params NewWalletParams) (*Wallet, error) {
	walletRpcClient, err := newWalletClient(params)
	if err != nil {
		return nil, err
	}
	wallet := &Wallet{
		redis:           params.Redis,
		natsConn:        params.NatsConn,
		envBse:          params.Env,
		walletRpcClient: walletRpcClient,
	}

	// 扣款队列
	subQueue, err := delayqueue.NewDelayQueue(wallet.redis, fmt.Sprintf("%v:wallet:sub", wallet.envBse.RdsKeyPrefix), wallet.processSub, wallet.failSub)
	if err != nil {
		return nil, err
	}
	wallet.subQueue = subQueue

	// 保底补偿队列
	backoffQueue, err := delayqueue.NewDelayQueue(wallet.redis, fmt.Sprintf("%v:wallet:backoff", wallet.envBse.RdsKeyPrefix), wallet.processBackoff, wallet.failBackoff)
	if err != nil {
		return nil, err
	}
	wallet.backoffQueue = backoffQueue

	// 加钱队列
	addQueue, err := delayqueue.NewDelayQueue(wallet.redis, fmt.Sprintf("%v:wallet:add", wallet.envBse.RdsKeyPrefix), wallet.processAdd, wallet.failAdd)
	if err != nil {
		return nil, err
	}
	wallet.addQueue = addQueue

	// 退单队列
	refundQueue, err := delayqueue.NewDelayQueue(wallet.redis, fmt.Sprintf("%v:wallet:refund", wallet.envBse.RdsKeyPrefix), wallet.processRefund, wallet.failRefund)
	if err != nil {
		panic(err)
	}
	wallet.refundQueue = refundQueue

	return wallet, nil
}

// Coins 批量查询余额,保证传入的uid一定会出现在map,即使用户不存在
func (w *Wallet) Coins(ctx context.Context, uids []string) map[string]uint64 {
	result := make(map[string]uint64, len(uids))
	uidArr := make([]string, 0, len(uids))
	for _, uid := range uids {
		uidArr = append(uidArr, uid)
		result[uid] = 0
	}
	coins, err := w.walletRpcClient.GetUsersCoinAccount(ctx, &getUsersCoinAccountRequest{
		UidArr: uidArr,
	})
	if err != nil {
		slog.Error("get user coin account error", "uids", uids, "error", err)
	} else {
		for _, coin := range coins.List {
			result[coin.Uid] = coin.Coin
		}
	}
	return result
}

type subOrder struct {
	OrderId       string   //订单号
	TotalAmount   uint64   //订单总金融 加钱的订单金额 不能超过扣钱的订单金额 否则直接报失败
	IgnoreSubFail bool     //是否是忽略扣款失败 true 需要拆分订单
	ChildOrderId  []string //子订单号 撤单用
}

func (w *Wallet) keySubOrder(orderId string) string {
	sb := strings.Builder{}
	sb.WriteString(w.envBse.RdsKeyPrefix)
	sb.WriteString(":wallet:subOrder:")
	sb.WriteString(orderId)
	return sb.String()
}

func (w *Wallet) keyTotalCtrl(roundId string) string {
	sb := strings.Builder{}
	sb.WriteString(w.envBse.RdsKeyPrefix)
	sb.WriteString(":wallet:totalCtrl:")
	sb.WriteString(roundId)
	return sb.String()
}

// Subs 批量扣款
// ignoreSubFail 忽略失败,当有扣款uid扣款失败的时候 会继续进行扣款
// errors.As 判断是否是ErrSubFail 并拿到失败的uid
func (w *Wallet) Subs(ctx context.Context, orderId string, actions []ActionItem, ignoreSubFail ...bool) error {
	if len(actions) == 0 {
		return ErrActionEmpty
	}
	ignoreSubFailB := false
	if len(ignoreSubFail) > 0 {
		ignoreSubFailB = ignoreSubFail[0]
	}

	sOrder := subOrder{
		OrderId:       orderId,
		IgnoreSubFail: ignoreSubFailB,
		TotalAmount:   0,
	}
	var err error
	if ignoreSubFailB { //如果忽略扣款失败 那么需要拆分这个订单进行传递
		failUids := make([]string, 0, len(actions))
		for _, action := range actions {
			childOrderId := createChildOrderId(orderId, action.Uid)
			action.UserOrderNo = childOrderId //对于拆分订单 UserOrderNo 和外层的一样就行
			subsErr := w.subs(ctx, childOrderId, []ActionItem{action})
			if subsErr != nil {
				failUids = append(failUids, action.Uid)
				err = errors.Join(err, subsErr)
			} else {
				sOrder.ChildOrderId = append(sOrder.ChildOrderId, childOrderId)
				sOrder.TotalAmount += action.Amount
			}
		}
		if len(failUids) > 0 {
			err = errors.Join(err, &ErrSubFail{
				FailUid: failUids,
			})
		}
		if len(failUids) >= len(actions) { //全部失败了 直接返回
			return err
		}
	} else {
		for idx := range actions { //不需要拆分的订单 那就创建一个子id
			actions[idx].UserOrderNo = createChildOrderId(orderId, actions[idx].Uid)
		}
		err = w.subs(ctx, orderId, actions)
		if err != nil {
			return err
		}
		for _, action := range actions {
			sOrder.TotalAmount += action.Amount
		}
	}

	//记录扣款订单的相关信息
	//Add操作的时候 同订单总金额不能超过扣款金额
	//有子订单的时候 需要循环Add子订单id
	sob, _ := json.Marshal(sOrder)
	w.redis.Set(ctx, w.keySubOrder(orderId), string(sob), 3*time.Hour)

	return err
}

func createChildOrderId(orderId string, uid string) string {
	return fmt.Sprintf("%s:%s", orderId, uid)
}

// Subs 批量扣款
func (w *Wallet) subs(ctx context.Context, orderId string, actions []ActionItem) (err error) {
	if len(actions) == 0 {
		return ErrActionEmpty
	}

	// 创建一笔订单
	order := Order{
		OrderId: orderId,
		Actions: actions,
	}

	// 扣款队列，这个队列收到数据后判断完状态应该会转到退单队列
	w.subQueue.Push(orderId, order, 30*time.Second, 3, 30*time.Second)
	// 保底补偿队列
	w.backoffQueue.Push(orderId, order, 3600*2*time.Second, 3, 30*time.Second)
	// 扣款
	return w.rpcSub(ctx, orderId, actions)
}

// Adds 批量加
func (w *Wallet) Adds(ctx context.Context, orderId string, actions []ActionItem) error {
	k := w.keySubOrder(orderId)
	val, err := w.redis.Get(ctx, k).Result()
	if val == "" {
		return errors.Join(ErrOrderNotExit, err)
	}
	i, err := w.redis.Del(ctx, k).Result()
	if i <= 0 {
		return fmt.Errorf("adds err:%w", err)
	}
	var sOrder subOrder
	_ = json.Unmarshal([]byte(val), &sOrder)

	addAmount := lo.Reduce(actions, func(agg uint64, item ActionItem, index int) uint64 {
		return agg + item.Amount
	}, 0)
	if addAmount > sOrder.TotalAmount {
		return ErrOrderAddThanSub
	}
	err = nil
	if sOrder.IgnoreSubFail { //说明有子订单 需要拆分
		for _, action := range actions {
			childOrderId := createChildOrderId(orderId, action.Uid)
			action.UserOrderNo = childOrderId //对于拆分订单 UserOrderNo 和外层的一样就行
			err = errors.Join(err, w.adds(ctx, childOrderId, []ActionItem{action}))
		}
	} else {
		for idx := range actions { //不需要拆分的订单 那就创建一个子id
			actions[idx].UserOrderNo = createChildOrderId(orderId, actions[idx].Uid)
		}
		err = w.adds(ctx, orderId, actions)
	}
	return err
}

// Adds 批量结算
func (w *Wallet) adds(ctx context.Context, orderId string, actions []ActionItem) error {
	if len(actions) == 0 {
		return ErrActionEmpty
	}

	order := Order{
		OrderId: orderId,
	}

	// 有些加钱是0的是不是要移除，肯能是用来关闭订单的
	totalAmount := uint64(0)
	for _, action := range actions {
		if action.Amount == 0 {
			continue
		}
		order.Actions = append(order.Actions, action)
		totalAmount += action.Amount
	}

	// 关闭订单用的不用真发起
	if totalAmount == 0 {
		slog.Info("加钱订单为0，直接关闭", "orderId", orderId)
		w.backoffQueue.Remove(orderId)
		return nil
	}

	// 流传订单状态
	w.addQueue.Push(orderId, order, time.Second*30, 3, time.Second*30)
	// 移除保底队列
	w.backoffQueue.Remove(orderId)

	_, err := w.rpcAdd(ctx, orderId, order.Actions)
	if err != nil {
		return err
	}
	return nil
}

func (w *Wallet) Refund(ctx context.Context, orderId string) {
	k := w.keySubOrder(orderId)
	val, _ := w.redis.Get(ctx, k).Result()
	if val == "" {
		return
	}
	i, _ := w.redis.Del(ctx, k).Result()
	if i <= 0 {
		return
	}
	var sOrder subOrder
	_ = json.Unmarshal([]byte(val), &sOrder)

	if sOrder.IgnoreSubFail { //有子订单 需要拆分
		for _, childOrderId := range sOrder.ChildOrderId {
			w.refund(ctx, childOrderId)
		}
	} else {
		w.refund(ctx, orderId)
	}
}

// Refund 退单,需要传订单号
func (w *Wallet) refund(ctx context.Context, orderId string) {
	// 加入退单队列
	w.refundQueue.Push(orderId, nil, 5*time.Second, 3, 30*time.Second)
	// 移除保底队列
	w.backoffQueue.Remove(orderId)
	// 执行退单
	_, _ = w.rpcRefund(ctx, orderId)
}

// ErrSubFail 扣款失败错误
type ErrSubFail struct {
	FailUid []string //扣失败失败的uid
}

const errSubFailPrefix = "ErrSubFail:"

func (e *ErrSubFail) Error() string {
	buffer := &bytes.Buffer{}
	buffer.WriteString(errSubFailPrefix)
	_ = json.NewEncoder(buffer).Encode(e)
	return buffer.String()
}

// ErrAsBalanceNotEnough
// errMag 解析ErrBalanceNotEnough
// 如果不是,则返回nil
func ErrAsBalanceNotEnough(errMag string) *ErrSubFail {
	sp := strings.Split(errMag, "\n")
	for _, msg := range sp {
		s, found := strings.CutPrefix(msg, errSubFailPrefix)
		if !found {
			continue
		}
		e := &ErrSubFail{}
		_ = json.Unmarshal([]byte(s), e)
		return e
	}
	return nil
}

// 发起扣款
// 执行完后会自动移除扣款队列、保底队列，失败会加入退单队列
func (w *Wallet) rpcSub(ctx context.Context, orderId string, actions []ActionItem) (err error) {
	// 发起扣款
	detail := make([]*rpc.TradeDetail, 0, len(actions))
	for _, action := range actions {
		detail = append(detail, &rpc.TradeDetail{
			Uid:         cast.ToUint32(action.Uid),
			Amount:      uint32(action.Amount),
			UserOrderNo: action.UserOrderNo,
		})
	}

	_, err = w.walletRpcClient.Deduct(ctx, &rpc.TradeRequest{
		Time:         uint32(time.Now().Unix()),
		BizNo:        orderId,
		ResourceType: 1,
		BizType:      1,
		Detail:       detail,
		GameId:       w.envBse.GameId,
	})

	// 接口直接失败，状态已经不确定了所以先加入补单队列然后在移除扣款队列
	st, _ := status.FromError(err)
	if st.Code() != codes.OK && st.Code() != TradeCodeIdempotent {
		if st.Code() != TradeCodeIdSubFail {
			w.refundQueue.Push(orderId, nil, time.Second*30, 3, time.Second*30)
		}
		// 移除扣款队列
		w.subQueue.Remove(orderId)
		// 移除保底队列
		w.backoffQueue.Remove(orderId)

		for _, dt := range st.Details() {
			errorInfo, yes := dt.(*errdetails.ErrorInfo)
			if !yes {
				continue
			}
			uid := errorInfo.Metadata["uid"]
			if uid == "" {
				continue
			}
			return errors.Join(err, &ErrSubFail{FailUid: []string{uid}})
		}
		return err
	}
	// 扣款接口调用成功，移除扣款队列
	w.subQueue.Remove(orderId)

	return nil
}

func (w *Wallet) rpcAdd(ctx context.Context, orderId string, actions []ActionItem) (ack bool, err error) {
	detail := make([]*rpc.TradeDetail, 0, len(actions))
	for _, action := range actions {
		detail = append(detail, &rpc.TradeDetail{
			Uid:         cast.ToUint32(action.Uid),
			Amount:      uint32(action.Amount),
			UserOrderNo: action.UserOrderNo,
		})
	}

	_, err = w.walletRpcClient.Reward(ctx, &rpc.TradeRequest{
		Time:         uint32(time.Now().Unix()),
		BizNo:        orderId,
		ResourceType: 1,
		BizType:      1,
		Detail:       detail,
		GameId:       w.envBse.GameId,
	})
	code := status.Code(err)
	if code != codes.OK && code != TradeCodeIdempotent {
		if code == TradeCodeIdAddErr { //这个是明确失败 上报一个失败  并且不需要去重试了
			e := &consts.EventFailedOrder[Order]{
				GameName:  w.envBse.GameId,
				OrderType: 1,
				OrderData: Order{
					OrderId: orderId,
					Actions: actions,
				},
			}
			eventbus.Publish(w.natsConn, consts.EventTopicFailedOrder, e)

			w.addQueue.Remove(orderId)
		}
		return false, err
	}

	// 移除加钱补偿队列
	w.addQueue.Remove(orderId)

	return true, nil
}

func (w *Wallet) rpcRefund(ctx context.Context, orderId string) (ack bool, err error) {
	// 插入退单队列
	ctx, cancelFunc := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelFunc()

	_, err = w.walletRpcClient.Refund(ctx, &rpc.RefundRequest{
		Time:    uint32(time.Now().Unix()),
		BizNo:   orderId,
		BizType: 1,
		GameId:  w.envBse.GameId,
	})

	code := status.Code(err)
	if code != codes.OK && status.Code(err) != TradeCodeIdempotent {
		// 会自动重试
		slog.Warn("refund rpc error", "orderId", orderId, "error", err)
		return false, err
	}

	// 移除退单队列
	w.refundQueue.Remove(orderId)

	//// 明确错误也不需要在尝试了
	//if refund.Code != 200 && refund.Code != TradeCodeIdempotent {
	//	// 完成了，移除退单队列
	//	return true, errors.New(refund.Message)
	//}
	return true, nil
}

// 投递失败
func (w *Wallet) failSub(id string, data Order) {
	// 这里投递失败可能是redis问题,只能记录日志了
	slog.Error("扣款重试队列投递失败", "id", id)
}

// 延迟30秒检测扣款状态，防止扣款时宕机需要，极低概率走到这里
func (w *Wallet) processSub(id string, data Order) bool {
	// 转入退单队列
	w.refundQueue.Push(id, data, 1*time.Second, 3, 30*time.Second)
	// 直接删除
	return true
}

// 投递失败
func (w *Wallet) failBackoff(id string, data Order) {
	// 这里投递失败可能是redis问题应该不能发生
	slog.Error("保底补偿重试队列投递失败", "id", id, "order", data)
}

func (w *Wallet) processBackoff(id string, data Order) bool {
	// 转入退单队列
	w.refundQueue.Push(id, data, 1*time.Second, 3, 30*time.Second)
	// 直接删除
	return true
}

// 投递失败
func (w *Wallet) failAdd(id string, data Order) {
	// 这里投递失败就可能是真正的多次投递后失败需要记录一条记录
	slog.Error("加钱重试队列投递失败", "id", id, "order", data)
	e := &consts.EventFailedOrder[Order]{
		GameName:  w.envBse.GameId,
		OrderType: 1,
		OrderData: data,
	}
	eventbus.Publish(w.natsConn, consts.EventTopicFailedOrder, e)
}

func (w *Wallet) processAdd(id string, order Order) bool {
	ctx, cancelFunc := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelFunc()
	ack, err := w.rpcAdd(ctx, id, order.Actions)
	code := status.Code(err)
	if code != codes.OK {
		slog.Error("processAdd失败", "id", id, "order", order, "error", err)
	}
	if code == TradeCodeIdParamErr {
		return true
	}
	return ack
}

// 投递失败
func (w *Wallet) failRefund(id string, data Order) {
	slog.Error("退单重试队列投递失败", "id", id, "order", data)
	data.OrderId = id
	e := &consts.EventFailedOrder[Order]{
		GameName:  w.envBse.GameId,
		OrderType: 2,
		OrderData: data,
	}
	eventbus.Publish(w.natsConn, consts.EventTopicFailedOrder, e)
}

func (w *Wallet) processRefund(id string, data Order) bool {
	ctx, cancelFunc := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelFunc()
	ack, err := w.rpcRefund(ctx, id)
	code := status.Code(err)
	if code != codes.OK {
		slog.Error("processRefund失败", "id", id, "order", data, "error", err)
	}
	if code == TradeCodeIdParamErr {
		return true
	}
	return ack
}

// CtrlSub 总量控制的扣款
// 以roundId为基准，一轮内 加钱的数量不能超过扣款数量
func (w *Wallet) CtrlSub(ctx context.Context, roundId string, uid string, Amount uint64) error {
	if roundId == "" || uid == "" || Amount <= 0 {
		return ErrActionEmpty
	}

	orderId := fmt.Sprintf("%s-%s-%d", roundId, uid, rand.Uint32())
	item := ActionItem{Amount: Amount, Uid: uid, UserOrderNo: orderId}
	subsErr := w.subs(ctx, orderId, []ActionItem{item})
	if subsErr != nil {
		return subsErr
	}

	bts, _ := json.Marshal(item)

	pipe := w.redis.Pipeline()
	pipe.HSet(ctx, w.keyTotalCtrl(roundId), orderId, bts)
	pipe.Expire(ctx, w.keyTotalCtrl(roundId), 3*time.Hour)
	_, _ = pipe.Exec(ctx)

	return nil
}

// CtrlAdds 总量控制的加钱
// 以roundId为基准，一轮内 加钱的数量不能超过扣款数量
func (w *Wallet) CtrlAdds(ctx context.Context, roundId string, actions []ActionItem) error {
	if roundId == "" {
		return errors.New("roundId is empty")
	}
	if len(actions) <= 0 {
		return ErrActionEmpty
	}
	k := w.keyTotalCtrl(roundId)

	mapData, e := w.redis.HGetAll(ctx, k).Result()
	if len(mapData) <= 0 {
		return errors.Join(ErrOrderNotExit, e)
	}
	w.redis.Del(ctx, k)

	addTotal := lo.Reduce(actions, func(agg uint64, item ActionItem, index int) uint64 {
		return agg + item.Amount
	}, 0)

	subActions := make([]ActionItem, 0, len(mapData))
	for _, v := range mapData {
		actionItem := ActionItem{}
		_ = json.Unmarshal([]byte(v), &actionItem)
		subActions = append(subActions, actionItem)
	}
	subTotal := lo.Reduce(subActions, func(agg uint64, item ActionItem, index int) uint64 {
		return agg + item.Amount
	}, 0)
	if addTotal > subTotal {
		return ErrOrderAddThanSub
	}

	addActionsMap := lo.SliceToMap(actions, func(act ActionItem) (string, ActionItem) {
		return act.Uid, act
	})
	if len(actions) != len(addActionsMap) {
		return ErrActionDuplicateUid
	}

	//加钱逻辑，应为每一笔订单都需要调用adds(即使是0的订单 也需要调用下，以移除对应的队列)
	//1，传入的actions 为实际需要加钱的uid, 需要从存起来subActions的列表中找到一个（只需要一个就行）对应的扣钱订单号，作为加钱的订单号

	realAddOrderId := make(map[string]string)
	for _, action := range subActions {
		if _, ok := addActionsMap[action.Uid]; ok {
			realAddOrderId[action.Uid] = action.UserOrderNo //任意一个订单号就行
		}
	}
	if len(realAddOrderId) <= 0 {
		return ErrActionAllUidNotSub
	}
	for _, action := range subActions {
		action.Amount = 0
		_ = w.adds(ctx, action.UserOrderNo, []ActionItem{action}) //传入Amount==0 就是关闭订单
	}

	var err error
	for _, action := range actions {
		action.UserOrderNo = realAddOrderId[action.Uid]
		addErr := w.adds(ctx, action.UserOrderNo, []ActionItem{action})
		err = errors.Join(err, addErr)
	}

	return err
}

//// SimpleAdd 简单对加钱的RPC封装
//// 没有任何补偿,重试,风控等逻辑
//// 增加这方法的原因是 派对房房主小费
//func (w *Wallet) SimpleAdd(ctx context.Context, gameId string, actions []ActionItem) (err error) {
//	if len(actions) == 0 {
//		return errors.New("actions is empty")
//	}
//	u, _ := uuid.NewV7()
//	orderId := u.String()
//	detail := make([]*rpc.TradeDetail, 0, len(actions))
//	for _, action := range actions {
//		if action.Amount <= 0 {
//			return errors.New("amount must be greater than 0")
//		}
//		detail = append(detail, &rpc.TradeDetail{
//			Uid:         cast.ToUint32(action.Uid),
//			Amount:      uint32(action.Amount),
//			UserOrderNo: orderId,
//		})
//	}
//
//	_, err = w.walletRpcClient.Reward(ctx, &rpc.TradeRequest{
//		Time:         uint32(time.Now().Unix()),
//		BizNo:        orderId,
//		ResourceType: 1,
//		BizType:      1,
//		Detail:       detail,
//		GameId:       gameId,
//	})
//	return err
//}
