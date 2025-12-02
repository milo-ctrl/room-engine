package consts

import "strings"

const (
	rdsKeyPrefix = "rme:"
)

// RdsKeyUidComponentInfo uid和关联的Component记录 hash结构
func RdsKeyUidComponentInfo(uid string) string {
	sb := strings.Builder{}
	sb.WriteString(rdsKeyPrefix)
	sb.WriteString("comInfo:")
	sb.WriteString(uid)
	return sb.String()
}

// ComInfo 组件相关信息结构
type ComInfo struct {
	Uid      string `redis:"uid"`
	ComId    string `redis:"comId"`    //组件生成的唯一id
	ComKey   string `redis:"comKey"`   //组件key
	GameName string `redis:"gameName"` //游戏名
	ProcName string `redis:"procName"` //进程名
	LiveId   string `redis:"liveId"`   //直播间id
	CreateTs int64  `redis:"createTs"` //创建时间戳
}

func (ComInfo) Uid_() string {
	return "uid"
}
func (ComInfo) ComId_() string {
	return "comId"
}
func (ComInfo) ComKey_() string {
	return "comKey"
}
func (ComInfo) GameName_() string {
	return "gameName"
}
func (ComInfo) ProcName_() string {
	return "procName"
}
func (ComInfo) LiveId_() string {
	return "liveId"
}
func (ComInfo) CreateTs_() string {
	return "createTs"
}

// RdsKeyLiveIdUids  liveId 和 uid的映射 hash结构
func RdsKeyLiveIdUids(liveId string) string {
	sb := strings.Builder{}
	sb.WriteString(rdsKeyPrefix)
	sb.WriteString("liveUids:")
	sb.WriteString(liveId)
	return sb.String()
}

// RdsKeyProcAll 全部的进程 hash结构 进程名:组件数量
func RdsKeyProcAll(gameName string) string {
	sb := strings.Builder{}
	sb.WriteString(rdsKeyPrefix)
	sb.WriteString("proc:all:")
	sb.WriteString(gameName)
	return sb.String()
}

// RdsKeyProcAlive 进程存活状态 string结构
func RdsKeyProcAlive(procName string) string {
	sb := strings.Builder{}
	sb.WriteString(rdsKeyPrefix)
	sb.WriteString("proc:alive:")
	sb.WriteString(procName)
	return sb.String()
}

// RdsKeyProcDetail 进程详情 hash结构
func RdsKeyProcDetail(procName string) string {
	sb := strings.Builder{}
	sb.WriteString(rdsKeyPrefix)
	sb.WriteString("proc:detail:")
	sb.WriteString(procName)
	return sb.String()
}

type ProcDetail struct {
	Weight        int    `redis:"weight,omitempty"` //权重 应该由后台配置
	ProcName      string `redis:"procName"`         //进程名
	Version       string `redis:"version"`          //服务版本
	ClientVersion string `redis:"clientVersion"`    //最小支持客户端版本
	StartTs       int64  `redis:"startTs"`          //启动时间
}

func (ProcDetail) Weight_() string {
	return "weight"
}
func (ProcDetail) ProcName_() string {
	return "procName"
}
func (ProcDetail) Version_() string {
	return "version"
}
func (ProcDetail) ClientVersion_() string {
	return "clientVersion"
}
func (ProcDetail) StartTs_() string {
	return "startTs"
}

// RdsKeyUserOnlineState 用户在线状态 hash结构
func RdsKeyUserOnlineState(uid string) string {
	sb := strings.Builder{}
	sb.WriteString(rdsKeyPrefix)
	sb.WriteString("ol:")
	sb.WriteString(uid)
	return sb.String()
}

// RdsKeyOpenapiNoReplay 对外提供的api的防重放检测
func RdsKeyOpenapiNoReplay(randStr string) string {
	sb := strings.Builder{}
	sb.WriteString(rdsKeyPrefix)
	sb.WriteString("apiNoRep:")
	sb.WriteString(randStr)
	return sb.String()
}
