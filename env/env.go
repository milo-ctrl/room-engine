package env

import (
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strings"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"go.uber.org/fx"
)

// Base 基础的必要的ENV
type Base struct {
	ProcName        string `mapstructure:"PROC_NAME"`
	GameName        string `mapstructure:"GAME_NAME"`
	GameId          string `mapstructure:"GAME_ID"`
	LogLevel        string `mapstructure:"LOG_LEVEL"`
	LogDir          string `mapstructure:"LOG_DIR"`
	Environment     string `mapstructure:"ENVIRONMENT"`
	Version         string `mapstructure:"VERSION"`        //服务版本号
	ClientVersion   string `mapstructure:"CLIENT_VERSION"` //支持的客户端最小版本号
	RdsKeyPrefix    string `mapstructure:"RDS_KEY_PREFIX"` //redis key前缀
	DouyinAppId     string `mapstructure:"DOUYIN_APPID"`
	DouyinAppSecret string `mapstructure:"DOUYIN_APPSECRET"`
	DouyinAppToken  string `mapstructure:"DOUYIN_APPTOKEN"`
}

var Instance *Base

const (
	LogLevelDebug = "debug"
	LogLevelInfo  = "info"
	LogLevelWarn  = "warn"
	LogLevelError = "error"
)

const (
	Dev     = "dev"
	Staging = "staging"    //测试环境
	Prod    = "production" //正式环境
)

var Module = fx.Module("env",
	fx.Provide(NewEnv),
)

func NewEnv() (*viper.Viper, *Base, error) {
	ip, err := internalIP()
	if err != nil {
		return nil, nil, err
	}
	// 读取命令行参数
	pflag.String("PROC_NAME", "", "进程名")
	pflag.String("ENV_PATH", "", "ENV路径")
	pflag.String("ENVIRONMENT", "", "环境")
	pflag.String("SERVE_PORT", "", "服务端口")
	pflag.Parse()
	if err := viper.BindPFlags(pflag.CommandLine); err != nil {
		return nil, nil, err
	}
	viper.SetConfigType("env")
	// 读取配置
	viper.SetConfigFile(filepath.Join(viper.GetString("ENV_PATH"), ".ext.env"))
	if err := viper.ReadInConfig(); err != nil {
		panic(err)
	}
	viper.SetConfigFile(filepath.Join(viper.GetString("ENV_PATH"), ".env"))
	if err := viper.MergeInConfig(); err != nil {
		return nil, nil, err
	}

	inst := &Base{}

	if err := viper.Unmarshal(inst); err != nil {
		return nil, nil, err
	}
	if inst.Environment == "" {
		return nil, nil, errors.New("ENVIRONMENT 不能为空")
	}
	if inst.GameName == "" {
		return nil, nil, errors.New("GAME_NAME 不能为空")
	}
	if inst.ProcName == "" {
		if inst.Environment == Dev {
			inst.ProcName = inst.GameName
		} else {
			return nil, nil, errors.New("PROC_NAME 不能为空")
		}
	} else {
		inst.ProcName = fmt.Sprintf("%s-%s", inst.ProcName, strings.ReplaceAll(ip.String(), ".", ""))
	}
	if inst.LogLevel == "" {
		inst.LogLevel = LogLevelError
	}
	if inst.LogDir == "" {
		inst.LogDir = "."
	}
	Instance = inst
	return viper.GetViper(), inst, nil
}

func internalIP() (net.IP, error) {
	inters, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	for _, inter := range inters {
		if inter.Flags&net.FlagUp != 0 && !strings.HasPrefix(inter.Name, "lo") {
			addrs, err := inter.Addrs()
			if err != nil {
				continue
			}
			for _, addr := range addrs {
				if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
					if ipnet.IP.To4() != nil {
						return ipnet.IP, nil
					}
				}
			}
		}
	}
	return nil, errors.New("未获取到ip")
}
