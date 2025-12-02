package main

import (
	"room-engine/env"
	"room-engine/logfx"
	"room-engine/natsfx"
	"room-engine/redisfx"
	"room-engine/rpc"

	"github.com/coder/websocket"
	"go.uber.org/fx"
)

func main() {
	fx.New(natsfx.Module, redisfx.Module, logfx.Module, env.Module,
		fx.Provide(rpc.NewClient),
		fx.Invoke(testHandle),
	).Run()
}

func start(client *rpc.Client) {
	//wg := sync.WaitGroup{}
	//
	//count := 5
	//wg.Add(count)
	//
	//var teams []dto.Team
	//liveId := "live100"
	//for i := 1; i <= count; i++ {
	//	uid := fmt.Sprintf("%d", i)
	//	teams = append(teams, dto.Team{
	//		Uids:        []string{uid},
	//		liveId: liveId,
	//	})
	//	conn, _, err := websocket.Dial(context.Background(), fmt.Sprintf("ws://127.0.0.1:9410/game?token=%s&liveHouseId=%s", uid, liveId), nil)
	//	if err != nil {
	//		panic(err)
	//	}
	//	go func() {
	//		wg.Done()
	//		h := Handle{
	//			conn: conn,
	//		}
	//		for {
	//			_, b, err2 := conn.Read(context.Background())
	//			if err2 != nil {
	//				panic(err2)
	//				return
	//			}
	//			h.dispatch(b)
	//		}
	//	}()
	//}
	//wg.Wait()
	//
	//req := dto.CreateComponentReq{
	//	GameName:     "explodingkittens",
	//	ComponentKey: "room",
	//	Teams:        teams,
	//}
	//resp, err := client.CreateComponent(context.Background(), req)
	//if err != nil {
	//	panic(err)
	//}
	//slog.Debug("CreateComponent resp", "resp", resp)
	//if resp.Code != 0 {
	//	panic(resp)
	//}
}

func testGate() {
	//count := 800
	//
	//var teams []dto.Team
	//liveId := "live100"
	//for i := 1; i <= count; i++ {
	//	time.Sleep(10 * time.Millisecond)
	//	uid := fmt.Sprintf("%d", i)
	//	teams = append(teams, dto.Team{
	//		Uids:        []string{uid},
	//		liveId: liveId,
	//	})
	//	conn, _, err := websocket.Dial(context.Background(), fmt.Sprintf("ws://127.0.0.1:8080/game?token=%s&liveHouseId=live%s", uid, uid), nil)
	//	if err != nil {
	//		panic(err)
	//	}
	//	go func() {
	//		for {
	//			_, bytes, err2 := conn.Read(context.Background())
	//			if err2 != nil {
	//				panic(err2)
	//				return
	//			}
	//			g := &gatefx.BaseMsg{}
	//			json.Unmarshal(bytes, g)
	//			if i == 1 {
	//				slog.Debug("gatefx.BaseMsg", "time", g.Ts)
	//			}
	//
	//		}
	//	}()
	//	go func() {
	//		tick := time.Tick(10 * time.Millisecond)
	//		msg := &gatefx.BaseMsg{
	//			Cmd: "ping",
	//		}
	//		bts, _ := json.Marshal(msg)
	//		for {
	//			select {
	//			case <-tick:
	//				err2 := conn.Write(context.Background(), websocket.MessageBinary, bts)
	//				if err2 != nil {
	//					panic(err2)
	//					return
	//				}
	//			}
	//		}
	//	}()
	//}

	//req := dto.CreateComponentReq{
	//	GameName:     "explodingkittens",
	//	ComponentKey: "room",
	//	Teams:        teams,
	//}
	//resp, err := client.CreateComponent(context.Background(), req)
	//if err != nil {
	//	panic(err)
	//}
	//slog.Debug("CreateComponent resp", "resp", resp)
	//if resp.Code != 0 {
	//	panic(resp)
	//}
}

func testHandle() {
	//count := 800
	//
	//var teams []dto.Team
	//liveId := "live100"
	//for i := 1; i <= count; i++ {
	//	time.Sleep(10 * time.Millisecond)
	//	uid := fmt.Sprintf("%d", i)
	//	teams = append(teams, dto.Team{
	//		Uids:        []string{uid},
	//		liveId: liveId,
	//	})
	//	conn, _, err := websocket.Dial(context.Background(), fmt.Sprintf("ws://127.0.0.1:8080/game?token=%s&liveHouseId=live%s", uid, uid), nil)
	//	if err != nil {
	//		panic(err)
	//	}
	//	go func() {
	//		for {
	//			_, bytes, err2 := conn.Read(context.Background())
	//			if err2 != nil {
	//				panic(err2)
	//				return
	//			}
	//			g := &gatefx.BaseMsg{}
	//			json.Unmarshal(bytes, g)
	//			if i == 1 {
	//				slog.Debug("gatefx.BaseMsg", "g", g)
	//			}
	//
	//		}
	//	}()
	//	go func() {
	//		tick := time.Tick(10 * time.Millisecond)
	//		msg := &gatefx.BaseMsg{
	//			Cmd: "bingo.user.GetUserInfo",
	//		}
	//		bts, _ := json.Marshal(msg)
	//		for {
	//			select {
	//			case <-tick:
	//				err2 := conn.Write(context.Background(), websocket.MessageBinary, bts)
	//				if err2 != nil {
	//					panic(err2)
	//					return
	//				}
	//			}
	//		}
	//	}()
	//}
	//
	////req := dto.CreateComponentReq{
	////	GameName:     "explodingkittens",
	////	ComponentKey: "room",
	////	Teams:        teams,
	////}
	////resp, err := client.CreateComponent(context.Background(), req)
	////if err != nil {
	////	panic(err)
	////}
	////slog.Debug("CreateComponent resp", "resp", resp)
	////if resp.Code != 0 {
	////	panic(resp)
	////}
}

type Handle struct {
	conn *websocket.Conn
}

func (h *Handle) dispatch(b []byte) {
	//baseCmd := &gatefx.BaseMsg{}
	//_ = json.Unmarshal(b, baseCmd)
	//cmdName := baseCmd.Cmd
	//v := reflect.ValueOf(h)
	//method := v.MethodByName(cmdName)
	//if !method.IsValid() {
	//	slog.Debug("无处理方法", "cmd", cmdName)
	//	return
	//}
	//
	//argType := method.Type().In(0).Elem()
	//argInstance := reflect.New(argType).Interface()
	//
	//err := json.Unmarshal(baseCmd.Data, argInstance)
	//if err != nil {
	//	log.Println("proto.Unmarshal err", err)
	//	return
	//}
	//method.Call([]reflect.Value{reflect.ValueOf(argInstance)})
}
