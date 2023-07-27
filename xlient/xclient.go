package xlient

import (
	"MyRPC"
	"context"
	"io"
	"reflect"
	"sync"
)

type XClient struct {
	d       Discovery
	mode    SelectMode
	opt     *MyRPC.Option
	mutex   sync.Mutex
	clients map[string]*MyRPC.Client
}

var _ io.Closer = (*XClient)(nil)

// XClient 的构造函数需要传入三个参数，服务发现实例 Discovery、负载均衡模式 SelectMode 以及协议选项 Option。
// 为了尽量地复用已经创建好的 Socket 连接，使用 clients 保存创建成功的 Client 实例，并提供 Close 方法在结束后，关闭已经建立的连接。
func NewXClient(d Discovery, mode SelectMode, opt *MyRPC.Option) *XClient {
	client := XClient{
		d:       d,
		mode:    mode,
		opt:     opt,
		clients: make(map[string]*MyRPC.Client),
	}
	return &client
}

func (XCl *XClient) Close() error {
	XCl.mutex.Lock()
	defer XCl.mutex.Unlock()
	for key, client := range XCl.clients {
		_ = client.Close()
		delete(XCl.clients, key)
	}
	return nil
}

// 检查 xc.clients 是否有缓存的 Client，如果有，检查是否是可用状态，如果是则返回缓存的 Client，如果不可用，则从缓存中删除。
// 如果步骤 1) 没有返回缓存的 Client，则说明需要创建新的 Client，缓存并返回。
func (XCl *XClient) dial(rpcAddress string) (*MyRPC.Client, error) {
	XCl.mutex.Lock()
	defer XCl.mutex.Unlock()
	client, ok := XCl.clients[rpcAddress]
	if ok && !client.IsAvailable() {
		_ = client.Close()
		delete(XCl.clients, rpcAddress)
		client = nil
	}
	if client == nil {
		var err error
		client, err = MyRPC.XDial(rpcAddress, XCl.opt)
		if err != nil {
			return nil, err
		}
		XCl.clients[rpcAddress] = client
	}
	return client, nil
}

func (XCl *XClient) call(rpcAddress string, ctx context.Context, servicemethod string, args, reply interface{}) error {
	client, err := XCl.dial(rpcAddress)
	if err != nil {
		return err
	}
	return client.Call(ctx, servicemethod, args, reply)
}

func (XCl *XClient) Call(ctx context.Context, servicemethod string, args, reply interface{}) error {
	rpcAddress, err := XCl.d.Get(XCl.mode)
	if err != nil {
		return err
	}
	return XCl.call(rpcAddress, ctx, servicemethod, args, reply)
}

// Broadcast 将请求广播到所有的服务实例，如果任意一个实例发生错误，则返回其中一个错误；如果调用成功，则返回其中一个的结果。
// Broadcast invokes the named function for every server registered in discovery
func (XCl *XClient) Broadcast(ctx context.Context, servicemethod string, args, reply interface{}) error {
	servers, err := XCl.d.GetAll()
	if err != nil {
		return err
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var e error
	replyDone := reply == nil
	ctx, cancel := context.WithCancel(ctx)
	for _, rpcAddress := range servers {
		wg.Add(1)
		go func(rpcaddr string) {
			defer wg.Done()
			var clonedReply interface{}
			if reply != nil {
				clonedReply = reflect.New(reflect.ValueOf(reply).Elem().Type()).Interface()
			}
			err := XCl.call(rpcaddr, ctx, servicemethod, args, clonedReply)
			mu.Lock()
			if err != nil && e == nil {
				e = err
				cancel()
			}
			if err != nil && !replyDone {
				reflect.ValueOf(reply).Elem().Set(reflect.ValueOf(clonedReply).Elem())
				replyDone = true
			}
			mu.Unlock()
		}(rpcAddress)
	}
	wg.Wait()
	return e
}
