package mongofx

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/qiniu/qmgo"
	"github.com/spf13/viper"
	"go.uber.org/fx"
)

var Module = fx.Module("mongo-fx", fx.Provide(config2mdbOpt, fx.Private), fx.Provide(NewClient))

func NewClient(config *qmgo.Config) (*qmgo.Client, error) {
	ctx, cancelFunc := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelFunc()
	cli, err := qmgo.NewClient(ctx, config)
	if cli != nil {
		err = errors.Join(err, cli.Ping(3))
	}
	return cli, err
}
func config2mdbOpt(v *viper.Viper) *qmgo.Config {
	mgoURL := getMgoURL(
		v.GetString("MONGO_HOST"),
		v.GetString("MONGO_USER"),
		v.GetString("MONGO_PASSWORD"),
		v.GetInt("MONGO_PORT"),
	)
	return &qmgo.Config{Uri: mgoURL}
}

func getMgoURL(ip, user, password string, port int) string {
	urlString := ""
	if user == "" && password == "" {
		urlString = fmt.Sprintf("mongodb://%s:%d/admin?retryWrites=false", ip, port)
	} else {
		urlString = fmt.Sprintf("mongodb://%s:%s@%s:%d/admin?retryWrites=false", url.QueryEscape(user), url.QueryEscape(password), ip, port)
	}
	return urlString
}
