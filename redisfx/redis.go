package redisfx

import (
	"context"
	"crypto/tls"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/spf13/viper"
	"go.uber.org/fx"
)

var Module = fx.Module("redis-fx", fx.Provide(config2RdsOpt, fx.Private), fx.Provide(NewClient))

func config2RdsOpt(v *viper.Viper) redisOptOut {
	opt := redisOptOut{}
	host := v.GetString("REDIS_HOST")
	if host != "" {
		ops := &redis.Options{
			Addr:     v.GetString("REDIS_HOST") + ":" + v.GetString("REDIS_PORT"),
			Password: v.GetString("REDIS_PASSWORD"),
			DB:       v.GetInt("REDIS_DB"),
			PoolSize: v.GetInt("REDIS_POOL_SIZE"),
		}
		if v.GetInt("REDIS_TLS") == 1 {
			ops.TLSConfig = &tls.Config{}
		}
		opt.RedisOpt = ops
	}
	return opt
}

type RedisOptIn struct {
	fx.In
	RedisOpt *redis.Options `name:"RedisOpt" optional:"true"`
}
type redisOptOut struct {
	fx.Out
	RedisOpt *redis.Options `name:"RedisOpt" optional:"true"`
}

type Result struct {
	fx.Out
	Redis *redis.Client
}

func NewClient(params RedisOptIn) (Result, error) {
	result := Result{}

	result.Redis = redis.NewClient(params.RedisOpt)
	timeout, cancelFunc := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelFunc()
	if err := result.Redis.Ping(timeout).Err(); err != nil {
		return result, err
	}
	return result, nil
}
