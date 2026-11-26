package main

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo"
)

// 日志落盘：打包成 GUI 之后 stderr 通常不可见，出问题时只有用户口述。
// 这里给标准库 log 再接一个异步文件 sink，事件落在 <配置目录>/logs/app.log，
// 单文件 2 MiB 滚动，保留当前文件与两份历史（app.log、app.log.1、app.log.2）。
//
// 写盘绝不能在日志调用方身上同步发生：sink.Write 只按行拆分、脱敏并投进
// 容量 4096 的队列，队列满就丢弃并计数，由单写协程负责打开、追加与轮转。
const (
	logFileName      = "app.log"
	logQueueCapacity = 4096
	logLineMax       = 4 << 10
	logFileMaxSize   = 2 << 20
	logFileKeep      = 3
	logFlushTimeout  = 200 * time.Millisecond
)

// logFileSink 是异步日志文件的写入端。字段分两组：Write 只碰 queue、pending、
// dropped 与只读配置；打开、追加、轮转与 size 只在写协程里改，由 mu 保护。
type logFileSink struct {
	dir     string
	version string
	maxSize int64
	keep    int
	maxLine int

	queue   chan []byte
	stop    chan struct{}
	done    chan struct{}
	started bool
	once    sync.Once

	pending atomic.Int64
	dropped atomic.Int64

	mu       sync.Mutex
	file     *os.File
	size     int64
	reported bool
}

// newLogFileSink 建一个直接写 dir/logs 的 sink，并启动唯一的写协程。无头测试
// 也可以直接构造字段，不启动写协程来验证队列满时丢弃。
func newLogFileSink(dir string) *logFileSink {
	s := &logFileSink{
		dir:     filepath.Join(dir, "logs"),
		version: mygo.App.Version(),
		maxSize: logFileMaxSize,
		keep:    logFileKeep,
		maxLine: logLineMax,
		queue:   make(chan []byte, logQueueCapacity),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
		started: true,
	}
	s.once.Do(func() { go s.loop() })
	return s
}

// installLogSink 把标准库 log 的输出接到 stderr 与文件 sink。只装一次；在
// main() 启动时调用，本机会话损坏、协议注册失败这类早期日志也能落盘。
func installLogSink(dir string) {
	logSinkOnce.Do(func() {
		if dir == "" {
			dir = configDir()
		}
		s := newLogFileSink(dir)
		globalLogSink = s
		log.SetFlags(log.LstdFlags | log.Lmicroseconds)
		log.SetOutput(io.MultiWriter(os.Stderr, s))
	})
}

var (
	globalLogSink *logFileSink
	logSinkOnce   sync.Once
)

// flushLogSink 等队列写完，最多等 timeout。退出流程调用：超时后剩余队列丢弃，
// 不让日志拖住进程退出。
func flushLogSink(timeout time.Duration) {
	if s := globalLogSink; s != nil {
		s.flush(timeout)
	}
}

// Write 实现 io.Writer：永不阻塞、永不报错。按行拆分后截断与脱敏再入队；队列
// 满时直接丢弃这一行并计数，log.Printf 的调用方照常返回。
func (s *logFileSink) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if s.queue == nil {
		s.queue = make(chan []byte, logQueueCapacity)
	}
	if s.maxLine <= 0 {
		s.maxLine = logLineMax
	}
	for _, raw := range strings.SplitAfter(string(p), "\n") {
		if raw == "" {
			continue
		}
		line := s.prepare(raw)
		s.pending.Add(1)
		select {
		case s.queue <- line:
		default:
			s.pending.Add(-1)
			s.dropped.Add(1)
		}
	}
	return len(p), nil
}

// prepare 把一条日志变成待写盘的字节：先脱敏，再按 rune 边界截断，最后补
// 换行。先脱敏可以避免截断把令牌切成「前半段仍完整可见」的样子。
func (s *logFileSink) prepare(raw string) []byte {
	raw = strings.TrimSuffix(raw, "\n")
	raw = strings.TrimSuffix(raw, "\r")
	raw = redactText(raw)
	if len(raw) > s.maxLine {
		raw = truncateUTF8(raw, s.maxLine) + "…(truncated)"
	}
	var buf bytes.Buffer
	buf.Grow(len(raw) + 1)
	buf.WriteString(raw)
	buf.WriteByte('\n')
	return buf.Bytes()
}

// truncateUTF8 截到最多 n 字节，且不把一个多字节字符切成两半。
func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// loop 是唯一的写协程：顺序消费队列，收到 stop 后先写完手头的再关文件。
func (s *logFileSink) loop() {
	defer close(s.done)
	for {
		select {
		case line := <-s.queue:
			s.writeLine(line)
			s.pending.Add(-1)
		case <-s.stop:
			for {
				select {
				case line := <-s.queue:
					s.writeLine(line)
					s.pending.Add(-1)
				default:
					s.closeFile()
					return
				}
			}
		}
	}
}

// flush 等 pending 归零，最多等 timeout。轮询而不是 WaitGroup：Write 可能
// 在 flush 期间继续 Add，轮询对反复入队的场景也安全。
func (s *logFileSink) flush(timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for s.pending.Load() > 0 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
}

// close 停止写协程并关文件。只对 newLogFileSink 建出来的 sink 有意义；测试
// 直接构造的 sink 没有写协程，不会调用它。
func (s *logFileSink) close() {
	if !s.started {
		return
	}
	select {
	case <-s.stop:
	default:
		close(s.stop)
	}
	<-s.done
}

func (s *logFileSink) writeLine(line []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		if err := s.openLocked(); err != nil {
			s.reportLocked(err)
			return
		}
	}
	if s.maxSize > 0 && s.size > 0 && s.size+int64(len(line)) > s.maxSize {
		if err := s.rotateLocked(); err != nil {
			s.reportLocked(err)
			return
		}
	}
	n, err := s.file.Write(line)
	s.size += int64(n)
	if err != nil {
		s.reportLocked(err)
	}
}

// openLocked 打开（必要时创建）当前日志文件。文件是空的（新建或刚轮转）时
// 先写一行启动信息，诊断包里一眼能看出日志来自哪个版本、哪个平台。
func (s *logFileSink) openLocked() error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(s.dir, logFileName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	s.file = f
	info, err := f.Stat()
	if err == nil {
		s.size = info.Size()
	} else {
		s.size = 0
	}
	if s.size > 0 {
		return nil
	}
	version := s.version
	if version == "" {
		version = "unknown"
	}
	header := fmt.Sprintf("t2-desktop %s %s/%s %s started %s\n",
		version, runtime.GOOS, runtime.GOARCH, runtime.Version(),
		time.Now().Format(time.RFC3339))
	n, err := f.WriteString(header)
	s.size += int64(n)
	return err
}

// rotateLocked 按 删 .2 → .1 改 .2 → app.log 改 .1 → 新建 的顺序滚动。改名
// 失败时保留当前文件继续写：Windows 上文件被占用时不能反复重试。
func (s *logFileSink) rotateLocked() error {
	s.closeFileLocked()
	base := filepath.Join(s.dir, logFileName)
	_ = os.Remove(base + ".2")
	if err := os.Rename(base+".1", base+".2"); err != nil && !os.IsNotExist(err) {
		s.reopenLocked()
		return err
	}
	if err := os.Rename(base, base+".1"); err != nil {
		s.reopenLocked()
		return err
	}
	s.size = 0
	return s.openLocked()
}

// reopenLocked 在轮转失败后重新打开原文件，保证后面的日志还能写进去。
func (s *logFileSink) reopenLocked() {
	s.closeFileLocked()
	path := filepath.Join(s.dir, logFileName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	s.file = f
	if info, err := f.Stat(); err == nil {
		s.size = info.Size()
	}
}

func (s *logFileSink) closeFile() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeFileLocked()
}

func (s *logFileSink) closeFileLocked() {
	if s.file != nil {
		_ = s.file.Close()
		s.file = nil
	}
}

// reportLocked 只往 stderr 报一次错误。不能再走 log.Printf：那会递归回到
// 这个 sink，文件写不进去时无限循环。
func (s *logFileSink) reportLocked(err error) {
	if s.reported {
		return
	}
	s.reported = true
	fmt.Fprintf(os.Stderr, "日志写入 %s 失败：%v\n", s.dir, err)
}
