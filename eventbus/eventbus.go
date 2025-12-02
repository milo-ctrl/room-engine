package eventbus

import (
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/nats-io/nats.go"
	"gitlab-code.v.show/bygame/room-engine/consts"
	"gitlab-code.v.show/bygame/room-engine/natsfx"
	"go.uber.org/zap"
)

// Publish 发布事件
func Publish(natsConn *natsfx.Conn, topic string, event any) {
	bys, _ := json.Marshal(event)
	err := natsConn.Publish(consts.SubjectEventBus(topic), bys)
	if err != nil && !errors.Is(err, nats.ErrNoResponders) {
		slog.Error("eventbus publish err", "topic", topic, "err", err)
	}
}

// Subscribe 订阅事件
// queue 订阅组  和nats的组概念一致 xx.uid
func Subscribe[E any](natsConn *natsfx.Conn, queue, topic string, callback func(event E)) (*nats.Subscription, error) {
	return natsConn.QueueSubscribe(consts.SubjectEventBus(topic), queue, func(msg *nats.Msg) {
		defer func() {
			if err := recover(); err != nil {
				slog.Error("eventbus subscribe panic", "topic", topic, "err", zap.StackSkip("", 3).String)
			}
		}()
		var e = new(E)
		_ = json.Unmarshal(msg.Data, e)
		callback(*e)
	})
}
