package cgateway

import (
	"crypto/tls"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// Connection represents a TCP/UDP over WebSocket connection
type Connection struct {
	isUdp   bool
	udpConn *net.UDPConn
	udpAddr *net.UDPAddr
	tcpConn net.Conn
	wsConn  *websocket.Conn
	uuid    string
	del     bool
	buf     [][]byte
	t       int64
}

// Server manages WebSocket server connections
type Server struct {
	connMap     map[string]*Connection
	connMapLock *sync.RWMutex
	upgrader    websocket.Upgrader
	msgType     int
}

// Client manages WebSocket client connections
type Client struct {
	wsAddr     string
	wsAddrIp   string
	wsAddrPort string
	msgType    int
	header     map[string]string
}

// TargetHandler defines how to handle different targets dynamically
type TargetHandler func(uuid string) (target string, isUdp bool, err error)

// NewClient creates a new TCPOverWebsocket client instance
func NewClient(wsAddr string, header map[string]string) (*Client, error) {

	return &Client{
		wsAddr:  wsAddr,
		header:  header,
		msgType: websocket.BinaryMessage,
	}, nil
}

// NewServer Server methods
func NewServer() *Server {
	return &Server{
		connMap:     make(map[string]*Connection),
		connMapLock: new(sync.RWMutex),
		upgrader: websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
			CheckOrigin:     func(r *http.Request) bool { return true },
		},
		msgType: websocket.BinaryMessage,
	}
}

func (s *Server) getConn(uuid string) (*Connection, bool) {
	s.connMapLock.RLock()
	defer s.connMapLock.RUnlock()
	conn, haskey := s.connMap[uuid]
	return conn, haskey
}

func (s *Server) setConn(uuid string, conn *Connection) {
	s.connMapLock.Lock()
	defer s.connMapLock.Unlock()
	s.connMap[uuid] = conn
}

func (s *Server) deleteConn(uuid string) {
	if conn, haskey := s.getConn(uuid); haskey && conn != nil && !conn.del {
		s.connMapLock.Lock()
		defer s.connMapLock.Unlock()
		conn.del = true
		if conn.udpConn != nil {
			conn.udpConn.Close()
		}
		if conn.tcpConn != nil {
			conn.tcpConn.Close()
		}
		if conn.wsConn != nil {
			log.Print(uuid, " bye")
			conn.wsConn.WriteMessage(websocket.TextMessage, []byte("close"))
			conn.wsConn.Close()
		}
		delete(s.connMap, uuid)
	}
}

// HandleConnection handles a WebSocket connection with dynamic target
func (s *Server) HandleConnection(wsConn *websocket.Conn, targetHandler TargetHandler) {
	defer func() {
		if err := recover(); err != nil {
			log.Print("server connection panic: ", err)
		}
	}()

	// Read UUID from client
	t, buf, err := wsConn.ReadMessage()
	if err != nil || t == -1 || len(buf) == 0 {
		log.Print("ws uuid read err: ", err)
		wsConn.Close()
		return
	}

	if t != websocket.TextMessage {
		log.Print("invalid message type for uuid")
		wsConn.Close()
		return
	}

	uuid := string(buf)
	if uuid == "" {
		log.Print("empty uuid received")
		wsConn.Close()
		return
	}

	// Get target from handler
	target, isUdp, err := targetHandler(uuid)
	if err != nil {
		log.Print("target handler error: ", err)
		wsConn.WriteMessage(websocket.TextMessage, []byte("close"))
		wsConn.Close()
		return
	}

	// Check if connection already exists
	var conn *Connection
	if existingConn, haskey := s.getConn(uuid); haskey {
		conn = existingConn
		if conn.wsConn != nil {
			conn.wsConn.Close()
		}
		conn.wsConn = wsConn
		s.writeErrorBufWs(conn)
	} else {
		// Create new connection
		if isUdp {
			conn = s.createUDPConnection(uuid, target, wsConn)
		} else {
			conn = s.createTCPConnection(uuid, target, wsConn)
		}

		if conn == nil {
			return
		}

		go s.readTcpWebsocket(uuid)
	}

	go s.readWebsocketTcp(uuid)
}

func (s *Server) createTCPConnection(uuid, target string, wsConn *websocket.Conn) *Connection {
	log.Print("new tcp for ", uuid, " -> ", target)
	tcpConn, err := net.Dial("tcp", target)
	if err != nil {
		log.Print("connect to tcp err: ", err)
		wsConn.WriteMessage(websocket.TextMessage, []byte("close"))
		wsConn.Close()
		return nil
	}

	conn := &Connection{
		isUdp:   false,
		tcpConn: tcpConn,
		wsConn:  wsConn,
		uuid:    uuid,
		del:     false,
		t:       time.Now().Unix(),
	}
	s.setConn(uuid, conn)
	return conn
}

func (s *Server) createUDPConnection(uuid, target string, wsConn *websocket.Conn) *Connection {
	log.Print("new udp for ", uuid, " -> ", target)
	udpAddr, err := net.ResolveUDPAddr("udp4", target)
	if err != nil {
		log.Print("resolve udp addr err: ", err)
		wsConn.WriteMessage(websocket.TextMessage, []byte("close"))
		wsConn.Close()
		return nil
	}

	udpConn, err := net.DialUDP("udp", nil, udpAddr)
	if err != nil {
		log.Print("connect to udp err: ", err)
		wsConn.WriteMessage(websocket.TextMessage, []byte("close"))
		wsConn.Close()
		return nil
	}

	conn := &Connection{
		isUdp:   true,
		udpConn: udpConn,
		wsConn:  wsConn,
		uuid:    uuid,
		del:     false,
		t:       time.Now().Unix(),
	}
	s.setConn(uuid, conn)
	return conn
}

func (s *Server) readTcpWebsocket(uuid string) {
	defer func() {
		if err := recover(); err != nil {
			log.Print(uuid, " tcp -> ws panic: ", err)
		}
	}()

	conn, haskey := s.getConn(uuid)
	if !haskey {
		return
	}

	buf := make([]byte, 500000)
	for {
		if conn.del {
			return
		}

		var length int
		var err error

		if conn.isUdp {
			if conn.udpConn == nil {
				return
			}
			length, conn.udpAddr, err = conn.udpConn.ReadFromUDP(buf)
		} else {
			if conn.tcpConn == nil {
				return
			}
			length, err = conn.tcpConn.Read(buf)
		}

		if err != nil {
			if conn, haskey := s.getConn(uuid); haskey && !conn.del {
				if err.Error() != "EOF" {
					log.Print(uuid, " read err: ", err)
				}
				s.deleteConn(uuid)
			}
			return
		}

		if length > 0 {
			conn, haskey := s.getConn(uuid)
			if !haskey || conn.del || conn.wsConn == nil {
				return
			}

			conn.t = time.Now().Unix()
			if err = conn.wsConn.WriteMessage(s.msgType, buf[:length]); err != nil {
				log.Print(uuid, " ws write err: ", err)
				conn.wsConn.Close()
				s.saveErrorBuf(conn, buf, length)
			}
		}
	}
}

func (s *Server) readWebsocketTcp(uuid string) {
	defer func() {
		if err := recover(); err != nil {
			log.Print(uuid, " ws -> tcp panic: ", err)
		}
	}()

	conn, haskey := s.getConn(uuid)
	if !haskey {
		return
	}

	for {
		if conn.del || conn.wsConn == nil {
			return
		}

		if !conn.isUdp && conn.tcpConn == nil {
			return
		}
		if conn.isUdp && conn.udpConn == nil {
			return
		}

		t, buf, err := conn.wsConn.ReadMessage()
		if err != nil || t == -1 {
			conn.wsConn.Close()
			if conn, haskey := s.getConn(uuid); haskey && !conn.del {
				log.Print(uuid, " ws read err: ", err)
			}
			return
		}

		if len(buf) > 0 {
			conn.t = time.Now().Unix()
			if t == websocket.TextMessage {
				msg := string(buf)
				if msg == "tcp" {
					continue
				} else if msg == "close" {
					log.Print(uuid, " say bye")
					s.deleteConn(uuid)
					return
				}
			}

			s.msgType = t
			if conn.isUdp {
				if _, err = conn.udpConn.Write(buf); err != nil {
					log.Print(uuid, " udp write err: ", err)
					s.deleteConn(uuid)
					return
				}
			} else {
				if _, err = conn.tcpConn.Write(buf); err != nil {
					log.Print(uuid, " tcp write err: ", err)
					s.deleteConn(uuid)
					return
				}
			}
		}
	}
}

func (s *Server) writeErrorBufWs(conn *Connection) {
	if conn != nil && conn.wsConn != nil {
		for i := 0; i < len(conn.buf); i++ {
			conn.wsConn.WriteMessage(websocket.BinaryMessage, conn.buf[i])
		}
		conn.buf = nil
	}
}

func (s *Server) saveErrorBuf(conn *Connection, buf []byte, length int) {
	if conn != nil {
		tmp := make([]byte, length)
		copy(tmp, buf[:length])
		if conn.buf == nil {
			conn.buf = [][]byte{tmp}
		} else {
			conn.buf = append(conn.buf, tmp)
		}
	}
}

// StartHeartbeat starts the heartbeat mechanism for server connections
func (s *Server) StartHeartbeat() {
	go func() {
		for {
			time.Sleep(2 * 60 * time.Second)
			nowTimeCut := time.Now().Unix() - 2*60

			s.connMapLock.RLock()
			connList := make([]*Connection, 0, len(s.connMap))
			for _, conn := range s.connMap {
				connList = append(connList, conn)
			}
			s.connMapLock.RUnlock()

			for _, conn := range connList {
				if conn.t < nowTimeCut {
					if conn.isUdp {
						log.Print(conn.uuid, " udp timeout close")
						s.deleteConn(conn.uuid)
					} else if err := conn.wsConn.WriteMessage(websocket.TextMessage, []byte("tcp")); err != nil {
						log.Print(conn.uuid, " tcp timeout close")
						conn.wsConn.Close()
						s.deleteConn(conn.uuid)
					}
				}
			}
		}
	}()
}

// Client methods

// ConnectTCP creates a TCP connection through WebSocket
func (c *Client) ConnectTCP(localConn net.Conn) error {
	uuid := uuid.New().String()[31:]
	return c.handleConnection(localConn, uuid, false)
}

// ConnectUDP creates a UDP connection through WebSocket
func (c *Client) ConnectUDP(localConn *net.UDPConn) error {
	u := "U" + uuid.New().String()[32:]
	return c.handleConnection(localConn, u, true)
}

func (c *Client) handleConnection(localConn interface{}, uuid string, isUdp bool) error {
	// Dial WebSocket
	dialer := websocket.Dialer{
		TLSClientConfig: &tls.Config{RootCAs: nil, InsecureSkipVerify: true},
		Proxy:           http.ProxyFromEnvironment,
	}
	requestHeader := http.Header{}
	for k, v := range c.header {
		requestHeader.Set(k, v)
	}
	wsConn, _, err := dialer.Dial(c.wsAddr, requestHeader)
	if err != nil {
		return err
	}

	// Send UUID
	if err := wsConn.WriteMessage(websocket.TextMessage, []byte(uuid)); err != nil {
		wsConn.Close()
		return err
	}

	// Start forwarding goroutines
	if isUdp {
		udpConn := localConn.(*net.UDPConn)
		go c.forwardUDPToWS(udpConn, wsConn, uuid)
		go c.forwardWSToUDP(udpConn, wsConn, uuid)
	} else {
		tcpConn := localConn.(net.Conn)
		go c.forwardTCPToWS(tcpConn, wsConn, uuid)
		go c.forwardWSToTCP(tcpConn, wsConn, uuid)
	}

	return nil
}

func (c *Client) forwardTCPToWS(tcpConn net.Conn, wsConn *websocket.Conn, uuid string) {
	defer func() {
		if err := recover(); err != nil {
			log.Print(uuid, " tcp->ws panic: ", err)
		}
		tcpConn.Close()
		wsConn.Close()
	}()

	buf := make([]byte, 500000)
	for {
		length, err := tcpConn.Read(buf)
		if err != nil {
			return
		}
		if length > 0 {
			if err = wsConn.WriteMessage(c.msgType, buf[:length]); err != nil {
				return
			}
		}
	}
}

func (c *Client) forwardWSToTCP(tcpConn net.Conn, wsConn *websocket.Conn, uuid string) {
	defer func() {
		if err := recover(); err != nil {
			log.Print(uuid, " ws->tcp panic: ", err)
		}
		tcpConn.Close()
		wsConn.Close()
	}()

	for {
		t, buf, err := wsConn.ReadMessage()
		if err != nil {
			return
		}
		if len(buf) > 0 {
			if t == websocket.TextMessage {
				msg := string(buf)
				if msg == "tcp" {
					continue
				} else if msg == "close" {
					return
				}
			}
			c.msgType = t
			if _, err = tcpConn.Write(buf); err != nil {
				return
			}
		}
	}
}

func (c *Client) forwardUDPToWS(udpConn *net.UDPConn, wsConn *websocket.Conn, uuid string) {
	defer func() {
		if err := recover(); err != nil {
			log.Print(uuid, " udp->ws panic: ", err)
		}
		udpConn.Close()
		wsConn.Close()
	}()

	buf := make([]byte, 500000)
	for {
		length, _, err := udpConn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		if length > 0 {
			if err = wsConn.WriteMessage(c.msgType, buf[:length]); err != nil {
				return
			}
		}
	}
}

func (c *Client) forwardWSToUDP(udpConn *net.UDPConn, wsConn *websocket.Conn, uuid string) {
	defer func() {
		if err := recover(); err != nil {
			log.Print(uuid, " ws->udp panic: ", err)
		}
		udpConn.Close()
		wsConn.Close()
	}()

	for {
		t, buf, err := wsConn.ReadMessage()
		if err != nil {
			return
		}
		if len(buf) > 0 {
			if t == websocket.TextMessage {
				msg := string(buf)
				if msg == "tcp" {
					continue
				} else if msg == "close" {
					return
				}
			}
			c.msgType = t
			if _, err = udpConn.Write(buf); err != nil {
				return
			}
		}
	}
}
