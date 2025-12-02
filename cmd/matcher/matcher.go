package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"

	"github.com/redis/go-redis/v9"
	"gitlab-code.v.show/bygame/room-engine/consts/baserpcpb"
	"gitlab-code.v.show/bygame/room-engine/natsfx"
	"gitlab-code.v.show/bygame/room-engine/rpc"
)

func main() {
	var host string
	var gameName string
	var liveHouseId string
	var num int
	flag.StringVar(&host, "host", "127.0.0.1", "连接ip")
	flag.StringVar(&gameName, "gameName", "defusemaster", "游戏")
	flag.StringVar(&liveHouseId, "liveId", "live100", "直播间id")
	flag.IntVar(&num, "num", 2, "匹配人数")
	flag.Parse()
	if gameName == "" {
		panic("提供参数")
	}

	natsConn, err := natsfx.NewConn(natsfx.NewConnIn{
		NatsUrl: fmt.Sprintf("nats://%s:4222", host),
	})
	if err != nil {
		panic(err)
	}
	rds := redis.NewClient(&redis.Options{
		Addr: host + ":6379",
	})
	_, err = rds.Ping(context.Background()).Result()
	if err != nil {
		panic(err)
	}
	client := rpc.NewClient(natsConn, rds)

	var teams []*baserpcpb.Team
	for i := range num {
		teams = append(teams, &baserpcpb.Team{
			Players: []*baserpcpb.Player{
				{
					Uid:         fmt.Sprintf("test_lzh_20%d", i+1),
					LiveHouseId: fmt.Sprintf("live_lzh20%d", i+1),
					Site:        int32(i + 1),
				},
			},
		})
	}

	req := &baserpcpb.CreateComponentReq{
		ClientVersion: "0.0.0",
		Teams:         teams,
		ModeId:        10202,
	}
	resp, err := client.CreateComponent(context.Background(), gameName, "room", req)
	if err != nil {
		panic(err)
	}
	slog.Info("CreateComponent resp", "resp", resp)
	if resp.Code != 0 {
		panic(resp)
	}
}
