package logger

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

type Level int

const (
	INFO Level = iota
	WARN
	ERROR
	DEBUG
)

var (
	// ANSI colors
	colorReset  = "\033[0m"
	colorRed    = "\033[31m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorBlue   = "\033[34m"
	colorGray   = "\033[90m"
	colorCyan   = "\033[36m"

	// Output writer (defaults to stdout)
	out io.Writer = os.Stdout

	// Log channel for dashboard (optional)
	logChan   chan LogEntry
	logChanMu sync.RWMutex
)

// LogEntry represents a structured log message
type LogEntry struct {
	Timestamp string `json:"timestamp"`
	Level     string `json:"level"`
	Component string `json:"component"`
	Message   string `json:"message"`
}

// Init sets up the logger (optional configuration can be added here)
func Init() {
	// Check if NO_COLOR env var is set
	if os.Getenv("NO_COLOR") != "" {
		DisableColors()
	}
}

func DisableColors() {
	colorReset = ""
	colorRed = ""
	colorGreen = ""
	colorYellow = ""
	colorBlue = ""
	colorGray = ""
	colorCyan = ""
}

// SetLogChannel sets a channel to stream logs to (e.g., for dashboard)
func SetLogChannel(ch chan LogEntry) {
	logChanMu.Lock()
	defer logChanMu.Unlock()
	logChan = ch
}

func log(level Level, component string, format string, args ...interface{}) {
	now := time.Now().Format("15:04:05")
	msg := fmt.Sprintf(format, args...)

	var levelStr string
	var color string

	switch level {
	case INFO:
		levelStr = "INFO"
		color = colorGreen
	case WARN:
		levelStr = "WARN"
		color = colorYellow // Orange-like in many terminals
	case ERROR:
		levelStr = "ERROR"
		color = colorRed
	case DEBUG:
		levelStr = "DEBUG"
		color = colorGray
	}

	// Console output (with colors)
	// Removed fixed width padding from level to avoid [WARN ] spaces
	// Added spaces after ] to align message start approximately
	consoleLog := fmt.Sprintf("%s[%s]%s %s[%s]%s %s[%s]%s: %s\n",
		colorGray, now, colorReset,
		color, levelStr, colorReset,
		colorCyan, component, colorReset,
		msg,
	)
	fmt.Fprint(out, consoleLog)

	// Send structured log to dashboard
	entry := LogEntry{
		Timestamp: now,
		Level:     levelStr,
		Component: component,
		Message:   msg,
	}

	logChanMu.RLock()
	if logChan != nil {
		select {
		case logChan <- entry:
		default:
			// Drop log if channel is full
		}
	}
	logChanMu.RUnlock()
}

func Info(component string, format string, args ...interface{}) {
	log(INFO, component, format, args...)
}

func Warn(component string, format string, args ...interface{}) {
	log(WARN, component, format, args...)
}

func Error(component string, format string, args ...interface{}) {
	log(ERROR, component, format, args...)
}

func Debug(component string, format string, args ...interface{}) {
	log(DEBUG, component, format, args...)
}
