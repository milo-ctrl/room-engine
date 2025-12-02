
### 1. 安装启动 NATS Server

使用 `go install` 安装 NATS Server and Cli

```bash
go install github.com/nats-io/nats-server/v2@latest
go install github.com/nats-io/natscli/nats@latest
```
前台启动
```bash
nats-server
```
后台启动
```bash
nohup nats-server &
```