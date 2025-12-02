package rpc

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/redis/go-redis/v9"
	"github.com/spf13/cast"
	"gitlab-code.v.show/bygame/room-engine/consts"
	"gitlab-code.v.show/bygame/room-engine/consts/baserpcpb"
	"gitlab-code.v.show/bygame/room-engine/natsfx"
	"gitlab-code.v.show/bygame/room-engine/serializer"
)

type Client struct {
	natsConn *natsfx.Conn
	redis    *redis.Client
}

func NewClient(natsConn *natsfx.Conn, redis *redis.Client) *Client {
	return &Client{
		natsConn: natsConn,
		redis:    redis,
	}
}

// CreateComponent 创建组件
// 匹配成功后 向对应的游戏服进行请求
func (c *Client) CreateComponent(ctx context.Context, gameName, comKey string, req *baserpcpb.CreateComponentReq) (*baserpcpb.CreateComponentResp, error) {
	if gameName == "" || comKey == "" || len(req.Teams) <= 0 {
		return nil, errors.New("params illegal")
	}

	//1,查询所有进程
	allProc := c.redis.HKeys(ctx, consts.RdsKeyProcAll(gameName)).Val()
	if len(allProc) <= 0 {
		return nil, errors.New("no proc found of " + gameName)
	}

	//2,查询所有存活的进程 和 进程详情
	detailCmds := make([]*redis.MapStringStringCmd, 0, len(allProc))
	aliveCmds := make([]*redis.IntCmd, 0, len(allProc))
	pip := c.redis.Pipeline()
	for _, procName := range allProc {
		detailCmds = append(detailCmds, pip.HGetAll(ctx, consts.RdsKeyProcDetail(procName)))
		aliveCmds = append(aliveCmds, pip.Exists(ctx, consts.RdsKeyProcAlive(procName)))
	}
	_, err := pip.Exec(ctx)
	if err != nil {
		return nil, err
	}
	procDetails := make([]consts.ProcDetail, len(allProc))
	for i, cmd := range detailCmds {
		_ = cmd.Scan(&procDetails[i])
	}

	m := make(map[int][]string)
	maxWeight := 0
	for idx, procDetail := range procDetails {
		if procDetail.ProcName == "" || aliveCmds[idx].Val() <= 0 { //详情和alive同时都有才行
			continue
		}
		//客户端版本号<服务器版本号 此进程排除掉
		//用于可能的不兼容更新
		if CompareVersion(req.ClientVersion, procDetail.ClientVersion) < 0 {
			continue
		}
		weight := procDetail.Weight
		m[weight] = append(m[weight], procDetail.ProcName)
		if weight >= maxWeight {
			maxWeight = weight
		}
	}
	procs := m[maxWeight]
	if len(procs) <= 0 {
		return nil, errors.New("not found proc of " + gameName)
	}

	procName := procs[rand.IntN(len(procs))]

	reqBts, _ := serializer.Default.Marshal(req)
	nRes, err := c.natsConn.Request(consts.SubjectComponentCreate(gameName, procName, comKey), reqBts, 3*time.Second)
	if err != nil {
		return nil, err
	}
	resp := &baserpcpb.CreateComponentResp{}
	err = serializer.Default.Unmarshal(nRes.Data, resp)
	return resp, err
}

// CompareVersion 比较任意长度的版本号（支持 "1.2.3"、"1.2.0.0"、"2.0" 等）
// 返回值: 1 表示 v1 > v2，-1 表示 v1 < v2，0 表示相等
func CompareVersion(v1, v2 string) int {
	s1 := strings.Split(v1, ".")
	s2 := strings.Split(v2, ".")

	maxLen := len(s1)
	if len(s2) > maxLen {
		maxLen = len(s2)
	}

	for i := 0; i < maxLen; i++ {
		var n1, n2 int
		if i < len(s1) {
			n1, _ = strconv.Atoi(s1[i])
		}
		if i < len(s2) {
			n2, _ = strconv.Atoi(s2[i])
		}
		if n1 > n2 {
			return 1
		} else if n1 < n2 {
			return -1
		}
	}
	return 0
}

// Request 发送通用rpc请求
func (c *Client) Request(gameName, comKey, handle string, req, resp any) error {
	reqBts, err := serializer.Default.Marshal(req)
	if err != nil {
		return err
	}
	msg := &nats.Msg{
		Subject: consts.SubjectReqRet(gameName, comKey, "x"),
		Header:  nats.Header{consts.HeaderCmd: []string{comKey + "." + handle}},
		Data:    reqBts,
	}
	nRes, err := c.natsConn.RequestMsg(msg, 3*time.Second)
	if err != nil {
		return err
	}
	if cast.ToInt(nRes.Header.Get("code")) != 0 {
		return fmt.Errorf("requst failed code:%s,msg:%s", nRes.Header.Get("code"), nRes.Header.Get("msg"))
	}
	return serializer.Default.Unmarshal(nRes.Data, resp)
}

// RequestInnerDisband 请求流局
// 特殊发送方式
func (c *Client) RequestInnerDisband(gameName, comKey, uid, liveId string) error {
	if uid == "" || liveId == "" {
		return errors.New("params illegal")
	}
	msg := &nats.Msg{
		Subject: consts.SubjectReqRetLiveHouse(gameName, comKey, liveId),
		Header: nats.Header{
			consts.HeaderCmd:    []string{comKey + "." + consts.CmdInnerDisband},
			consts.HeaderUid:    []string{uid},
			consts.HeaderLiveId: []string{liveId},
		},
	}
	_, err := c.natsConn.RequestMsg(msg, 5*time.Second)
	return err
}

// RequestInnerSurrender 请求投降
// 特殊发送方式
func (c *Client) RequestInnerSurrender(gameName, comKey, uid, liveId string) error {
	if uid == "" || liveId == "" {
		return errors.New("params illegal")
	}
	msg := &nats.Msg{
		Subject: consts.SubjectReqRet(gameName, comKey, uid),
		Header: nats.Header{
			consts.HeaderCmd:    []string{comKey + "." + consts.CmdInnerSurrender},
			consts.HeaderUid:    []string{uid},
			consts.HeaderLiveId: []string{liveId},
		},
	}
	_, err := c.natsConn.RequestMsg(msg, 5*time.Second)
	return err
}

// RequestUserJoin 用户加入组件
func (c *Client) RequestUserJoin(uid, liveId, componentId string) error {
	if uid == "" || componentId == "" {
		// 检查参数是否为空
		return errors.New("params illegal")
	}
	msg := &nats.Msg{
		Subject: componentId,
		Header: nats.Header{
			consts.HeaderCmd:    []string{"userJoin"},
			consts.HeaderUid:    []string{uid},
			consts.HeaderLiveId: []string{liveId},
		},
	}
	retMsg, err := c.natsConn.RequestMsg(msg, 5*time.Second)
	if err != nil {
		return err
	}
	code := retMsg.Header.Get("code")
	errmsg := retMsg.Header.Get("msg")
	if errmsg != "" {
		return fmt.Errorf("userJoin return err: errmsg:%s,code:%s", errmsg, code)
	}
	return nil
}

// RequestToComponent Request 通过组件id 请求
// 需求背景 不在组件内的用户 需要请求组件内的方法
func (c *Client) RequestToComponent(componentId, comKey, handle string, req, resp any) error {
	if componentId == "" || comKey == "" || handle == "" {
		return errors.New("params illegal")
	}
	reqBts, err := serializer.Default.Marshal(req)
	if err != nil {
		return err
	}
	msg := &nats.Msg{
		Subject: componentId,
		Header: nats.Header{
			consts.HeaderCmd: []string{comKey + "." + handle},
		},
		Data: reqBts,
	}
	nRes, err := c.natsConn.RequestMsg(msg, 3*time.Second)
	if err != nil {
		return err
	}

	if cast.ToInt(nRes.Header.Get("code")) != 0 {
		return fmt.Errorf("requst failed code:%s,msg:%s", nRes.Header.Get("code"), nRes.Header.Get("msg"))
	}
	return serializer.Default.Unmarshal(nRes.Data, resp)
}
