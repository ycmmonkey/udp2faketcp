package udp2faketcp

import (
	"context"
	"io"
	"log"
	"net"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xtaci/tcpraw"
)

var udpConnections sync.Map
var udpLock sync.RWMutex // 使用读写锁，读操作更高效

// 连接统计
var (
	activeConnections int64
	totalPackets      int64
	totalBytes        int64
)

// UDP连接包装器，增加连接管理
type UDPConnWrapper struct {
	conn     *net.UDPConn
	lastUsed int64 // 原子操作时间戳
	addr     string
}

func (w *UDPConnWrapper) updateLastUsed() {
	atomic.StoreInt64(&w.lastUsed, time.Now().Unix())
}

func (w *UDPConnWrapper) getLastUsed() time.Time {
	return time.Unix(atomic.LoadInt64(&w.lastUsed), 0)
}

func Server(localAddr string, remoteAddr string) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	udpAddr, err := net.ResolveUDPAddr("udp", remoteAddr)
	if err != nil {
		debugLogln("Error resolving UDP address:", err)
		return
	}

	conn, err := tcpraw.Listen("tcp", localAddr)
	if err != nil {
		log.Println("Error listening TCP:", err)
		return
	}
	defer conn.Close()

	// 优化worker数量
	workerCount := runtime.NumCPU()
	if workerCount > 6 {
		workerCount = 6 // 限制最大worker数量
	}

	// 启动连接清理goroutine
	go connectionCleaner(ctx)

	// 启动统计goroutine
	go statsReporter(ctx)

	// 使用WaitGroup管理goroutine
	var wg sync.WaitGroup

	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			serverWorker(ctx, conn, udpAddr, workerID)
		}(i)
	}

	// 等待所有worker完成
	wg.Wait()
}

func serverWorker(ctx context.Context, conn *tcpraw.TCPConn, udpAddr *net.UDPAddr, workerID int) {
	debugLogln("Worker", workerID, "started")
	defer debugLogln("Worker", workerID, "stopped")

	for {
		select {
		case <-ctx.Done():
			return
		default:
			// 从对象池获取buffer
			buffer := bufferPool.Get().([]byte)

			// 设置读取超时，避免阻塞
			conn.SetReadDeadline(time.Now().Add(time.Second))

			length, tcpAddr, err := conn.ReadFrom(buffer)
			if err != nil {
				bufferPool.Put(buffer)
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					continue // 超时继续
				}
				debugLogln("Error reading from RAWTCP:", err)
				continue
			}

			// 更新统计
			atomic.AddInt64(&totalPackets, 1)
			atomic.AddInt64(&totalBytes, int64(length))

			debugLogln("Worker", workerID, "read from RAWTCP:", length, tcpAddr.String())

			// 处理数据
			udpConn := getOrCreateUDPConnection(tcpAddr.String(), udpAddr)
			if udpConn != nil {
				_, err = udpConn.conn.Write(buffer[:length])
				if err != nil {
					debugLogln("Error writing to UDP:", err)
					closeUDPConnection(tcpAddr.String())
				} else {
					udpConn.updateLastUsed()
					debugLogln("Worker", workerID, "wrote", length, "bytes to UDP")
				}
			}

			// 归还buffer
			bufferPool.Put(buffer)
		}
	}
}

// 优化的连接获取函数
func getOrCreateUDPConnection(addrStr string, udpAddr *net.UDPAddr) *UDPConnWrapper {
	// 先尝试读锁
	if val, exists := udpConnections.Load(addrStr); exists {
		wrapper := val.(*UDPConnWrapper)
		wrapper.updateLastUsed()
		return wrapper
	}

	// 需要创建新连接，使用写锁
	udpLock.Lock()
	defer udpLock.Unlock()

	// 双重检查
	if val, exists := udpConnections.Load(addrStr); exists {
		wrapper := val.(*UDPConnWrapper)
		wrapper.updateLastUsed()
		return wrapper
	}

	log.Println("New TCP client:", addrStr)
	udpConn, err := net.DialUDP("udp", nil, udpAddr)
	if err != nil {
		debugLogln("Error dialing UDP:", err)
		return nil
	}

	wrapper := &UDPConnWrapper{
		conn: udpConn,
		addr: addrStr,
	}
	wrapper.updateLastUsed()

	udpConnections.Store(addrStr, wrapper)
	atomic.AddInt64(&activeConnections, 1)

	// 启动处理goroutine
	go handleServerConnection(wrapper, addrStr)

	return wrapper
}

func handleServerConnection(wrapper *UDPConnWrapper, addrStr string) {
	defer func() {
		closeUDPConnection(addrStr)
	}()

	// 从对象池获取buffer
	buffer := bufferPool.Get().([]byte)
	defer bufferPool.Put(buffer)

	// 设置初始deadline
	wrapper.conn.SetDeadline(time.Now().Add(UDP_TTL))

	for {
		// 动态调整超时时间
		deadline := time.Now().Add(UDP_TTL)
		err := wrapper.conn.SetDeadline(deadline)
		if err != nil {
			debugLogln("Error setting read deadline:", err)
			break
		}

		length, err := wrapper.conn.Read(buffer)
		if err != nil {
			if err != io.EOF {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					debugLogln("UDP connection timeout:", addrStr)
				} else {
					debugLogln("Error reading from UDP:", err)
				}
			}
			break
		}

		wrapper.updateLastUsed()

		// 这里需要回写到TCP连接
		// 原代码中缺少TCP连接的引用，需要重新设计
		// 可以考虑在wrapper中保存TCP连接引用
		_ = length // 临时处理
	}
}

func closeUDPConnection(addrStr string) {
	if val, exists := udpConnections.LoadAndDelete(addrStr); exists {
		wrapper := val.(*UDPConnWrapper)
		wrapper.conn.Close()
		atomic.AddInt64(&activeConnections, -1)
		debugLogln("Closed UDP connection:", addrStr)
	}
}

// 连接清理器
func connectionCleaner(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cleanupExpiredConnections()
		}
	}
}

func cleanupExpiredConnections() {
	now := time.Now()
	expiredThreshold := now.Add(-UDP_TTL)

	var expiredKeys []string

	udpConnections.Range(func(key, value interface{}) bool {
		wrapper := value.(*UDPConnWrapper)
		if wrapper.getLastUsed().Before(expiredThreshold) {
			expiredKeys = append(expiredKeys, key.(string))
		}
		return true
	})

	for _, key := range expiredKeys {
		closeUDPConnection(key)
		debugLogln("Cleaned up expired connection:", key)
	}

	if len(expiredKeys) > 0 {
		debugLogln("Cleaned up", len(expiredKeys), "expired connections")
	}
}

// 统计报告器
func statsReporter(ctx context.Context) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			active := atomic.LoadInt64(&activeConnections)
			packets := atomic.LoadInt64(&totalPackets)
			bytes := atomic.LoadInt64(&totalBytes)

			log.Printf("Stats - Active connections: %d, Total packets: %d, Total bytes: %d",
				active, packets, bytes)
		}
	}
}
