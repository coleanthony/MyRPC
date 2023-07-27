package MyRPC

import (
	"MyRPC/codec"
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type ServerError string

// Call represents an active RPC.
type Call struct {
	Seq           uint64
	ServiceMethod string     // The name of the service and method to call. format "<service>.<method>
	Args          any        // The argument to the function (*struct).
	Reply         any        // The reply from the function (*struct).
	Error         error      // After completion, the error status.
	Done          chan *Call // Receives *Call when Go is complete.
}

func (call *Call) done() {
	call.Done <- call
}

// Client represents an RPC Client.
// There may be multiple outstanding Calls associated
// with a single Client, and a Client may be used by
// multiple goroutines simultaneously.
/*
cc 是消息的编解码器，和服务端类似，用来序列化将要发送出去的请求，以及反序列化接收到的响应。
reqMutex 是一个互斥锁，和服务端类似，为了保证请求的有序发送，即防止出现多个请求报文混淆。
header 是每个请求的消息头，header 只有在请求发送时才需要，而请求发送是互斥的，因此每个客户端只需要一个，声明在 Client 结构体中可以复用。
seq 用于给发送的请求编号，每个请求拥有唯一编号。
pending 存储未处理完的请求，键是编号，值是 Call 实例。
closing 和 shutdown 任意一个值置为 true，则表示 Client 处于不可用的状态，但有些许的差别，closing 是用户主动关闭的，即调用 Close 方法，而 shutdown 置为 true 一般是有错误发生。
*/
type Client struct {
	cc       codec.Codec
	opt      *Option
	reqMutex sync.Mutex // protects following
	header   codec.Header
	mutex    sync.Mutex // protects following
	seq      uint64
	pending  map[uint64]*Call
	closing  bool // user has called Close
	shutdown bool // server has told us to stop
}

type clientResult struct {
	client *Client
	err    error
}

var _ io.Closer = (*Client)(nil)

var Errshutdown = errors.New("connection is shut down")

type newClientfunc func(conn net.Conn, opt *Option) (client *Client, err error)

func dialTimeout(f newClientfunc, network, address string, opts ...*Option) (client *Client, err error) {
	opt, err := parseOptions(opts...)
	if err != nil {
		return nil, err
	}
	conn, err := net.DialTimeout(network, address, opt.ConnectTimeout)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = conn.Close()
		}
	}()
	ch := make(chan clientResult)
	go func() {
		client, err := f(conn, opt)
		ch <- clientResult{
			client: client,
			err:    err,
		}
	}()
	if opt.ConnectTimeout == 0 {
		result := <-ch
		return result.client, result.err
	}
	select {
	case <-time.After(opt.ConnectTimeout):
		return nil, fmt.Errorf("rpc client: connect timeout: expect within %s", opt.ConnectTimeout)
	case result := <-ch:
		return result.client, result.err
	}
}

// close the connection
func (client *Client) Close() error {
	client.mutex.Lock()
	defer client.mutex.Unlock()
	if client.closing {
		return Errshutdown
	}
	client.closing = true
	return client.cc.Close()
}

// return true id the client is working
func (client *Client) IsAvailable() bool {
	client.mutex.Lock()
	defer client.mutex.Unlock()
	return client.closing == false && client.shutdown == false
}

//定义三个与call相关的方法
//registerCall：将参数 call 添加到 client.pending 中，并更新 client.seq。
//removeCall：根据 seq，从 client.pending 中移除对应的 call，并返回。
//terminateCalls：服务端或客户端发生错误时调用，将 shutdown 设置为 true，且将错误信息通知所有 pending 状态的 call。

func (client *Client) registerCall(call *Call) (uint64, error) {
	client.mutex.Lock()
	defer client.mutex.Unlock()
	if client.closing || client.shutdown {
		return 0, Errshutdown
	}
	call.Seq = client.seq
	client.pending[call.Seq] = call
	client.seq++
	return call.Seq, nil
}

func (client *Client) removeCall(seq uint64) *Call {
	client.mutex.Lock()
	defer client.mutex.Unlock()
	call := client.pending[seq]
	delete(client.pending, seq)
	return call
}

func (client *Client) terminateCalls(err error) {
	client.reqMutex.Lock()
	defer client.reqMutex.Unlock()
	client.mutex.Lock()
	defer client.mutex.Unlock()
	client.shutdown = true
	for _, call := range client.pending {
		call.Error = err
		call.done()
	}
}

// 客户端接收响应
// 1.call 不存在，可能是请求没有发送完整，或者因为其他原因被取消，但是服务端仍旧处理了。
// 2.call 存在，但服务端处理出错，即 h.Error 不为空。
// 3.call 存在，服务端处理正常，那么需要从 body 中读取 Reply 的值。
func (client *Client) Receive() {
	var err error
	for err == nil {
		var h codec.Header
		if err := client.cc.ReadHeader(&h); err != nil {
			break
		}
		call := client.removeCall(h.Seq)
		switch {
		case call == nil:
			//1.call不存在
			err = client.cc.ReadBody(nil)
		case h.Error != "":
			call.Error = fmt.Errorf(h.Error)
			err = client.cc.ReadBody(nil)
			call.done()
		default:
			//服务端正常，从body中读取reply值
			err = client.cc.ReadBody(call.Reply)
			if err != nil {
				call.Error = errors.New("client read body error:" + err.Error())
			}
			call.done()
		}

	}
	client.terminateCalls(err)
}

//创建 Client 实例时，首先需要完成一开始的协议交换，即发送 Option 信息给服务端。
//协商好消息的编解码方式之后，再创建一个子协程调用 receive() 接收响应。

func NewClient(conn net.Conn, opt *Option) (*Client, error) {
	f := codec.NewCodecFuncMap[opt.CodecType]
	if f == nil {
		err := fmt.Errorf("invalid error type %s", opt.CodecType)
		log.Println("rpc client:codec error:", err)
		return nil, err
	}
	//不是nil，就可以发是报告option
	if err := json.NewEncoder(conn).Encode(opt); err != nil {
		log.Println("rpc client:options error", err)
		_ = conn.Close()
		return nil, err
	}
	return NewClientCodec(f(conn), opt), nil
}

func NewClientCodec(cc codec.Codec, opt *Option) *Client {
	client := &Client{
		cc:      cc,
		opt:     opt,
		seq:     1,
		pending: make(map[uint64]*Call),
	}
	go client.Receive()
	return client
}

// Dial 函数，便于用户传入服务端地址，创建 Client 实例。
// 为了简化用户调用，通过 ...*Option 将 Option 实现为可选参数。
func parseOptions(opts ...*Option) (*Option, error) {
	if len(opts) == 0 || opts[0] == nil {
		return DefaultOption, nil
	}
	if len(opts) != 1 {
		return nil, errors.New("number of options more than one")
	}
	opt := opts[0]
	opt.MagicNumber = DefaultOption.MagicNumber
	if opt.CodecType == "" {
		opt.CodecType = DefaultOption.CodecType
	}
	return opt, nil
}

// 通过dial与RPC server通过网络地址链接
func Dial(network string, address string, opts ...*Option) (client *Client, err error) {
	/*opt, err := parseOptions(opts...)
	if err != nil {
		return nil, err
	}
	conn, err := net.Dial(network, address)
	if err != nil {
		log.Println("dial error: network dial error")
		return nil, err
	}
	//如果client为nil，就关闭这个连接
	defer func() {
		if client == nil {
			_ = conn.Close()
		}
	}()
	return NewClient(conn, opt)*/
	return dialTimeout(NewClient, network, address, opts...)
}

//Send 发送请求

func (client *Client) send(call *Call) {
	//保证client
	client.reqMutex.Lock()
	defer client.reqMutex.Unlock()

	//注册一个call
	seq, err := client.registerCall(call)
	if err != nil {
		call.Error = err
		call.done()
		return
	}

	client.header.ServiceMethod = call.ServiceMethod
	client.header.Seq = call.Seq
	client.header.Error = ""

	//encode然后发送request
	if err := client.cc.Write(&client.header, call.Args); err != nil {
		call := client.removeCall(seq)

		if call != nil {
			call.Error = err
			call.done()
		}
	}
}

// Go 和 Call 是客户端暴露给用户的两个 RPC 服务调用接口，Go 是一个异步接口，返回 call 实例。
// Call 是对 Go 的封装，阻塞 call.Done，等待响应返回，是一个同步接口。
// Go invokes the function asynchronously.
// It returns the Call structure representing the invocation.
func (client *Client) Go(serveiceMethod string, args interface{}, reply interface{}, done chan *Call) *Call {
	if done == nil {
		done = make(chan *Call, 10)
	} else if cap(done) == 0 {
		log.Panic("rpc client:done channel is unbuffered")
	}
	call := &Call{
		ServiceMethod: serveiceMethod,
		Args:          args,
		Reply:         reply,
		Done:          done,
	}
	client.send(call)
	return call
}

// Call invokes the named function, waits for it to complete,
// and returns its error status.
func (client *Client) Call(ctx context.Context, serviceMethod string, args, reply interface{}) error {
	//Client.Call 的超时处理机制，使用 context 包实现，控制权交给用户，控制更为灵活。
	call := client.Go(serviceMethod, args, reply, make(chan *Call, 1))
	select {
	case <-ctx.Done():
		client.removeCall(call.Seq)
		return errors.New("rpc client:call failed:" + ctx.Err().Error())
	case call := <-call.Done:
		return call.Error
	}
}

// 服务端已经能够接受 CONNECT 请求，并返回了 200 状态码 HTTP/1.0 200 Connected to Gee RPC，
// 客户端要做的，发起 CONNECT 请求，检查返回状态码即可成功建立连接。
func NewHTTPClient(conn net.Conn, opt *Option) (*Client, error) {
	_, _ = io.WriteString(conn, fmt.Sprintf("CONNECT %s HTTP/1.0\n\n", defaultRPCPath))
	// Require successful HTTP response
	// before switching to RPC protocol.
	resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: "CONNECT"})
	if err == nil && resp.Status == connected {
		return NewClient(conn, opt)
	}
	if err == nil {
		err = errors.New("unexpected HTTP response: " + resp.Status)
	}
	return nil, err
}

// DialHTTP connects to an HTTP RPC server at the specified network address
// listening on the default HTTP RPC path.
func DialHTTP(network, address string, opts ...*Option) (*Client, error) {
	return dialTimeout(NewHTTPClient, network, address, opts...)
}

//通过 HTTP CONNECT 请求建立连接之后，后续的通信过程就交给 NewClient 了。
//
//为了简化调用，提供了一个统一入口 XDial
// XDial calls different functions to connect to a RPC server
// according the first parameter rpcAddr.
// rpcAddr is a general format (protocol@addr) to represent a rpc server
// eg, http@10.0.0.1:7001, tcp@10.0.0.1:9999, unix@/tmp/geerpc.sock

func XDial(rpcAddress string, opts ...*Option) (*Client, error) {
	parts := strings.Split(rpcAddress, "@")
	if len(parts) != 2 {
		return nil, fmt.Errorf("rpc client err: wrong format '%s',expect format@addr", rpcAddress)
	}
	protocal, addr := parts[0], parts[1]
	switch protocal {
	case "http":
		return DialHTTP("tcp", addr, opts...)
	default:
		return Dial(protocal, addr, opts...)
	}
}
