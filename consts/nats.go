package consts

import (
	"strings"
)

const (
	HeaderCmd        = "c"  //nats header中的cmd
	HeaderUid        = "u"  //nats header中的uid
	HeaderExceptUids = "eu" //nats header中的广播需要排除的uid
	HeaderLiveId     = "l"  //nats header中的直播房id
	HeaderReqType    = "rt" //nats header中的请求类型 0/内部普通请求 1/外部api请求
)

const (
	// CmdGetComponentDetail 特殊专用协议 观众获取组件详情
	CmdGetComponentDetail = "GetComponentDetail"

	// CmdInnerDisband 特殊专用协议 流局
	CmdInnerDisband = "InnerDisband"

	// CmdInnerSurrender  特殊专用协议  认输
	CmdInnerSurrender = "InnerSurrender"
)

// SubjectReqRet 组件或者无状态handle请求回复的subject
// 无状态handle uid传入通配符 *
func SubjectReqRet(gameName, comKey, uid string) string {
	sb := strings.Builder{}
	sb.WriteString("gm.req.")
	sb.WriteString(gameName)
	sb.WriteString(".")
	sb.WriteString(comKey)
	sb.WriteString(".")
	sb.WriteString(uid)
	return sb.String()
}

// SubjectReqRetLiveHouse 直播房请求回复的subject
// 特殊的 不能进组件内部的请求
func SubjectReqRetLiveHouse(gameName, comKey, liveId string) string {
	sb := strings.Builder{}
	sb.WriteString("gm.reqLive.")
	sb.WriteString(gameName)
	sb.WriteString(".")
	sb.WriteString(comKey)
	sb.WriteString(".")
	sb.WriteString(liveId)
	return sb.String()
}

// SubjectComponentEventUid 组件主动推送单人的 subject
func SubjectComponentEventUid(uid string) string {
	sb := strings.Builder{}
	sb.WriteString("gm.e.")
	sb.WriteString(uid)
	return sb.String()
}

// SubjectComponentEventLiveHouse 组件主动推送直播房的 subject
func SubjectComponentEventLiveHouse(liveHouseId string) string {
	sb := strings.Builder{}
	sb.WriteString("gm.lh.")
	sb.WriteString(liveHouseId)
	return sb.String()
}

// SubjectComponentCreate 创建组件的 subject
func SubjectComponentCreate(gameName, procName, comKey string) string {
	sb := strings.Builder{}
	sb.WriteString("gm.ct.")
	sb.WriteString(gameName)
	sb.WriteString(".")
	sb.WriteString(procName)
	sb.WriteString(".")
	sb.WriteString(comKey)
	return sb.String()
}

// SubjectSys 系统操作的subject
func SubjectSys(gameName, procName string) string {
	sb := strings.Builder{}
	sb.WriteString("sys.")
	sb.WriteString(gameName)
	sb.WriteString(".")
	sb.WriteString(procName)
	return sb.String()
}

// SubjectEventBus 事件总线的 subject
func SubjectEventBus(topic string) string {
	sb := strings.Builder{}
	sb.WriteString("eventbus.")
	sb.WriteString(topic)
	return sb.String()
}
