package logfx

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"time"

	"room-engine/env"

	rotatelogs "github.com/lestrrat-go/file-rotatelogs"
	slogcommon "github.com/samber/slog-common"
	slogzap "github.com/samber/slog-zap/v2"
	"go.uber.org/fx"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var Module = fx.Module("log-fx",
	fx.Provide(NewLogger),
	fx.Provide(parseConfig2Writer),
	fx.Invoke(func(lifecycle fx.Lifecycle, zapLogger *zap.Logger) {
		lifecycle.Append(fx.Hook{
			OnStop: func(ctx context.Context) error {
				return zapLogger.Sync()
			},
		})
	}),
)

func parseConfig2Writer(envBase *env.Base) (io.Writer, error) {
	if envBase.Environment == env.Dev { //本地输出到控制台
		return os.Stdout, nil
	}
	logDir := filepath.Join(envBase.LogDir, "/logs")
	err := os.MkdirAll(logDir, 0755)
	if err != nil {
		return nil, err
	}
	name := envBase.ProcName //进程名
	if name == "" {
		name = "default"
	}
	writer, err := rotatelogs.New(
		fmt.Sprintf("%s/%s.%s.log", logDir, name, "%Y-%m-%d"),
		rotatelogs.WithLinkName(logDir+"/"+name+".log"),
		rotatelogs.WithRotationTime(time.Hour*24),
		rotatelogs.WithMaxAge(15*24*time.Hour),
	)
	if err != nil {
		return nil, err
	}

	return writer, nil
}

var slogLvMap = map[string]slog.Level{
	"debug": slog.LevelDebug,
	"info":  slog.LevelInfo,
	"warn":  slog.LevelWarn,
	"error": slog.LevelError,
}
var zapLogLvMap = map[string]zapcore.Level{
	"debug": zapcore.DebugLevel,
	"info":  zapcore.InfoLevel,
	"warn":  zapcore.WarnLevel,
	"error": zapcore.ErrorLevel,
}

type NewLoggerIn struct {
	fx.In
	LogWriter io.Writer
	EnvBase   *env.Base
}

func NewLogger(p NewLoggerIn) (*zap.Logger, *slog.Logger) {
	var syncer zapcore.WriteSyncer
	syncer = zapcore.AddSync(p.LogWriter)

	var zapLogger *zap.Logger
	zapLogLv, ok := zapLogLvMap[p.EnvBase.LogLevel]
	if !ok { //默认为error
		zapLogLv = zapcore.ErrorLevel
	}
	//if p.Base.Environment == env.Dev {
	zapLogger = zap.New(zapcore.NewCore(zapcore.NewConsoleEncoder(zap.NewDevelopmentEncoderConfig()), syncer, zapLogLv)) //这个输出阅读起来更容易 Development 没有性能差异
	//} else {
	//	encoderConfig := zap.NewProductionEncoderConfig()
	//	encoderConfig.EncodeTime = zapcore.TimeEncoderOfLayout("2006-01-02T15:04:05.000Z07:00") // 自定义时间格式
	//	zapLogger = zap.New(zapcore.NewCore(zapcore.NewJSONEncoder(encoderConfig), syncer, zapLogLv))
	//}
	zap.ReplaceGlobals(zapLogger)

	slogLv, ok := slogLvMap[p.EnvBase.LogLevel]
	if !ok { //默认为error
		slogLv = slog.LevelError
	}

	//复制的源码 修改converter
	logger := slog.New(slogzap.Option{Level: slogLv, Logger: zapLogger, AddSource: true, Converter: converter}.NewZapHandler())

	slog.SetDefault(logger)

	return zapLogger, logger
}

func converter(addSource bool, replaceAttr func(groups []string, a slog.Attr) slog.Attr, loggerAttr []slog.Attr, groups []string, record *slog.Record) []zapcore.Field {

	attrs := slogcommon.AppendRecordAttrsToAttrs(loggerAttr, groups, record)
	attrs = slogcommon.ReplaceError(attrs, slogzap.ErrorKeys...)
	attrs = slogcommon.ReplaceAttrs(replaceAttr, []string{}, attrs...)
	attrs = slogcommon.RemoveEmptyAttrs(attrs)

	return attrsToFields(attrs...)
}

func attrsToFields(attrs ...slog.Attr) []zapcore.Field {
	output := make([]zapcore.Field, 0, len(attrs))

	for _, attr := range attrs {
		v := mergeAttrValues(attr.Value)
		if v.Kind() == slog.KindGroup {
			output = slices.Concat(output, attrsToFields(v.Group()...))
		} else {
			output = append(output, zap.Any(attr.Key, v.Any()))
		}
	}

	return output
}
func mergeAttrValues(values ...slog.Value) slog.Value {
	v := values[0]

	for i := 1; i < len(values); i++ {
		if v.Kind() != slog.KindGroup || values[i].Kind() != slog.KindGroup {
			v = values[i]
			continue
		}

		v = slog.GroupValue(append(v.Group(), values[i].Group()...)...)
	}

	return v
}
