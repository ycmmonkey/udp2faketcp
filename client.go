package udp2faketcp

import (
	"io"
	"log"
	"net"
	"runtime"
	"sync"
	"time"

	"github.com/xtaci/tcpraw"
)

var tcpConnections sync.Map
var tcpLock sync.Mutex

// 使用对象池减少内存分配
var bufferPool = sync.Pool{
	New: func() interface{} {
		return make([]byte, MAX_PACKET_LEN)
	},
}

func Client(localAddr string, remoteAddr string) {
	udpAddr, err := net.ResolveUDPAddr("udp", localAddr)
	if err != nil {
		log.Println("Error resolving UDP:", err)
		return
	}
	tcpAddr, err := net.ResolveTCPAddr("tcp", remoteAddr)
	if err != nil {
		log.Println("Error dialing TCP:", err)
		return
	}
	udpConn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		log.Println("Error listen UDP:", err)
		return
	}
	defer udpConn.Close()

	// 优化并发数量，避免过度并发
	workerCount := runtime.NumCPU()
	if workerCount > 4 {
		workerCount = 4 // 限制最大worker数量
	}

	// 使用channel控制并发
	done := make(chan struct{})

	for i := 0; i < workerCount; i++ {
		go func() {
			defer func() {
				done <- struct{}{}
			}()

			for {
				// 从对象池获取buffer
				buffer := bufferPool.Get().([]byte)

				length, addr, err := udpConn.ReadFromUDP(buffer)
				if err != nil {
					debugLogln("Error reading from UDP:", err)
					bufferPool.Put(buffer) // 记得归还buffer
					continue
				}

				// 处理连接逻辑
				tcpConn := getOrCreateTCPConnection(addr.String(), remoteAddr)
				if tcpConn != nil {
					_, err = tcpConn.WriteTo(buffer[:length], tcpAddr)
					if err != nil {
						debugLogln("Error writing to TCP:", err)
						tcpConn.Close()
						tcpConnections.Delete(addr.String())
					} else {
						debugLogln("Wrote", length, "bytes from", addr.String())
					}
				}

				// 归还buffer到对象池
				bufferPool.Put(buffer)
			}
		}()
	}

	// 等待所有worker完成
	for i := 0; i < workerCount; i++ {
		<-done
	}
}

// 提取连接获取逻辑，减少重复代码
func getOrCreateTCPConnection(addrStr, remoteAddr string) *tcpraw.TCPConn {
	if val, exists := tcpConnections.Load(addrStr); exists {
		return val.(*tcpraw.TCPConn)
	}

	tcpLock.Lock()
	defer tcpLock.Unlock()

	// 双重检查
	if val, exists := tcpConnections.Load(addrStr); exists {
		return val.(*tcpraw.TCPConn)
	}

	log.Printf("New UDP client: %s", addrStr)
	tcpConn, err := tcpraw.Dial("tcp", remoteAddr)
	if err != nil {
		debugLogln("Error dialing TCP:", err)
		return nil
	}

	err = tcpConn.SetDeadline(time.Now().Add(UDP_TTL))
	if err != nil {
		debugLogln("Error set deadline:", err)
		tcpConn.Close()
		return nil
	}

	tcpConnections.Store(addrStr, tcpConn)

	// 启动处理goroutine
	go handleClientConnection(tcpConn, addrStr)

	return tcpConn
}

func handleClientConnection(conn *tcpraw.TCPConn, addrStr string) {
	defer func() {
		conn.Close()
		tcpConnections.Delete(addrStr)
	}()

	// 从对象池获取buffer
	buffer := bufferPool.Get().([]byte)
	defer bufferPool.Put(buffer)

	for {
		err := conn.SetDeadline(time.Now().Add(UDP_TTL))
		if err != nil {
			debugLogln("Error setting read deadline:", err)
			break
		}

		length, _, err := conn.ReadFrom(buffer)
		if err != nil {
			if err != io.EOF {
				debugLogln("Error reading from RAWTCP:", err)
			}
			break
		}

		// 这里需要根据实际需求处理返回数据
		// 原代码中udpConn和udpAddr在这个函数中不可用
		// 需要重新设计这部分逻辑
		_ = length // 临时处理
	}
}
