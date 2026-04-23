package helper

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func InitLogger(devMode bool) (*zap.Logger, *zap.SugaredLogger) {
	var (
		logger *zap.Logger
		err    error
	)

	if devMode {
		encoderConfig := zapcore.EncoderConfig{
			MessageKey:     "msg",
			LevelKey:       "level",
			TimeKey:        "",
			NameKey:        "logger",
			CallerKey:      "caller",
			EncodeLevel:    zapcore.CapitalColorLevelEncoder,
			EncodeDuration: zapcore.StringDurationEncoder,
			EncodeCaller:   customCallerEncoder,
		}
		core := zapcore.NewCore(
			zapcore.NewConsoleEncoder(encoderConfig),
			os.Stdout,
			zap.DebugLevel,
		)
		logger = zap.New(core, zap.AddCaller())
	} else {
		logger, err = zap.NewProduction()
	}

	if err != nil {
		panic(fmt.Errorf("failed to initialize logger: %w", err))
	}

	return logger, logger.Sugar()
}

const LoggerKey = "logger"

func InjectLoggerMiddleware(logger *zap.SugaredLogger) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set(LoggerKey, logger)
		c.Next()
	}
}

func customCallerEncoder(caller zapcore.EntryCaller, enc zapcore.PrimitiveArrayEncoder) {
	fn := caller.Function
	if idx := strings.LastIndexByte(fn, '.'); idx != -1 {
		fn = fn[idx+1:]
	}
	enc.AppendString(fmt.Sprintf("(%s:%d) %s", filepath.Base(caller.File), caller.Line, fn))
}

// ZapWriter is an io.Writer that forwards to a Zap logger.
type ZapWriter struct {
	SugarLogger *zap.SugaredLogger
	Level       zapcore.Level
}

func (zw *ZapWriter) Write(p []byte) (int, error) {
	s := strings.TrimSpace(string(p))
	if s == "" {
		return len(p), nil
	}
	switch zw.Level {
	case zap.DebugLevel:
		zw.SugarLogger.Debug(s)
	case zap.WarnLevel:
		zw.SugarLogger.Warn(s)
	case zap.ErrorLevel:
		zw.SugarLogger.Error(s)
	default:
		zw.SugarLogger.Info(s)
	}
	return len(p), nil
}
