package natsfx

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/buraksezer/consistent"
	"github.com/cespare/xxhash/v2"
	"github.com/nats-io/nats.go"
	"github.com/spf13/viper"
	"go.uber.org/fx"
)

var Module = fx.Module("nats-fx",
	fx.Provide(fx.Annotate(func(v *viper.Viper) (string, string, string) {
		return v.GetString("NATS_URL"), v.GetString("NATS_USER"), v.GetString("NATS_PASSWORD")
	}, fx.ResultTags(`name:"NatsUrl" optional:"true"`, `name:"NatsUser" optional:"true"`, `name:"NatsPassword" optional:"true"`)), fx.Private),
	fx.Provide(NewConn),
	fx.Invoke(func(lc fx.Lifecycle, conn *Conn) {
		lc.Append(fx.StopHook(conn.FlushWithContext))
	}),
)

type NewConnIn struct {
	fx.In
	NatsUrl      string `name:"NatsUrl" optional:"true"`
	NatsUser     string `name:"NatsUser" optional:"true"`
	NatsPassword string `name:"NatsPassword" optional:"true"`
}

type Conn struct {
	// 种子服务器的前缀用来过滤一些奇怪的网卡
	seedPrefix string
	// 种子节点
	seed *nats.Conn

	consistent *consistent.Consistent
}

type natsConnsMember struct {
	initAddr string //初始建立时候地址
	conn     *nats.Conn
}

func (m *natsConnsMember) String() string {
	return m.initAddr
}

func (c *Conn) Subscribe(subj string, cb nats.MsgHandler) (*nats.Subscription, error) {
	return c.shardingConn(subj).Subscribe(subj, cb)
}
func (c *Conn) SubscribeSync(subj string) (*nats.Subscription, error) {
	return c.shardingConn(subj).SubscribeSync(subj)
}
func (c *Conn) ChanSubscribe(subj string, ch chan *nats.Msg) (*nats.Subscription, error) {
	return c.shardingConn(subj).ChanSubscribe(subj, ch)
}
func (c *Conn) QueueSubscribe(subj, queue string, cb nats.MsgHandler) (*nats.Subscription, error) {
	return c.shardingConn(subj).QueueSubscribe(subj, queue, cb)
}
func (c *Conn) QueueSubscribeSync(subj, queue string) (*nats.Subscription, error) {
	return c.shardingConn(subj).QueueSubscribeSync(subj, queue)
}
func (c *Conn) ChanQueueSubscribe(subj string, queue string, ch chan *nats.Msg) (*nats.Subscription, error) {
	return c.shardingConn(subj).ChanQueueSubscribe(subj, queue, ch)
}

func (c *Conn) Publish(subj string, data []byte) error {
	return c.shardingConn(subj).Publish(subj, data)
}
func (c *Conn) PublishMsg(msg *nats.Msg) error {
	return c.shardingConn(msg.Subject).PublishMsg(msg)
}
func (c *Conn) Request(subj string, data []byte, timeout time.Duration) (*nats.Msg, error) {
	return c.shardingConn(subj).Request(subj, data, timeout)
}
func (c *Conn) RequestMsg(msg *nats.Msg, timeout time.Duration) (*nats.Msg, error) {
	return c.shardingConn(msg.Subject).RequestMsg(msg, timeout)
}
func (c *Conn) Flush() error {
	var err error
	for _, member := range c.consistent.GetMembers() {
		err = errors.Join(err, member.(*natsConnsMember).conn.Flush())
	}
	err = errors.Join(err, c.seed.Flush())
	return err
}
func (c *Conn) FlushWithContext(ctx context.Context) error {
	var err error
	for _, member := range c.consistent.GetMembers() {
		err = errors.Join(member.(*natsConnsMember).conn.FlushWithContext(ctx), err)
	}
	err = errors.Join(err, c.seed.FlushWithContext(ctx))
	return err
}
func (c *Conn) Drain() error {
	var err error
	for _, member := range c.consistent.GetMembers() {
		err = errors.Join(member.(*natsConnsMember).conn.Drain(), err)
	}
	err = errors.Join(err, c.seed.Drain())
	return err
}
func (c *Conn) FlushTimeout(timeout time.Duration) error {
	var err error
	for _, member := range c.consistent.GetMembers() {
		err = errors.Join(member.(*natsConnsMember).conn.FlushTimeout(timeout), err)
	}
	err = errors.Join(err, c.seed.FlushTimeout(timeout))
	return err
}

func (c *Conn) shardingConn(subj string) *nats.Conn {
	m := c.consistent.LocateKey([]byte(subj))
	if m == nil {
		return c.seed
	}
	return m.(*natsConnsMember).conn
}

// 扩容会自动创建新链接
func (c *Conn) check() {
	nodes := make(map[string]*natsConnsMember, 3)
	for _, member := range c.consistent.GetMembers() {
		connsMember := member.(*natsConnsMember)
		if connsMember.initAddr != connsMember.conn.ConnectedAddr() { //移除平衡过的节点
			c.consistent.Remove(connsMember.initAddr)
		} else {
			nodes[connsMember.initAddr] = connsMember
		}
	}

	// 获取最新服务器节点
	for _, srv := range c.seed.Servers() {
		if !strings.HasPrefix(srv, c.seedPrefix) {
			continue
		}
		addr := strings.TrimPrefix(srv, "nats://")
		_, ok := nodes[addr]
		if !ok { //如果有新加入的节点 会走到这里
			conn, err := connectToNats(srv, c.seed.Opts.User, c.seed.Opts.Password)
			if err != nil {
				continue
			}
			c.consistent.Add(&natsConnsMember{initAddr: conn.ConnectedAddr(), conn: conn})
		} else {
		}
	}
}

func (c *Conn) loopCheck() {
	go func() {
		for range time.Tick(time.Second * 60) {
			c.check()
		}
	}()
}

func connectToNats(url, user, password string) (*nats.Conn, error) {
	if user != "" && password != "" {
		return nats.Connect(url, nats.UserInfo(user, password))
	}
	return nats.Connect(url)
}

// NewConn 获取nats 包装链接
func NewConn(p NewConnIn) (*Conn, error) {
	url := p.NatsUrl
	if url == "" {
		url = nats.DefaultURL
	}
	connect, err := connectToNats(url, p.NatsUser, p.NatsPassword)
	if err != nil {
		return nil, err
	}

	// 创建链接池，并把种子链接放进去
	c := &Conn{
		seed:       connect,
		seedPrefix: strings.Split(url, ".")[0],
	}
	c.consistent = consistent.New(
		[]consistent.Member{&natsConnsMember{initAddr: connect.ConnectedAddr(), conn: connect}},
		consistent.Config{
			Hasher:            hasher{},
			PartitionCount:    797,
			ReplicationFactor: 100,
			Load:              1.25,
		})
	c.check()
	// 启动循环检查
	c.loopCheck()
	return c, nil
}

type hasher struct{}

func (hasher) Sum64(data []byte) uint64 {
	return xxhash.Sum64(data)
}
