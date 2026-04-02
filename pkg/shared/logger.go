package shared

import (
	"fmt"
	"log"
	"os"
	"time"
)

// Logger provides structured logging capabilities.
type Logger struct {
	*log.Logger
	level LogLevel
}

// LogLevel represents logging verbosity.
type LogLevel int

const (
	LogLevelDebug LogLevel = iota
	LogLevelInfo
	LogLevelWarning
	LogLevelError
)

func (l LogLevel) String() string {
	switch l {
	case LogLevelDebug:
		return "DEBUG"
	case LogLevelInfo:
		return "INFO"
	case LogLevelWarning:
		return "WARNING"
	case LogLevelError:
		return "ERROR"
	default:
		return "UNKNOWN"
	}
}

func NewLogger(level LogLevel) *Logger {
	return &Logger{
		Logger: log.New(os.Stdout, "", log.LstdFlags|log.Lmicroseconds),
		level:  level,
	}
}

func (l *Logger) SetLevel(level LogLevel) {
	l.level = level
}

func (l *Logger) Debug(format string, v ...interface{}) {
	if l.level <= LogLevelDebug {
		l.Printf("[DEBUG] "+format, v...)
	}
}

func (l *Logger) Info(format string, v ...interface{}) {
	if l.level <= LogLevelInfo {
		l.Printf("[INFO] "+format, v...)
	}
}

func (l *Logger) Warning(format string, v ...interface{}) {
	if l.level <= LogLevelWarning {
		l.Printf("[WARNING] "+format, v...)
	}
}

func (l *Logger) Error(format string, v ...interface{}) {
	if l.level <= LogLevelError {
		l.Printf("[ERROR] "+format, v...)
	}
}

func (l *Logger) WithFields(level LogLevel, format string, fields map[string]interface{}, v ...interface{}) {
	if l.level <= level {
		fieldStr := ""
		for k, val := range fields {
			if fieldStr != "" {
				fieldStr += ", "
			}
			fieldStr += k + "=" + formatValue(val)
		}
		if fieldStr != "" {
			l.Printf("[%s] "+format+" | %s", append([]interface{}{level.String()}, append(v, fieldStr)...)...)
		} else {
			l.Printf("[%s] "+format, append([]interface{}{level.String()}, v...)...)
		}
	}
}

func formatValue(v interface{}) string {
	switch val := v.(type) {
	case string:
		return val
	case int:
		return formatInt(int64(val))
	case int64:
		return formatInt(val)
	case int32:
		return formatInt(int64(val))
	case float64:
		return formatFloat(val)
	case float32:
		return formatFloat(float64(val))
	case bool:
		if val {
			return "true"
		}
		return "false"
	case time.Time:
		return val.Format(time.RFC3339)
	default:
		return fmt.Sprintf("%v", v)
	}
}

func formatInt(v int64) string {
	return fmt.Sprintf("%d", v)
}

func formatFloat(v float64) string {
	return fmt.Sprintf("%f", v)
}

var defaultLogger = NewLogger(LogLevelInfo)

func SetDefaultLogger(logger *Logger) {
	defaultLogger = logger
}

func Debug(format string, v ...interface{}) {
	defaultLogger.Debug(format, v...)
}

func Info(format string, v ...interface{}) {
	defaultLogger.Info(format, v...)
}

func Warning(format string, v ...interface{}) {
	defaultLogger.Warning(format, v...)
}

func Error(format string, v ...interface{}) {
	defaultLogger.Error(format, v...)
}
