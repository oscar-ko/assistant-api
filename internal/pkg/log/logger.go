package logger

import (
	"os"

	"assistant-api/internal/config"

	"github.com/natefinch/lumberjack"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var lg *zap.Logger

// InitLogger 初始化 zap 全域 logger，格式與 backend 一樣走 JSON。
func InitLogger() error {
	writeSyncer := getLogWriter(config.Log.Filename, config.Log.MaxSize, config.Log.MaxBackups, config.Log.MaxAge)
	encoder := getEncoder()
	level := new(zapcore.Level)
	if err := level.UnmarshalText([]byte(config.Log.Level)); err != nil {
		return err
	}

	core := zapcore.NewCore(encoder, writeSyncer, level)
	lg = zap.New(core, zap.AddCaller())
	zap.ReplaceGlobals(lg)
	return nil
}

func getEncoder() zapcore.Encoder {
	encoderConfig := zap.NewProductionEncoderConfig()
	encoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	encoderConfig.TimeKey = "time"
	encoderConfig.EncodeLevel = zapcore.CapitalLevelEncoder
	encoderConfig.EncodeDuration = zapcore.SecondsDurationEncoder
	encoderConfig.EncodeCaller = zapcore.ShortCallerEncoder
	return zapcore.NewJSONEncoder(encoderConfig)
}

func getLogWriter(filename string, maxSize, maxBackup, maxAge int) zapcore.WriteSyncer {
	if filename == "" {
		return zapcore.AddSync(os.Stdout)
	}

	lumberJackLogger := &lumberjack.Logger{
		Filename:   filename,
		MaxSize:    maxSize,
		MaxBackups: maxBackup,
		MaxAge:     maxAge,
	}
	return zapcore.NewMultiWriteSyncer(
		zapcore.AddSync(lumberJackLogger),
		zapcore.AddSync(os.Stdout),
	)
}
