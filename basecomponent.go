package rme

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"room-engine/consts"
	"room-engine/consts/baserpcpb"
	"room-engine/env"
	"room-engine/eventbus"
	"room-engine/natsfx"
	"room-engine/serializer"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/samber/lo"
	"github.com/spf13/cast"

	"github.com/nats-io/nats.go"
)

type BaseComponent struct {
	//组件的唯一id
	ID string
	// 版本 组件每调用一次OnComponentChanged 版本加1 严格递增
	Ver int
	//主定时器tick周期
	tickInterval time.Duration

	modeId int32

	//这几个BaseComponent中用到了 直接暴露出去算了 上层直接使用 不能修改
	Logger   *slog.Logger
	Helper   *Helper
	Redis    *redis.Client
	NatsConn *natsfx.Conn
	EnvBase  env.Base

	// 消息通道
	inbox chan *nats.Msg
	//代理执行
	invokeChan chan *invoke

	//玩家所有资源
	playerSource sync.Map
	//组件id订阅
	selfSub *nats.Subscription
	//uid liveId映射
	uidLiveIdMap map[string]string

	// context
	ctx context.Context
	// cancel
	cancel context.CancelCauseFunc
	// 组件实例
	instance Handler
	// 销毁
	destroyOnce sync.Once

	//组件消息 用于观众视角的初始化数据
	componentMsgCache any

	clearWg   *sync.WaitGroup
	methodMap map[string]doFunc
}

func (*BaseComponent) OnStart(_ ...any) error {
	return nil
}
func (*BaseComponent) OnUserJoin(_ string, _ []byte) error {
	return nil
}
func (*BaseComponent) OnDestroy(_ ...any) {}
func (*BaseComponent) Tick(_ time.Time) bool {
	return false
}
func (bt *BaseComponent) OnUserConnChanged(_ *consts.EventUserConnChanged) {}

type playerSource struct {
	Uid           string             // 玩家id
	liveId        string             // 玩家订阅的直播间id
	playerSub     *nats.Subscription //玩家订阅
	liveSub       *nats.Subscription //所在直播间的订阅
	connChangeSub *nats.Subscription //连接状态变更事件订阅

	clearOnce sync.Once
}

func (ps *playerSource) clear(bt *BaseComponent, pip redis.Pipeliner) {
	ps.clearOnce.Do(func() {
		ps.playerSub.Unsubscribe()
		ps.connChangeSub.Unsubscribe()
		ps.liveSub.Unsubscribe()

		pip.Del(context.Background(), consts.RdsKeyUidComponentInfo(ps.Uid))

		playerLiveId, ok := bt.uidLiveIdMap[ps.Uid]
		if ok {
			//删除uid,查询剩余的uid是否还包含已经删除的playerLiveId
			//包含 则说明对应的liveId还有其他人在(一个直播间内一起玩的用户)  那么就hdel 当前uid
			//不包含 则说明liveId 只有自己了  那么必须直接删除RdsKeyLiveIdUids,因为当前直播间没人玩游戏了,此key不能存在了
			//注意: RdsKeyLiveIdUids 存的是本局游戏中的所有uid,并不是单纯的只是这个live中的uid 所以要进行上上述步骤
			uniqLiveIds := lo.UniqValues(bt.uidLiveIdMap)
			for _, liveId := range uniqLiveIds {
				pip.HDel(context.Background(), consts.RdsKeyLiveIdUids(liveId), ps.Uid)
			}

			delete(bt.uidLiveIdMap, ps.Uid)
			leftLiveIds := lo.UniqValues(bt.uidLiveIdMap)
			hasOthers := slices.Contains(leftLiveIds, playerLiveId)
			if !hasOthers {
				pip.Del(context.Background(), consts.RdsKeyLiveIdUids(playerLiveId))
			}
		}

		bt.Logger.Info("clear playerSource", "uid", ps.Uid, "liveId", ps.liveId)
	})
}

// Destroy 只会执行一次
func (bt *BaseComponent) Destroy(cause error) {
	bt.destroyOnce.Do(func() {
		defer bt.cancel(errors.Join(ErrCauseComponentCancel, cause))

		bt.instance.OnDestroy(cause)
	})
}

// SetTickInterval
// 设置tick的周期 只能在OnStart中设置
// 不设置 不会tick
func (bt *BaseComponent) SetTickInterval(tickInterval time.Duration) {
	bt.tickInterval = tickInterval
}

func injectNewHandler(src Handler) (Handler, reflect.Value) {
	srcTp := reflect.TypeOf(src)
	srcElem := srcTp.Elem()
	newVal := reflect.New(srcElem)

	srcEle := reflect.ValueOf(src).Elem()
	newEle := newVal.Elem()
	for i := range srcElem.NumField() {
		field := srcElem.Field(i)
		if !field.IsExported() || field.Anonymous { //未导出和匿名都不管
			continue
		}
		newEle.Field(i).Set(srcEle.Field(i))
	}
	return newVal.Interface().(Handler), newEle
}

func onComponentCreate(msg *nats.Msg, handler Handler, e *Engine) {
	defer func() {
		if err := recover(); err != nil {
			slog.Error("component Start panic", "err", err, "stack", Stack())
			createComponentErrResp(msg, -200, "component create panic")
		}
	}()

	//检查传入参数的合法性
	req, uids, _, uidLiveIdMap, illegal := checkCreateComponentReq(msg)
	if illegal {
		return
	}

	ins, insElem := injectNewHandler(handler)

	field := insElem.FieldByName("BaseComponent")
	bt := field.Addr().Interface().(*BaseComponent)
	ctx, cancel := context.WithCancelCause(e.ctx)
	id, _ := uuid.NewV7()
	bt.ID = id.String()
	bt.inbox = make(chan *nats.Msg, 2048)
	bt.invokeChan = make(chan *invoke, 256)
	bt.ctx = ctx
	bt.cancel = cancel
	bt.uidLiveIdMap = uidLiveIdMap
	bt.instance = ins
	bt.Logger = slog.With("cid", bt.ID)
	bt.Redis = e.redis
	bt.NatsConn = e.natsConn
	bt.EnvBase = *e.envBase
	bt.Helper = e.helper
	bt.methodMap = e.methodMap
	bt.clearWg = e.clearWg
	bt.modeId = req.ModeId

	//清理资源
	defer bt.componentClear()

	//提前初始化好playerSource map,
	//后续所有的回退清理都是循环playerSource map,
	for _, uid := range uids {
		bt.playerSource.Store(uid, &playerSource{Uid: uid, liveId: bt.uidLiveIdMap[uid]})
	}

	var code int
	var err error

	defer func() {
		if code != 0 {
			bt.Logger.Error("component create fail", "err", err)
			createComponentErrResp(msg, code, err.Error())
		}
	}()

	//0,订阅组件id subject
	bt.selfSub, err = bt.NatsConn.ChanSubscribe(bt.ID, bt.inbox)
	if err != nil {
		code = -106
		err = fmt.Errorf("subscribe component id err:%w", err)
		cancel(err)
		return
	}

	//合并检查 订阅 写数据  为一个循环中操作 封装针对单个用户的操作
	for _, uid := range uids {
		code, err = bt.initNewUser(uid)
		if err != nil {
			break
		}
	}
	if err != nil {
		cancel(err) //cancel 传递一个cause过去,记录日志
		return
	}

	//4,Flush nats的订阅 防止消息未完全监听成功 客户端就发了消息
	err = bt.NatsConn.FlushTimeout(200 * time.Millisecond) //保证订阅可用
	if err != nil {
		bt.Logger.Error("flushTimeout err", "err", err) //做个记录监控
	}

	//5,启动组件
	err = ins.OnStart(req)
	if err != nil {
		code = -105
		cancel(fmt.Errorf("OnStart err:%w", err)) //cancel 传递一个cause过去,记录日志
		return
	}

	//6,续约相关资源
	bt.renewalResource()

	bt.componentChange(true)

	//7,启动loop
	Go(bt.loop) // 启动loop

	Go(bt.eventComponentInfo) //定时上报对局事件  //组件类型0上报

	resp := &baserpcpb.CreateComponentResp{
		ComponentId: bt.ID,
	}
	seria := serializer.GetSerializer(cast.ToInt(msg.Header.Get(consts.HeaderReqType)))
	bts, _ := seria.Marshal(resp)
	_ = msg.Respond(bts)

	//这里存一些Component的数量
	bt.Redis.Incr(ctx, consts.RdsKeyProcAlive(bt.EnvBase.ProcName))
}

func (bt *BaseComponent) initNewUser(uid string) (code int, err error) {
	//1,检查uid是否可以开局
	if !bt.checkUserIsPlaying(uid) {
		code = -103
		err = fmt.Errorf("uid:[%s] is playing", uid)
		return //发生错误 直接跳出 会走 componentClear进行回退清理
	}
	//2,记录uid和组件的关联信息
	err = bt.setComponentInfo(uid)
	if err != nil {
		code = -104
		return
	}
	//3,订阅资源
	err = bt.subUserSource(uid)
	if err != nil {
		code = -101
		return
	}
	return 0, nil
}

func (bt *BaseComponent) checkUserIsPlaying(uid string) bool {
	//原子命令来保证幂等性
	key := consts.RdsKeyUidComponentInfo(uid)
	nx := bt.Redis.HSetNX(bt.ctx, key, consts.ComInfo{}.Uid_(), uid).Val()
	bt.Redis.Expire(bt.ctx, key, 60*time.Second) //设定一个过期保底
	return nx
}

const (
	componentInfoEx = 120 * time.Second
)

func (bt *BaseComponent) setComponentInfo(uid string) (err error) {
	pipeliner := bt.Redis.Pipeline()
	//设置uid的组件信息
	comInfo := &consts.ComInfo{
		ComId:    bt.ID,
		ComKey:   bt.instance.Key(),
		CreateTs: time.Now().Unix(),
		GameName: bt.EnvBase.GameName,
		ProcName: bt.EnvBase.ProcName,
		LiveId:   bt.uidLiveIdMap[uid],
		Uid:      uid,
	}
	key := consts.RdsKeyUidComponentInfo(uid)
	pipeliner.HSet(bt.ctx, key, comInfo)
	pipeliner.Expire(bt.ctx, key, componentInfoEx)

	//写入直播间id关联的所有uid
	//map结构 value为uid实际所在的直播间
	//应为用户可能处于多个liveId，需要维护所有的liveId中的映射数据 这里需要全部写一遍
	// note:和原逻辑有区别，原始逻辑是一次写入，一次维护的
	uniqLiveIds := lo.UniqValues(bt.uidLiveIdMap)
	for _, liveId := range uniqLiveIds {
		pipeliner.HSet(bt.ctx, consts.RdsKeyLiveIdUids(liveId), uid, bt.uidLiveIdMap[uid])
	}
	pipeliner.Expire(bt.ctx, key, componentInfoEx)

	_, setErr := pipeliner.Exec(bt.ctx)

	if setErr != nil {
		err = fmt.Errorf("setComponentInfo err:%s:%w", uid, setErr)
		return err
	}
	return nil
}
func (bt *BaseComponent) subUserSource(uid string) (err error) {
	val, _ := bt.playerSource.Load(uid)
	ps := val.(*playerSource)
	{
		//订阅主请求subject
		suj := consts.SubjectReqRet(bt.EnvBase.GameName, bt.instance.Key(), uid)
		sub, subErr := bt.NatsConn.ChanSubscribe(suj, bt.inbox)
		if subErr != nil {
			err = fmt.Errorf("SubjectReqRet:%s:%w", uid, subErr) //有一个订阅失败  那么就终止
			return
		}
		ps.playerSub = sub
	}
	{
		//订阅直播间的请求 此类请求不走Component的单线程中
		//例如拉取房间信息 请求量大 独立协程处理
		suj := consts.SubjectReqRetLiveHouse(bt.EnvBase.GameName, bt.instance.Key(), ps.liveId)
		sub, subErr := bt.NatsConn.QueueSubscribe(suj, "liveSub", func(msg *nats.Msg) { //带Queue订阅 重复也没关系 不会重复消息
			cmd := msg.Header.Get(consts.HeaderCmd)
			if cmd == "" {
				_ = msg.Respond(nil)
				return
			}
			Go(func() {
				if msg.Header == nil {
					msg.Header = nats.Header{}
				}
				msg.Header.Set(consts.HeaderLiveId, ps.liveId)
				bt.onComponentRequest(msg)
			})
		})
		if subErr != nil {
			err = fmt.Errorf("SubjectReqRetLiveHouse:%s:%w", uid, subErr) //有一个订阅失败  那么就终止
			return
		}
		ps.liveSub = sub
	}
	{
		//订阅上下线事件
		connChangedTopic := consts.EventTopicUserConnChanged(uid)
		sub, subErr := eventbus.Subscribe(bt.NatsConn, bt.ID, connChangedTopic, func(event *consts.EventUserConnChanged) {
			bt.AfterFunc(0, func() bool {
				bt.instance.OnUserConnChanged(event)
				return true
			})

		})
		if subErr != nil {
			err = fmt.Errorf("EventTopicUserConnChanged:%s:%w", uid, subErr) //有一个订阅失败  那么就终止
			return
		}
		ps.connChangeSub = sub
	}

	return nil
}

func (bt *BaseComponent) renewalResource() {
	//对ComponentInfo续约
	Go(func() {
		tick := time.Tick(componentInfoEx/2 - 10*time.Second)
		for {
			select {
			case <-tick:
				{
					_, _ = bt.Redis.Pipelined(bt.ctx, func(pipe redis.Pipeliner) error {
						bt.playerSource.Range(func(k, v any) bool {
							uid := k.(string)
							ps := v.(*playerSource)
							pipe.Expire(bt.ctx, consts.RdsKeyUidComponentInfo(uid), componentInfoEx)
							pipe.Expire(bt.ctx, consts.RdsKeyLiveIdUids(ps.liveId), componentInfoEx)
							return true
						})
						return nil
					})
				}
			case <-bt.ctx.Done():
				return
			}
		}
	})
}

func checkCreateComponentReq(msg *nats.Msg) (*baserpcpb.CreateComponentReq, []string, []string, map[string]string, bool) {
	req := &baserpcpb.CreateComponentReq{}
	seria := serializer.GetSerializer(cast.ToInt(msg.Header.Get(consts.HeaderReqType)))
	_ = seria.Unmarshal(msg.Data, req)
	if len(req.Teams) <= 0 {
		createComponentErrResp(msg, -100, "team is empty")
		return nil, nil, nil, nil, true
	}

	liveIds := make([]string, 0, len(req.Teams))
	uidLiveIdMap := make(map[string]string, len(req.Teams))
	for _, team := range req.Teams {
		for _, player := range team.Players {
			if player.Uid == "" || player.LiveHouseId == "" {
				createComponentErrResp(msg, -100, "uid or liveHouseId has empty")
				return nil, nil, nil, nil, true
			}
			liveIds = append(liveIds, player.LiveHouseId)
			uidLiveIdMap[player.Uid] = player.LiveHouseId
		}
	}
	uids := lo.Keys(uidLiveIdMap)
	if len(uids) <= 0 {
		createComponentErrResp(msg, -100, "uids is empty")
		return nil, nil, nil, nil, true
	}
	liveIds = lo.Uniq(liveIds)
	if len(liveIds) <= 0 {
		createComponentErrResp(msg, -100, "liveHouseId is empty")
		return nil, nil, nil, nil, true
	}
	return req, uids, liveIds, uidLiveIdMap, false
}

func createComponentErrResp(msg *nats.Msg, code int, errMsg string) {
	resp := &baserpcpb.CreateComponentResp{
		Code:   int32(code),
		ErrMsg: errMsg,
	}
	seria := serializer.GetSerializer(cast.ToInt(msg.Header.Get(consts.HeaderReqType)))
	bts, _ := seria.Marshal(resp)
	_ = msg.Respond(bts)
}

func (bt *BaseComponent) componentClear() {
	//cancel 后自动清理
	bt.clearWg.Add(1)
	context.AfterFunc(bt.ctx, func() {
		defer bt.clearWg.Done()

		bt.selfSub.Unsubscribe()

		pip := bt.Redis.Pipeline().Pipeline()

		//component销毁 数量-1  //防止进程停止key被del的情况下 又incr导致key误写入
		if errors.Is(context.Cause(bt.ctx), ErrCauseComponentCancel) {
			pip.Decr(context.Background(), consts.RdsKeyProcAlive(bt.EnvBase.ProcName))
			pip.Expire(context.Background(), consts.RdsKeyProcAlive(bt.EnvBase.ProcName), 120*time.Second) //再防护一下
		}

		bt.playerSource.Range(func(k, v any) bool {
			ps := v.(*playerSource)
			ps.clear(bt, pip)
			return true
		})
		_, err := pip.Exec(context.Background())
		if err != nil {
			bt.Logger.Error("clear component exec redis err", "err", err, "id", bt.ID)
		}

		bt.Logger.Info("clear component", "cause", context.Cause(bt.ctx))
	})
}

// 事件上报对局信息
func (bt *BaseComponent) eventComponentInfo() {
	uids := make([]string, 0)
	liveIds := make([]string, 0)
	tick := time.Tick(30 * time.Second)
	for {
		select {
		case <-tick:
			{
				bt.playerSource.Range(func(key, value any) bool {
					uids = append(uids, key.(string))
					liveIds = append(liveIds, value.(*playerSource).liveId)
					return true
				})
				eventbus.Publish(bt.NatsConn, consts.EventTopicComponentRunning, &consts.EventComponentRunning{
					Id:       bt.ID,
					Uids:     uids,
					LiveIds:  liveIds,
					ModeId:   bt.modeId,
					GameName: bt.EnvBase.GameName,
					ComKey:   bt.instance.Key(),
				})
				uids = uids[:0]
				liveIds = liveIds[:0]
			}
		case <-bt.ctx.Done():
			return
		}
	}
}

func (bt *BaseComponent) loop() {
	end := false
	tick := time.Tick(bt.tickInterval)
	for !end {
		func() {
			defer func() {
				if r := recover(); r != nil {
					bt.Logger.Error("BaseComponent loop panic", "err", r, "stack", Stack())
				}
			}()
			select {
			case msg := <-bt.inbox:
				{
					cmd := msg.Header.Get(consts.HeaderCmd)
					var success bool
					if cmd == "userJoin" {
						success = bt.onUserJoin(msg)
					} else {
						//常规消息
						success = bt.onComponentRequest(msg)
					}
					bt.componentChange(success)
				}
			case ik := <-bt.invokeChan:
				{
					success := ik.fun()
					bt.componentChange(success)
				}
			case t := <-tick:
				{
					success := bt.instance.Tick(t)
					bt.componentChange(success)
				}
			case <-bt.ctx.Done():
				{
					end = true
					return
				}
			}
		}()
	}
}

func (bt *BaseComponent) componentChange(isChange bool) {
	if !isChange {
		return
	}
	bt.Ver++
	bt.componentMsgCache = bt.instance.ComponentDetail()
}

// PushMsgToClient 发消息给客户端
func (bt *BaseComponent) PushMsgToClient(uid string, msg any) error {
	// 这里要判断是否持有玩家订阅
	if _, ok := bt.playerSource.Load(uid); ok {
		return bt.Helper.PushMsgToClient(uid, msg)
	}
	return ErrInvalidUid
}

// Broadcast 广播消息 包括观众 exceptUids指定的用户 不会收到推送
// 通常用法中 用PushMsgToClient 给当前玩家或者当前阵营发送像手牌这种敏感数据的消息  对其他人广播脱敏数据
func (bt *BaseComponent) Broadcast(msg any, exceptUids ...string) {
	var liveIds []string
	bt.playerSource.Range(func(key, value any) bool {
		ps := value.(*playerSource)
		liveIds = append(liveIds, ps.liveId)
		return true
	})
	liveIds = lo.Uniq(liveIds)
	bt.Helper.Broadcast(liveIds, msg, exceptUids...)
}

// 通过字符串方法名调用组件方法
func (bt *BaseComponent) onComponentRequest(nm *nats.Msg) (success bool) {
	success = true
	cmd := nm.Header.Get(consts.HeaderCmd)
	if f, ok := bt.methodMap[strings.ToLower(cmd)]; ok {
		uid := nm.Header.Get(consts.HeaderUid)
		liveId := nm.Header.Get(consts.HeaderLiveId)
		ctx := NewContextWith(bt.ctx, uid, liveId)

		timeout, cancelFunc := context.WithTimeout(ctx, 5*time.Second)
		defer cancelFunc()
		if nm.Header == nil {
			nm.Header = nats.Header{}
		}
		seria := serializer.GetSerializer(cast.ToInt(nm.Header.Get(consts.HeaderReqType)))
		data, rErr := f(timeout, seria, nm.Data, bt.instance)
		nm.Data = nil
		if rErr != nil {
			resErr, ok := rErr.(*ResultError)
			errCode := -1
			if ok {
				errCode = resErr.Code
			}
			nm.Header.Set("code", cast.ToString(errCode))
			nm.Header.Set("msg", rErr.Error())
			success = false
			bt.Logger.Error("handle return errr", "uid", uid, "liveId", liveId, "cmd", cmd, "err", rErr)
		}
		if data != nil {
			dataBts, _ := seria.Marshal(data)
			nm.Data = dataBts
		}
		err := nm.RespondMsg(nm)
		if err != nil {
			bt.Logger.Error("nats Respond err:", "err", err)
		}
	} else {
		bt.Logger.Error("cmd not found", "cmd", cmd)
		nm.Header.Set("code", "-404")
		nm.Header.Set("msg", fmt.Sprintf("router:%s not found handle", cmd))
		nm.Data = nil
		_ = nm.RespondMsg(nm)
		success = false
	}
	return
}

type invoke struct {
	fun func() bool //返回值意义 是否有组件信息变更
	t   time.Time   //任务进队列的时间
}

// AfterFunc 定时任务 duc传0 表示下一个空闲周期立即执行
func (bt *BaseComponent) AfterFunc(duc time.Duration, fun func() bool) {
	ik := &invoke{t: time.Now(), fun: fun}
	if duc <= 0 {
		bt.invokeChan <- ik
		return
	}
	time.AfterFunc(duc, func() {
		bt.invokeChan <- ik
	})

}

// KickUser 踢出玩家
// 移除消息的订阅通道 和 Uids
// 此操作后 玩家的消息不会进入组件内
func (bt *BaseComponent) KickUser(uid string) error {
	value, loaded := bt.playerSource.LoadAndDelete(uid)
	if loaded {
		ps := value.(*playerSource)
		_, _ = bt.Redis.Pipelined(context.Background(), func(pipe redis.Pipeliner) error {
			ps.clear(bt, pipe)
			return nil
		})
		return nil
	} else {
		return ErrInvalidUid
	}
}

// 收到user加入的请求
func (bt *BaseComponent) onUserJoin(nm *nats.Msg) bool {
	uid := nm.Header.Get(consts.HeaderUid)
	liveId := nm.Header.Get(consts.HeaderLiveId)
	code, err := bt.userJoin(uid, liveId, nm.Data)
	if err != nil {
		nm.Header.Set("code", cast.ToString(code))
		nm.Header.Set("msg", err.Error())
		bt.Logger.Error("userJoin return errr", "uid", uid, "liveId", liveId, "err", err)
	}
	nm.Data = nil
	respRrr := nm.RespondMsg(nm)
	if respRrr != nil {
		bt.Logger.Error("nats Respond err:", "err", respRrr)
	}
	return err == nil
}

func (bt *BaseComponent) userJoin(uid, liveId string, data []byte) (int, error) {
	if uid == "" {
		return -301, errors.New("uid is empty")
	}
	_, ok := bt.playerSource.Load(uid)
	if ok {
		return -302, errors.New("uid already in this component")
	}
	bt.uidLiveIdMap[uid] = liveId
	ps := &playerSource{liveId: liveId, Uid: uid}
	bt.playerSource.Store(uid, ps)
	code, err := bt.initNewUser(uid)

	defer func() {
		if err != nil {
			_ = bt.KickUser(uid)
		}
	}()

	if err != nil {
		return code, err
	}

	//回调回去组件
	err = bt.instance.OnUserJoin(uid, data)
	if err == nil {
		return 0, nil
	}
	resErr, ok := err.(*ResultError)
	errCode := -304
	if ok {
		errCode = resErr.Code
	}
	return errCode, err
}

func (bt *BaseComponent) Uids() []string {
	//uids := make([]string, 0)
	//bt.playerSource.Range(func(key, value any) bool {
	//	uids = append(uids, key.(string))
	//	return true
	//})
	return lo.Keys(bt.uidLiveIdMap)
}

// HandleGetComponentDetail 此handle为附加的通用handle 获取获取组件详情
// 外层需要自己将协议定义到对应proto中
// 用于观众视角拿到房间的数据
func (bt *BaseComponent) HandleGetComponentDetail(ctx context.Context, req any) (any, error) {
	return bt.componentMsgCache, nil
}

// HandleInnerDisband 流局
// 外层需要自己将协议定义到对应proto中
func (bt *BaseComponent) HandleInnerDisband(ctx context.Context, req any) (any, error) {
	//HandleInnerDisband 是被外部线程调用的 这里交回组件线程处理
	//检查uid参数
	uid := GetUidFromContext(ctx)
	if uid == "" {
		return nil, errors.New("uid is empty")
	}
	_, ok := bt.playerSource.Load(uid)
	if !ok { //检查uid是不是在组件内
		return nil, errors.New("illegal uid")
	}
	bt.AfterFunc(0, func() bool {
		bt.Destroy(&ErrCauseDisband{Uid: uid})
		return false
	})
	return nil, nil
}
