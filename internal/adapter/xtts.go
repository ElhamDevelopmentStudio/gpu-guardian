package adapter

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

type WorkloadAdapter interface {
	Start(ctx context.Context, cmd string, concurrency int) error
	Restart(ctx context.Context, concurrency int) error
	Stop() error
	GetPID() int
}

type Config struct {
	OutputPath  string
	StopTimeout time.Duration
	EchoOutput  bool
}

type XTTSAdapter struct {
	mu      sync.Mutex
	cmd     *exec.Cmd
	command string
	cancel  context.CancelFunc
	cfg     Config

	writer      *countingWriter
	outputFile  *os.File
	outputPath  string
	runningFile bool
}

type countingWriter struct {
	w io.Writer
	n atomic.Uint64
}

func (cw *countingWriter) Write(p []byte) (int, error) {
	n, err := cw.w.Write(p)
	if n > 0 {
		cw.n.Add(uint64(n))
	}
	return n, err
}

func (cw *countingWriter) Bytes() uint64 {
	return cw.n.Load()
}

func (cw *countingWriter) Reset() {
	cw.n.Store(0)
}

func NewXttsAdapter(cfg Config) *XTTSAdapter {
	return &XTTSAdapter{
		cfg:        cfg,
		outputPath: cfg.OutputPath,
	}
}

func (a *XTTSAdapter) ensureOutputFileLocked() error {
	if a.outputPath == "" {
		f, err := os.CreateTemp("", "guardian-xtts-output-*.log")
		if err != nil {
			return err
		}
		a.outputPath = f.Name()
		if err := f.Close(); err != nil {
			return err
		}
	}

	f, err := os.OpenFile(a.outputPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if a.outputFile != nil {
		_ = a.outputFile.Close()
	}
	a.outputFile = f

	if a.writer != nil {
		a.writer.w = f
		a.writer.Reset()
	} else {
		a.writer = &countingWriter{w: f}
	}
	return nil
}

func (a *XTTSAdapter) Start(ctx context.Context, cmd string, concurrency int) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.cmd != nil {
		return fmt.Errorf("adapter already started")
	}
	if err := a.ensureOutputFileLocked(); err != nil {
		return err
	}

	a.command = cmd

	runCtx, cancel := context.WithCancel(ctx)
	a.cancel = cancel
	command := exec.CommandContext(runCtx, "sh", "-lc", cmd)
	command.Env = withConcurrencyEnv(os.Environ(), concurrency)

	var outWriter io.Writer = a.writer
	if a.cfg.EchoOutput {
		outWriter = io.MultiWriter(os.Stdout, a.writer)
	}
	command.Stdout = outWriter
	command.Stderr = outWriter

	if err := command.Start(); err != nil {
		if a.cancel != nil {
			a.cancel()
			a.cancel = nil
		}
		return err
	}

	a.cmd = command
	a.runningFile = true
	a.writer.Reset()
	return nil
}

func (a *XTTSAdapter) Restart(ctx context.Context, concurrency int) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if err := a.stopLocked(); err != nil {
		return err
	}

	runCtx, cancel := context.WithCancel(ctx)
	command := exec.CommandContext(runCtx, "sh", "-lc", a.command)
	command.Env = withConcurrencyEnv(os.Environ(), concurrency)

	var outWriter io.Writer = a.writer
	if a.cfg.EchoOutput {
		outWriter = io.MultiWriter(os.Stdout, a.writer)
	}
	command.Stdout = outWriter
	command.Stderr = outWriter

	if err := command.Start(); err != nil {
		a.cancel = nil
		a.cmd = nil
		return err
	}

	a.cancel = cancel
	a.cmd = command
	a.writer.Reset()
	a.runningFile = true
	return nil
}

func (a *XTTSAdapter) stopLocked() error {
	if a.cmd == nil || a.cmd.Process == nil {
		a.cmd = nil
		return nil
	}
	a.writer.Reset()
	if a.cancel != nil {
		a.cancel()
	}

	grace := a.cfg.StopTimeout
	if grace <= 0 {
		grace = 5 * time.Second
	}
	done := make(chan error, 1)
	go func() {
		done <- a.cmd.Wait()
	}()
	select {
	case <-done:
		// Process terminated.
	case <-time.After(grace):
		_ = a.cmd.Process.Signal(syscall.SIGKILL)
		<-done
	}

	a.cmd = nil
	a.cancel = nil
	a.runningFile = false
	return nil
}

func (a *XTTSAdapter) Stop() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.stopLocked()
}

func (a *XTTSAdapter) GetPID() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cmd == nil || a.cmd.Process == nil {
		return 0
	}
	return a.cmd.Process.Pid
}

func (a *XTTSAdapter) IsRunning() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cmd == nil || a.cmd.Process == nil {
		return false
	}
	return a.runningFile && a.cmd.ProcessState == nil
}

func (a *XTTSAdapter) OutputBytes() uint64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.writer == nil {
		return 0
	}
	return a.writer.Bytes()
}

func (a *XTTSAdapter) OutputPath() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.outputPath
}

func withConcurrencyEnv(base []string, concurrency int) []string {
	envMap := map[string]string{}
	for _, item := range base {
		if split := strings.SplitN(item, "=", 2); len(split) == 2 {
			envMap[split[0]] = split[1]
		}
	}
	envMap["CONCURRENCY"] = fmt.Sprintf("%d", concurrency)
	envMap["XTTS_CONCURRENCY"] = fmt.Sprintf("%d", concurrency)

	result := make([]string, 0, len(envMap))
	for k, v := range envMap {
		result = append(result, fmt.Sprintf("%s=%s", k, v))
	}
	return result
}
