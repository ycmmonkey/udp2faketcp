package udp2faketcp

import (
	"sync"
	"time"
)

// 全局配置
const (
	BUFFER_POOL_SIZE = 1000  // buffer池大小
	MAX_CONNECTIONS  = 10000 // 最大连接数
)

var (
	UDP_TTL        = 180 * time.Second
	MAX_PACKET_LEN = 1440 // MTU大小
	DEBUG          = false
)

// 全局buffer池 - 支持不同大小的buffer
var (
	smallBufferPool = sync.Pool{
		New: func() interface{} {
			return make([]byte, 512) // 小包
		},
	}

	mediumBufferPool = sync.Pool{
		New: func() interface{} {
			return make([]byte, 1024) // 中等包
		},
	}

	largeBufferPool = sync.Pool{
		New: func() interface{} {
			return make([]byte, MAX_PACKET_LEN) // 大包
		},
	}
)

// 根据数据大小选择合适的buffer池
func getBuffer(size int) []byte {
	switch {
	case size <= 512:
		return smallBufferPool.Get().([]byte)
	case size <= 1024:
		return mediumBufferPool.Get().([]byte)
	default:
		return largeBufferPool.Get().([]byte)
	}
}

func putBuffer(buf []byte) {
	switch cap(buf) {
	case 512:
		smallBufferPool.Put(buf)
	case 1024:
		mediumBufferPool.Put(buf)
	case MAX_PACKET_LEN:
		largeBufferPool.Put(buf)
	}
}

// 高性能的连接池
type ConnPool struct {
	mu    sync.RWMutex
	conns map[string]*ConnWrapper
	size  int
}

type ConnWrapper struct {
	conn      interface{} // 可以是UDP或TCP连接
	lastUsed  int64
	useCount  int64
	addr      string
	cleanupFn func()
}

func NewConnPool(maxSize int) *ConnPool {
	return &ConnPool{
		conns: make(map[string]*ConnWrapper, maxSize),
		size:  maxSize,
	}
}

func (p *ConnPool) Get(key string) *ConnWrapper {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if conn, exists := p.conns[key]; exists {
		return conn
	}
	return nil
}

func (p *ConnPool) Set(key string, wrapper *ConnWrapper) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	if len(p.conns) >= p.size {
		// 连接池满了，需要清理
		p.cleanupOldest()
	}

	p.conns[key] = wrapper
	return true
}

func (p *ConnPool) Delete(key string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if wrapper, exists := p.conns[key]; exists {
		if wrapper.cleanupFn != nil {
			wrapper.cleanupFn()
		}
		delete(p.conns, key)
	}
}

func (p *ConnPool) cleanupOldest() {
	// 清理最老的连接
	var oldestKey string
	var oldestTime int64 = ^int64(0) // 最大值

	for key, wrapper := range p.conns {
		if wrapper.lastUsed < oldestTime {
			oldestTime = wrapper.lastUsed
			oldestKey = key
		}
	}

	if oldestKey != "" {
		if wrapper := p.conns[oldestKey]; wrapper.cleanupFn != nil {
			wrapper.cleanupFn()
		}
		delete(p.conns, oldestKey)
	}
}

// 性能监控
type PerfMonitor struct {
	packetsPerSecond int64
	bytesPerSecond   int64
	connections      int64
	errors           int64
	lastReset        time.Time
}

func NewPerfMonitor() *PerfMonitor {
	return &PerfMonitor{
		lastReset: time.Now(),
	}
}

func (m *PerfMonitor) RecordPacket(bytes int) {
	// 使用原子操作更新统计
	// atomic.AddInt64(&m.packetsPerSecond, 1)
	// atomic.AddInt64(&m.bytesPerSecond, int64(bytes))
}

// 网络优化设置
func OptimizeNetworkSettings() {
	// 这些设置需要在程序启动时调用
	// 可以通过环境变量或配置文件设置
}

// 批处理器 - 减少系统调用
type BatchProcessor struct {
	packets chan []byte
	batch   [][]byte
	ticker  *time.Ticker
}

func NewBatchProcessor(batchSize int, flushInterval time.Duration) *BatchProcessor {
	bp := &BatchProcessor{
		packets: make(chan []byte, batchSize*2),
		batch:   make([][]byte, 0, batchSize),
		ticker:  time.NewTicker(flushInterval),
	}

	go bp.run()
	return bp
}

func (bp *BatchProcessor) run() {
	for {
		select {
		case packet := <-bp.packets:
			bp.batch = append(bp.batch, packet)
			if len(bp.batch) >= cap(bp.batch) {
				bp.flush()
			}
		case <-bp.ticker.C:
			if len(bp.batch) > 0 {
				bp.flush()
			}
		}
	}
}

func (bp *BatchProcessor) flush() {
	// 批量处理数据包
	for _, packet := range bp.batch {
		// 处理packet
		_ = packet
	}
	bp.batch = bp.batch[:0] // 清空但保留容量
}
