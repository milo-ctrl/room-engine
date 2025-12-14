package examples

import (
	"fmt"
	"reflect"
	"testing"

	rme "github.com/milo-ctrl/room-engine"
	"github.com/milo-ctrl/room-engine/mongofx"

	"github.com/jinzhu/copier"
	"github.com/redis/go-redis/v9"
)

func TestCommonV2(t *testing.T) {
	rme.Run(
		rme.WithFxOptions(mongofx.Module),
		rme.WithComponentConstructor(NewRoom),
	)
}

// nats request 'gm.ct.bingo.bingo1.room' '{"Teams":[{"Uids":["1","2"],"liveId":"live"}]}'
func NewRoom(redis *redis.Client) *Room {
	return &Room{
		BaseComponent: rme.BaseComponent{ID: "ssssssssss"},
		Redis:         redis,
		M:             make(map[string]any),
	}
}

type Room struct {
	rme.BaseComponent
	lastPlayCard int32
	Redis        *redis.Client
	M            map[string]any
}

var _ rme.Handler = &Room{}

func (r *Room) ComponentDetail() any {
	//TODO implement me
	panic("implement me")
}

// 实现handler
func (r *Room) Key() string {
	return "room"
}

// 定义handle的名称 以  Handle开头   以及ctx第一个参数  参数 和 返回值 推荐全用是指针类型(因为用到了反射,一定会逃逸)
// 如 HandlePlayCard 路由就是 room.PlayCard
//func (r *Room) HandlePlayCard(ctx context.Context, req *ReqPlayCard) (*RetPlayCard, error) {
//	slog.Info("HandlePlayCard", "req", req.String(), "uid", ctx.Value("uid"))
//	r.lastPlayCard = req.Card
//	return &RetPlayCard{}, nil
//}

func TestInjectHandler(t *testing.T) {
	r := NewRoom(redis.NewClient(&redis.Options{}))

	tar := injectHandler(r)

	fmt.Println(tar)
}
func BenchmarkInjectHandler(b *testing.B) {
	r := NewRoom(redis.NewClient(&redis.Options{}))
	for b.Loop() {
		injectHandler(r)
	}
}
func BenchmarkCopy(b *testing.B) {
	r := NewRoom(redis.NewClient(&redis.Options{}))
	for b.Loop() {
		tar := &Room{}
		copier.Copy(tar, r)
	}
}

func injectHandler(src rme.Handler) rme.Handler {
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
	return newVal.Interface().(rme.Handler)
}
