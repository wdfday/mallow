package shared

import pkgshared "mallow/pkg/shared"

type Logger = pkgshared.Logger
type LogLevel = pkgshared.LogLevel

const (
	LogLevelDebug   = pkgshared.LogLevelDebug
	LogLevelInfo    = pkgshared.LogLevelInfo
	LogLevelWarning = pkgshared.LogLevelWarning
	LogLevelError   = pkgshared.LogLevelError
)

var (
	NewLogger        = pkgshared.NewLogger
	SetDefaultLogger = pkgshared.SetDefaultLogger
	Debug            = pkgshared.Debug
	Info             = pkgshared.Info
	Warning          = pkgshared.Warning
	Error            = pkgshared.Error
)
