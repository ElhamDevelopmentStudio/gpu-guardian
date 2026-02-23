package logger

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Logger struct {
	mu         sync.Mutex
	file       *os.File
	path       string
	toStdout   bool
	maxBytes   int64
	currentLen int64
}

type Entry map[string]interface{}

func New(path string, maxBytes int64, toStdout bool) (*Logger, error) {
	if path == "" {
		return &Logger{path: "", toStdout: toStdout}, nil
	}

	dir := filepath.Dir(path)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}

	return &Logger{
		file:       f,
		path:       path,
		toStdout:   toStdout,
		maxBytes:   maxBytes,
		currentLen: fi.Size(),
	}, nil
}

func (l *Logger) Info(msg string, fields Entry) {
	l.write("INFO", msg, fields)
}

func (l *Logger) Warn(msg string, fields Entry) {
	l.write("WARN", msg, fields)
}

func (l *Logger) Error(msg string, fields Entry) {
	l.write("ERROR", msg, fields)
}

func (l *Logger) write(level, msg string, fields Entry) {
	rec := map[string]interface{}{
		"ts":    time.Now().UTC().Format(time.RFC3339Nano),
		"lvl":   level,
		"msg":   msg,
		"event": msg,
	}
	for k, v := range fields {
		rec[k] = v
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return
	}
	line := string(b) + "\n"

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.file != nil {
		_ = l.rotateIfNeeded(int64(len(line)))
		_, _ = fmt.Fprint(l.file, line)
		l.currentLen += int64(len(line))
	}
	if l.toStdout {
		fmt.Print(line)
	}
}

func (l *Logger) rotateIfNeeded(incoming int64) error {
	if l.path == "" || l.maxBytes <= 0 || l.currentLen+incoming <= l.maxBytes {
		return nil
	}
	if l.file != nil {
		_ = l.file.Close()
	}
	rotated := fmt.Sprintf("%s.%s", l.path, time.Now().Format("20060102T150405"))
	_ = os.Rename(l.path, rotated)
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	l.file = f
	l.currentLen = 0
	return nil
}

func (l *Logger) Close() {
	if l.file != nil {
		_ = l.file.Close()
	}
}
