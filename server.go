package MyRPC

import (
	"MyRPC/codec"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"time"
)

const MagicNumber = 0x3bef5c

type Option struct {
	MagicNumber    int
	CodecType      codec.Type
	ConnectTimeout time.Duration
	HandleTimeout  time.Duration
}

var DefaultOption = &Option{
	MagicNumber:    MagicNumber,
	CodecType:      codec.GobType,
	ConnectTimeout: time.Second * 10,
}

type Server struct {
	serviceMap sync.Map
}

func NewServer() *Server {
	return &Server{}
}

var DefaultServer = NewServer()

// 注册method
func (server *Server) Register(receiveval interface{}) error {
	s := newService(receiveval)
	if _, duplica := server.serviceMap.LoadOrStore(s.name, s); duplica {
		//存在重复
		return errors.New("rpc server:service already defined " + s.name)
	}
	return nil
}

func Register(receiveval interface{}) error {
	return DefaultServer.Register(receiveval)
}

// 实现findService方法，通过 ServiceMethod 从 serviceMap 中找到对应的 service
func (server *Server) findService(serviveMethod string) (svice *service, mtype *methodType, err error) {
	//ServiceMethod 的构成是 “Service.Method”，因此先将其分割成 2 部分，第一部分是 Service 的名称，第二部分即方法名。
	//先在 serviceMap 中找到对应的 service 实例，再从 service 实例的 method 中，找到对应的 methodType
	id := strings.LastIndex(serviveMethod, ".")
	if id < 0 {
		err = errors.New("rpc server:service method filled to found " + serviveMethod)
		return
	}
	serviceName, methodName := serviveMethod[0:id], serviveMethod[id+1:]
	sviceinterface, sviceok := server.serviceMap.Load(serviceName)
	if !sviceok {
		err = errors.New("rpc server:service filled to found " + serviceName)
		return
	}
	svice = sviceinterface.(*service)
	mtype = svice.method[methodName]
	if mtype == nil {
		err = errors.New("rpc server:method filled to found " + methodName)
	}
	return
}

// Accept accepts connections on the listener and serves requests
// for each incoming connection.
func (server *Server) Accept(lis net.Listener) {
	//for 循环等待 socket 连接建立，并开启子协程处理
	//处理过程交给了 ServerConn 方法
	for {
		conn, err := lis.Accept()
		if err != nil {
			log.Println("rpc server:accept error:", err)
			return
		}
		go server.ServeConn(conn)
	}
}

// 对于所有到来的连接，接收该连接并且提供服务请求
func Accept(lis net.Listener) {
	DefaultServer.Accept(lis)
}

// ServeConn runs the server on a single connection.
// ServeConn blocks, serving the connection until the client hangs up.
func (server *Server) ServeConn(conn io.ReadWriteCloser) {
	defer func() { _ = conn.Close() }()
	var opt Option
	if err := json.NewDecoder(conn).Decode(&opt); err != nil {
		log.Println("rpc server:option error", err)
		return
	}

	if opt.MagicNumber != MagicNumber {
		log.Printf("rpc server:invalid magicnumber %x", opt.MagicNumber)
		return
	}
	f := codec.NewCodecFuncMap[opt.CodecType]
	if f == nil {
		log.Printf("rpc server:invalid codec type %s\n", opt.CodecType)
	}
	//根据 CodeType 得到对应的消息编解码器，接下来的处理交给 serverCodec。
	server.serveCodec(f(conn), &opt)
}

var inValidRequest = struct{}{}

// 主要包含三个阶段
// 读取请求 readRequest
// 处理请求 handleRequest
// 回复请求 sendResponse
func (server *Server) serveCodec(cc codec.Codec, opt *Option) {
	sending := new(sync.Mutex)
	wg := new(sync.WaitGroup)
	for {
		req, err := server.readRequest(cc)
		if err != nil {
			if req == nil {
				break
			}
			req.header.Error = err.Error()
			server.sendResponse(cc, req.header, inValidRequest, sending)
			continue
		}
		wg.Add(1)
		go server.handleRequest(cc, req, sending, wg, opt.ConnectTimeout)
	}
	wg.Wait()
	err := cc.Close()
	if err != nil {
		log.Println("server error:close codec error:", err)
	}
}

type request struct {
	header       *codec.Header //request 头信息
	argv, replyv reflect.Value //argv and replyv
	mtype        *methodType
	svice        *service
}

func (server *Server) readRequestHeader(cc codec.Codec) (*codec.Header, error) {
	head := codec.Header{}
	if err := cc.ReadHeader(&head); err != nil {
		if err != io.EOF && err != io.ErrUnexpectedEOF {
			log.Println("rpc server: read header error:", err)
		}
		return nil, err
	}
	return &head, nil
}

func (server *Server) readRequest(cc codec.Codec) (*request, error) {
	h, err := server.readRequestHeader(cc)
	if err != nil {
		return nil, err
	}
	req := &request{header: h}

	req.svice, req.mtype, err = server.findService(h.ServiceMethod)
	if err != nil {
		return req, err
	}

	//通过 newArgv() 和 newReplyv() 两个方法创建出两个入参实例，然后通过 cc.ReadBody() 将请求报文反序列化为第一个入参 argv
	//需要注意 argv 可能是值类型，也可能是指针类型，所以处理方式有点差异。
	req.argv = req.mtype.newArgv()
	req.replyv = req.mtype.newRelpyv()

	argvInterface := req.argv.Interface()
	if req.argv.Type().Kind() != reflect.Ptr {
		argvInterface = req.argv.Addr().Interface()
	}

	if err = cc.ReadBody(argvInterface); err != nil {
		log.Println("rpc server:read body error:", err)
		return req, err
	}
	return req, nil
}

func (server *Server) handleRequest(cc codec.Codec, req *request, sending *sync.Mutex, wg *sync.WaitGroup, timeout time.Duration) {
	defer wg.Done()
	//调用service call方法，再将 replyv 传递给 sendResponse 完成序列化即可。
	called := make(chan struct{})
	sent := make(chan struct{})
	//called 信道接收到消息，代表处理没有超时，继续执行 sendResponse。
	//time.After() 先于 called 接收到消息，说明处理已经超时，called 和 sent 都将被阻塞。在 case <-time.After(timeout) 处调用 sendResponse。
	go func() {
		err := req.svice.call(req.mtype, req.argv, req.replyv)
		called <- struct{}{}
		if err != nil {
			req.header.Error = err.Error()
			server.sendResponse(cc, req.header, inValidRequest, sending)
			sent <- struct{}{}
			return
		}
		server.sendResponse(cc, req.header, req.replyv.Interface(), sending)
		sent <- struct{}{}
	}()

	if timeout == 0 {
		<-called
		<-sent
		return
	}
	select {
	case <-time.After(timeout):
		req.header.Error = fmt.Sprintf("rpc server:request handle timeout:expect within %s", timeout)
		server.sendResponse(cc, req.header, inValidRequest, sending)
	case <-called:
		<-sent
	}
}

func (server *Server) sendResponse(cc codec.Codec, header *codec.Header, body interface{}, sending *sync.Mutex) {
	sending.Lock()
	defer sending.Unlock()
	if err := cc.Write(header, body); err != nil {
		log.Println("rpc server:send response error: ", err)
	}
}

// 服务端支持HTTP协议
const (
	connected        = "200 Connected to MyRPC"
	defaultRPCPath   = "/_myrpc_"
	defaultDebugPath = "/debug/myrpc"
)

// ServeHTTP implements an http.Handler that answers RPC requests.
func (server *Server) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method != "CONNECT" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusMethodNotAllowed)
		_, _ = io.WriteString(w, "405 must CONNECT\n")
		return
	}
	conn, _, err := w.(http.Hijacker).Hijack()
	if err != nil {
		log.Print("rpc hijacking", req.RemoteAddr, ": ", err.Error())
		return
	}
	_, _ = io.WriteString(conn, "HTTP/1.0 "+connected+"\n\n")
	server.ServeConn(conn)
}

// HandleHTTP registers an HTTP handler for RPC messages on rpcPath.
// It is still necessary to invoke http.Serve(), typically in a go statement.
func (server *Server) HandleHTTP() {
	http.Handle(defaultRPCPath, server)
	http.Handle(defaultDebugPath, debugHTTP{server})
	log.Println("rpc server debug path:", defaultDebugPath)
}

// HandleHTTP is a convenient approach for default server to register HTTP handlers
func HandleHTTP() {
	DefaultServer.HandleHTTP()
}
