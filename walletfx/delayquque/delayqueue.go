package delayqueue

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// 延时队列 zset 计算执行时间

type DelayQueue struct {
	redis    *redis.Client
	callback func(id string, data []byte) bool
	fail     func(id string, data []byte)
	started  bool
	topic    string
}

type Task struct {
	// 任务Id
	ID string `json:"id" redis:"id"`
	// 携带数据
	Data []byte `json:"data" redis:"data"`
	// 延时
	Delay time.Duration `json:"delay" redis:"delay"`
	// 重试次数
	MaxRetry int `json:"maxRetry" redis:"maxRetry"`
	// 当前执行次数
	CurrentRetry int `json:"currentRetry" redis:"currentRetry"`
	// 重试间隔
	RetryInterval int `json:"retryInterval" redis:"retryInterval"`
}

// 创建一个延时队列
// topic 在同一个redis实例里保持唯一
// event 事件回调如果返回值是false事件会进入重试队列
// fail 最终投递失败的事件回调
// 队列事件保证不丢失但是可能重复投递
func NewDelayQueue[T any](redis *redis.Client, topic string, event func(id string, data T) bool, fail func(id string, data T)) (dq *DelayQueue, err error) {
	dq = &DelayQueue{redis: redis, topic: topic}
	dq.callback = func(id string, data []byte) bool {
		var dataT T
		err := json.Unmarshal(data, &dataT)
		if err != nil {
			slog.Error("反序列化数据失败", "err", err)
			return false
		}
		return event(id, dataT)
	}
	dq.fail = func(id string, data []byte) {
		var dataT T
		err := json.Unmarshal(data, &dataT)
		if err != nil {
			slog.Error("反序列化数据失败", "dt", dataT, "data", string(data), "err", err)
			return
		}
		fail(id, dataT)
	}
	// 启动内部定时任务
	dq.loop()
	return dq, nil
}

// hash
func (dq *DelayQueue) keyTask(id string) string {
	return dq.topic + ":" + id
}

// zset
func (dq *DelayQueue) keyDelay() string {
	return dq.topic + ":delay"
}

// zset
func (dq *DelayQueue) keyPending() string {
	return dq.topic + ":pending"
}

// zset
func (dq *DelayQueue) keyProcess() string {
	return dq.topic + ":process"
}

// Push 推送任务 id 数据 延时 重试次数 重试间隔,注意这里的data需要是json可序列化的
func (dq *DelayQueue) Push(id string, data any, delay time.Duration, retry int, retryInterval time.Duration) {
	// 创建一个任务
	// 序列化数据
	dataBytes, err := json.Marshal(data)
	if err != nil {
		slog.Error("序列化数据失败", "err", err)
		return
	}
	task := &Task{
		ID:            id,
		Data:          dataBytes,
		Delay:         delay,
		MaxRetry:      retry,
		RetryInterval: int(retryInterval.Seconds()),
	}

	_, err = dq.redis.Pipelined(context.Background(), func(pipe redis.Pipeliner) error {
		pipe.HMSet(context.Background(), dq.keyTask(id), task)
		pipe.Expire(context.Background(), dq.keyTask(id), 24*time.Hour)
		return nil
	})

	if err != nil && dq.fail != nil {
		slog.Error("task 保存失败", "err", err)
		dq.fail(id, dataBytes)
		return
	}

	// 加入到延迟队列
	err = dq.redis.ZAdd(context.Background(), dq.keyDelay(), redis.Z{Member: id, Score: float64(time.Now().Unix() + int64(delay.Seconds()))}).Err()
	if err != nil && dq.fail != nil {
		slog.Error("task 加入延迟队列失败", "err", err)
		dq.fail(id, dataBytes)
		return
	}
	return
}

// 移除一个未投递的延迟任务
func (dq *DelayQueue) Remove(id string) {
	// 移除任务信息
	dq.delTask(id)
	// 移除待确认队列
	dq.redis.ZRem(context.Background(), dq.keyPending(), id)
	// 移除延迟队列
	dq.redis.ZRem(context.Background(), dq.keyDelay(), id)
}

func (dq *DelayQueue) loop() {
	go dq.loopMoveWait2Process()
	go dq.loopMovePending2Wait()
	go dq.process()
}

// 抢锁,每10s抢一次执行锁
func (dq *DelayQueue) lock(key string, lockId string) bool {
	key = fmt.Sprintf("%s:%d", key, time.Now().Unix()/10)
	dq.redis.SetNX(context.Background(), key, lockId, time.Second*10)
	return dq.redis.Get(context.Background(), key).Val() == lockId
}

// 循环从定时队列捞数据
func (dq *DelayQueue) loopMoveWait2Process() {
	f := func() {
		// 每次抢到锁后会在一定时间里循环执行直到锁丢失
		ids := dq.redis.ZRangeByScore(context.Background(), dq.keyDelay(), &redis.ZRangeBy{
			Min:   "0",
			Max:   fmt.Sprintf("%d", time.Now().Unix()),
			Count: 100,
		}).Val()
		for _, id := range ids {
			// 插入到待确认队列 给10s的执行时间
			err := dq.redis.ZAdd(context.Background(), dq.keyPending(), redis.Z{Member: id, Score: float64(time.Now().Unix() + 10)}).Err()
			if err != nil {
				continue
			}

			// 从定时队列移除
			removeInt := dq.redis.ZRem(context.Background(), dq.keyDelay(), id).Val()
			if removeInt == 1 {
				// 插入执行队列
				dq.redis.ZAdd(context.Background(), dq.keyProcess(), redis.Z{Member: id, Score: float64(time.Now().Unix())})
			}
		}
	}
	for range time.NewTicker(time.Second * 10).C {
		// 从定时队列捞数据
		lockId, _ := uuid.NewV7()
		for range time.NewTicker(time.Second * 1).C {
			if dq.lock(dq.keyDelay(), lockId.String()) {
				f()
			} else {
				break
			}
		}

	}
}

// 从待确认队列捞数据，放回等待队列
func (dq *DelayQueue) loopMovePending2Wait() {
	f := func() {
		// 每次抢到锁后会在一定时间里循环执行直到锁丢失
		ids := dq.redis.ZRangeByScore(context.Background(), dq.keyPending(), &redis.ZRangeBy{
			Min:   "0",
			Max:   fmt.Sprintf("%d", time.Now().Unix()),
			Count: 100,
		}).Val()
		for _, id := range ids {
			// 从待执行队列移除,有可能已经不在了无所谓
			dq.redis.ZRem(context.Background(), dq.keyProcess(), id)
			// 查任务数据
			cmd := dq.redis.HGetAll(context.Background(), dq.keyTask(id))
			err := cmd.Err()
			if err != nil && err != redis.Nil {
				continue
			}

			if err != nil && err == redis.Nil {
				// 任务被移除了？移除pending
				dq.redis.ZRem(context.Background(), dq.keyPending(), id)
				continue
			}

			task := &Task{}
			if err := cmd.Scan(task); err != nil {
				// 应该不会发生
				dq.redis.ZRem(context.Background(), dq.keyPending(), id)
				continue
			}

			if task.CurrentRetry >= task.MaxRetry {
				// 重试次数超过最大重试次数
				if removeInt := dq.redis.ZRem(context.Background(), dq.keyPending(), id).Val(); removeInt == 1 {
					// 移除成功
					dq.fail(id, task.Data)
				}
				continue
			}

			// 重新计算执行时间
			next := time.Now().Add(time.Duration(task.RetryInterval) * time.Second * time.Duration(task.CurrentRetry))

			// 放回延迟队列
			err = dq.redis.ZAdd(context.Background(), dq.keyDelay(), redis.Z{Member: id, Score: float64(next.Unix())}).Err()
			if err != nil {
				continue
			}
			// 从待确认队列移除
			dq.redis.ZRem(context.Background(), dq.keyPending(), id)
		}
	}

	for range time.NewTicker(time.Second * 10).C {
		// 从定时队列捞数据
		lockId, _ := uuid.NewV7()
		for range time.NewTicker(time.Millisecond * 10).C {
			if dq.lock(dq.keyPending(), lockId.String()) {
				f()
			} else {
				break
			}
		}

	}
}

func (dq *DelayQueue) delTask(id string) {
	dq.redis.Del(context.Background(), dq.keyTask(id))
}

// 处理待处理队列数据
func (dq *DelayQueue) process() {
	for {
		result := dq.redis.BZPopMin(context.Background(), time.Second*60, dq.keyProcess())
		if result.Err() != nil {
			continue
		}
		id := result.Val().Member.(string)
		if id == "" {
			continue
		}

		// 获取任务数据
		cmd := dq.redis.HGetAll(context.Background(), dq.keyTask(id))
		if err := cmd.Err(); err != nil {
			slog.Error("获取任务数据失败", "err", err)
			continue
		}
		task := &Task{}
		if err := cmd.Scan(task); err != nil {
			continue
		}
		if task.Data == nil {
			data, _ := cmd.Result()
			slog.Error("任务数据为空", "id", id, "data", data)
			continue
		}
		// 处理数据
		ack := dq.callback(id, task.Data)
		dq.redis.HIncrBy(context.Background(), dq.keyTask(id), "currentRetry", 1)
		if ack {
			// 移除任务
			dq.delTask(id)
			// 移除pending队列
			dq.redis.ZRem(context.Background(), dq.keyPending(), id)
		} else {
			// 根据设置的间隔时间放回延迟队列
			if task.CurrentRetry >= task.MaxRetry && dq.fail != nil {
				// 移除任务
				dq.delTask(id)
				// 移除pending队列
				removeInt := dq.redis.ZRem(context.Background(), dq.keyPending(), id).Val()
				if removeInt == 1 {
					// 执行失败
					dq.fail(id, task.Data)
				}
				continue
			} else {
				// 计算下一次时间并重新放回去
				next := time.Now().Add(time.Duration(task.RetryInterval) * time.Second * time.Duration(task.CurrentRetry))
				err := dq.redis.ZAdd(context.Background(), dq.keyDelay(), redis.Z{Member: id, Score: float64(next.Unix())}).Err()
				if err != nil {
					// 给pending队列自动恢复的机会
					continue
				} else {
					// 移除pending队列
					dq.redis.ZRem(context.Background(), dq.keyPending(), id)
				}
			}
		}
	}
}
