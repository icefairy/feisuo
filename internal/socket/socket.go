// Package socket 提供 feisuo 的控制/介入通道：
// - Server：监听 Unix socket，广播会话收发事件，接收 attach 客户端的输入注入
// - Client：feisuo attach 命令用，连接 server 显示广播 + 发送输入
package socket

import (
	"bufio"
	"encoding/json"
	"log"
	"net"
	"os"
	"sync"
)

// Server 会话广播服务器
type Server struct {
	path string
	ln   net.Listener
	mu   sync.Mutex
	subs map[net.Conn]bool
	// OnInput 处理 attach 注入的输入（main 注入：Enqueue 到会话）。nil 则忽略。
	OnInput func(session, content, sender string)
}

func NewServer(path string) *Server {
	return &Server{path: path, subs: make(map[net.Conn]bool)}
}

// Start 启动监听（unix socket）。失败返回 error（如路径已占用）。
func (s *Server) Start() error {
	os.Remove(s.path) // 清理残留 socket 文件
	ln, err := net.Listen("unix", s.path)
	if err != nil {
		return err
	}
	s.ln = ln
	go s.acceptLoop()
	log.Printf("[socket] listening on %s", s.path)
	return nil
}

func (s *Server) acceptLoop() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.subs[conn] = true
		s.mu.Unlock()
		go s.handleConn(conn)
	}
}

// Broadcast 向所有订阅者广播一行文本（会话收发事件）。
func (s *Server) Broadcast(line string) {
	s.mu.Lock()
	subs := make([]net.Conn, 0, len(s.subs))
	for c := range s.subs {
		subs = append(subs, c)
	}
	s.mu.Unlock()
	msg, _ := json.Marshal(map[string]string{"type": "event", "line": line})
	for _, c := range subs {
		if _, err := c.Write(append(msg, '\n')); err != nil {
			s.removeConn(c)
		}
	}
}

// handleConn 处理 attach 客户端连接：
// 客户端发 {"type":"input","session":"ice","content":"..."} → 通过注入回调转发给 feisuo。
// OnInput 由 main 注入（nil 则忽略输入）。
func (s *Server) handleConn(conn net.Conn) {
	defer s.removeConn(conn)
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		var req struct {
			Type    string `json:"type"`
			Session string `json:"session"`
			Content string `json:"content"`
			Sender  string `json:"sender"`
		}
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			continue
		}
		if req.Type == "input" && s.OnInput != nil {
			s.OnInput(req.Session, req.Content, req.Sender)
		}
	}
}

func (s *Server) removeConn(conn net.Conn) {
	s.mu.Lock()
	delete(s.subs, conn)
	s.mu.Unlock()
	conn.Close()
}

// Close 关闭服务器
func (s *Server) Close() {
	if s.ln != nil {
		s.ln.Close()
	}
	os.Remove(s.path)
}

// --- Client ---

// Client attach 客户端（feisuo attach <会话> 命令用）
type Client struct {
	conn net.Conn
	sc   *bufio.Scanner
}

// Connect 连接 unix socket
func Connect(path string) (*Client, error) {
	conn, err := net.Dial("unix", path)
	if err != nil {
		return nil, err
	}
	return &Client{conn: conn, sc: bufio.NewScanner(conn)}, nil
}

// SendInput 发送输入注入
func (c *Client) SendInput(session, content, sender string) error {
	msg, _ := json.Marshal(map[string]string{
		"type": "input", "session": session, "content": content, "sender": sender,
	})
	_, err := c.conn.Write(append(msg, '\n'))
	return err
}

// NextEvent 读取下一条广播事件（阻塞）。返回 false 表示连接关闭。
func (c *Client) NextEvent() (string, bool) {
	if !c.sc.Scan() {
		return "", false
	}
	var ev struct {
		Type string `json:"type"`
		Line string `json:"line"`
	}
	if err := json.Unmarshal([]byte(c.sc.Text()), &ev); err != nil {
		return "", true // 无法解析，跳过
	}
	return ev.Line, true
}

func (c *Client) Close() { c.conn.Close() }
