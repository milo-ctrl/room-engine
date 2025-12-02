package consts

import "strings"

func EventTopicUserConnChanged(uid string) string {
	sb := strings.Builder{}
	sb.WriteString("UserConnChanged")
	sb.WriteString(".")
	sb.WriteString(uid)
	return sb.String()
}

// EventUserConnChanged 用户连接状态变更
// 网关发出
type EventUserConnChanged struct {
	Uid    string `json:"uid"`    //uid
	LiveId string `json:"liveId"` //直播间id
	State  int    `json:"state"`  //0下线 1上线
	ConnId string `json:"connId"` //为每个ws链接生成的唯一id
}

const (
	// EventTopicComponentRunning 组件运行中
	EventTopicComponentRunning = "ComponentRunning"

	// EventTopicUserHeartbeat 用户心跳消息 网关发出
	EventTopicUserHeartbeat = "UserHeartbeat"

	// EventTopicFailedOrder 加/扣 金币的失败订单
	EventTopicFailedOrder = "FailedOrder"

	// EventTopicGameEnd  对局结束 上层抛出
	EventTopicGameEnd = "GameEnd"
)

type EventComponentRunning struct {
	Id       string   `json:"id"`
	ModeId   int32    `json:"modeId"`
	GameName string   `json:"gameName"`
	ComKey   string   `json:"comKey"`
	Uids     []string `json:"uids"`
	LiveIds  []string `json:"liveIds"`
}

type EventUserHeartbeat struct {
	Uid    string `json:"uid"`
	LiveId string `json:"liveId"`
}

type EventFailedOrder[T any] struct {
	GameName  string `json:"gameName"`
	OrderType int    `json:"orderType"` //1 加钱失败 2 退单失败
	OrderData T      `json:"orderData"`
}

type EventGameEnd struct {
	Id       string         `json:"id"`
	ModeId   int32          `json:"modeId"`
	GameName string         `json:"gameName"`
	EndType  int            `json:"endType"` //0:正常结束 1:流局结束 2:异常结束
	Data     []GameEndData  `json:"data"`
	Extra    map[string]any `json:"extra"` //额外数据 有些游戏需要自己的业务数据
}

const (
	EndTypeNormal  = 0 //正常结束
	EndTypeDisband = 1 //流局结束
	EndTypeError   = 2 //异常结束
)

type GameEndData struct {
	Uid       string `json:"uid"`
	LiveId    string `json:"liveId"`
	SubAmount uint64 `json:"subAmount"`
	AddAmount uint64 `json:"addAmount"`
	OrderId   string `json:"orderId"`
}
