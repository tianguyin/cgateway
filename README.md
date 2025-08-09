# 🛠️ cgateway

**cgateway** 是一个由 **Go** 编写的 **TCP over WebSocket** 接口库。  

---

## ✨ 特性
- 支持 **TCP over WebSocket** 隧道
- 轻量化、易于集成
- **Client** 支持自定义 Header（可用于认证场景）
- 内置心跳机制，确保长连接稳定性

---

## 📦 快速开始

你只需要简单调用以下任意方法即可：
```go
// 创建客户端
cgateway.NewClient(url, header)

// 创建服务端
cgateway := cgateway.NewServer()
`````

server代码示例
`````go
ws, err := upgrade.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("WebSocket升级失败: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"message": "WebSocket连接失败"})
		return
	}
	defer func() {

	}()

	// 可选：设置WebSocket的CloseHandler
	ws.SetCloseHandler(func(code int, text string) error {
		log.Printf("WebSocket连接关闭: %d %s", code, text)
		return nil
	})

	cgateway := cgateway.NewServer()

	targetHandler := func(uuid string) (targetAddr string, err error) {
		// 根据UUID动态选择目标
		return Target, nil
	}
	// 启动心跳机制
	cgateway.StartHeartbeat()

	cgateway.HandleConnection(ws, targetHandler)
``````

client代码示例
````go
func RunClient(url, localAddr, gatewayId string) {
	ConnectServer(url, localAddr, gatewayId)
}

// ConnectServer 连接到WebSocket服务器并启动本地TCP监听
func ConnectServer(url, localAddr, gatewayId string) {

	headers := map[string]string{
		"C-Gateway-ID": gatewayId,
	}
	client, err := cgateway.NewClient(url, headers)

	if err != nil {
		log.Fatal("创建客户端失败:", err)
	}
	startTCPForwarding(client, localAddr)
}
func startTCPForwarding(client *tcp.Client, localAddr string) {
	listener, err := net.Listen("tcp", localAddr)
	if err != nil {
		log.Printf("TCP监听失败: %v", err)
		return
	}
	defer listener.Close()

	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Printf("接受TCP连接失败: %v", err)
			continue
		}

		log.Printf("新的TCP连接: %s", conn.RemoteAddr())

		// 每个连接启动一个goroutine处理
		go func(localConn net.Conn) {
			if err := client.ConnectTCP(localConn); err != nil {
				log.Printf("TCP连接到WebSocket失败: %v", err)
				localConn.Close()
			}
		}(conn)
	}
}
`````
注意，client支持自定义header，这是为了部分需要认证的场景，这并非可选，如果你不需要该功能，请传入一个任意的header的值
