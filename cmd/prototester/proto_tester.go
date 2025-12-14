package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/milo-ctrl/room-engine/consts"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
	"github.com/nats-io/nats.go"
)

type protoData struct {
	GameName string
	Req      string
	Uid      string
	LiveId   string
}

func main() {
	host := ""
	flag.StringVar(&host, "host", "127.0.0.1", "nats连接地址")
	flag.Parse()
	conn, err := nats.Connect(fmt.Sprintf("nats://%s:4222", host))
	if err != nil {
		panic(err)
	}
	a := app.NewWithID("proto-tester-5-7")
	prefs := a.Preferences()
	w := a.NewWindow("协议测试 nats:" + host)

	pd := make(map[string]protoData)
	his := prefs.StringListWithFallback("protoHis", []string{})
	pdStr := prefs.StringWithFallback("protoData", "{}")
	_ = json.Unmarshal([]byte(pdStr), &pd)

	selectProtocol := widget.NewSelectEntry(his)
	if len(his) > 0 {
		selectProtocol.Text = his[0]
	}
	selectProtocol.SetMinRowsVisible(1)

	//游戏
	game := widget.NewMultiLineEntry()
	game.Text = pd[selectProtocol.Text].GameName
	game.SetMinRowsVisible(1)

	//uid框
	uidE := widget.NewMultiLineEntry()
	uidE.Text = pd[selectProtocol.Text].Uid
	uidE.SetMinRowsVisible(1)
	//liveId框
	liveIdE := widget.NewMultiLineEntry()
	liveIdE.Text = pd[selectProtocol.Text].LiveId
	liveIdE.SetMinRowsVisible(1)

	// JSON 输入框
	jsonInput := widget.NewMultiLineEntry()
	jsonInput.Text = pd[selectProtocol.Text].Req
	jsonInput.SetMinRowsVisible(5)

	selectProtocol.OnChanged = func(s string) {
		if s == "" {
			return
		}
		newProData, ok := pd[s]
		if ok {
			game.SetText(newProData.GameName)
			jsonInput.SetText(newProData.Req)
		}
	}

	// JSON 输出框
	jsonOutput := widget.NewMultiLineEntry()
	jsonOutput.SetPlaceHolder("返回值将显示在这里...")
	jsonOutput.SetMinRowsVisible(15)

	// 发送请求函数
	sendRequest := func() {
		protocol := selectProtocol.Text
		if protocol == "" {
			jsonOutput.SetText("错误：请选择或输入有效的协议！")
			return
		}

		slices.DeleteFunc(his, func(s string) bool {
			return s == protocol
		})
		his = slices.Insert(his, 0, protocol)
		if len(his) > 50 {
			his = his[:50]
		}
		selectProtocol.SetOptions(his)
		selectProtocol.SetText(protocol)
		pd[protocol] = protoData{
			GameName: game.Text,
			Req:      jsonInput.Text,
			Uid:      uidE.Text,
			LiveId:   liveIdE.Text,
		}

		sp := strings.Split(protocol, ".")

		nMsg := &nats.Msg{
			Subject: consts.SubjectReqRet(game.Text, sp[0], "1"),
			Data:    []byte(jsonInput.Text),
			Header: nats.Header{
				consts.HeaderCmd:    []string{protocol},
				consts.HeaderUid:    []string{uidE.Text},
				consts.HeaderLiveId: []string{liveIdE.Text},
			},
		}
		resp, err := conn.RequestMsg(nMsg, 2*time.Second)
		if err != nil {
			jsonOutput.SetText(fmt.Sprintf("请求出错:%v", err))
			return
		}
		respData := map[string]any{}
		if len(resp.Data) > 0 {
			if err := json.Unmarshal(resp.Data, &respData); err != nil {
				jsonOutput.SetText(fmt.Sprintf("Unmarshal出错:%v", err))
				return
			}
		}
		if resp.Header == nil {
			jsonOutput.SetText("header nil")
			return
		}

		m := map[string]any{
			"data":   respData,
			"header": resp.Header,
		}

		bytes, _ := json.MarshalIndent(m, "", "  ")
		// 显示响应数据
		jsonOutput.SetText(string(bytes))
	}

	// 发送按钮
	sendButton := widget.NewButton("发送", sendRequest)

	// 布局
	content := container.NewVBox(
		widget.NewLabel("游戏名"),
		game,
		widget.NewLabel("协议名"),
		selectProtocol,
		container.NewGridWithColumns(2,
			container.NewVBox(widget.NewLabel("uid"), uidE),
			container.NewVBox(widget.NewLabel("liveId"), liveIdE),
		),

		widget.NewLabel("请求"),
		jsonInput,
		sendButton,
		widget.NewLabel("响应"),
		jsonOutput,
	)

	w.SetContent(content)
	sr := prefs.StringWithFallback("mainSize", `{"Width":500,"Height":600}`)
	size := fyne.Size{}
	_ = json.Unmarshal([]byte(sr), &size)
	w.Resize(size)

	w.SetOnClosed(func() {
		prefs.SetStringList("protoHis", his)
		bytes, _ := json.Marshal(pd)
		prefs.SetString("protoData", string(bytes))
		size := w.Canvas().Size()
		sb, _ := json.Marshal(size)
		prefs.SetString("mainSize", string(sb))
	})
	w.ShowAndRun()
}
