package main

import (
	"MyRPC"
	"MyRPC/xlient"
	"context"
	"log"
	"net"
	"net/http"
	"sync"
	"time"
)

type Foo int
type Args struct {
	Num1, Num2 int
}

func (f Foo) Sum(args Args, reply *int) error {
	*reply = args.Num1 + args.Num2
	return nil
}

func (f Foo) Sleep(args Args, reply *int) error {
	time.Sleep(time.Second * time.Duration(args.Num1))
	*reply = args.Num1 + args.Num2
	return nil
}

func startServer(addr chan string) {
	var foo Foo
	if err := MyRPC.Register(&foo); err != nil {
		log.Fatal("register error ", err)
	}

	l, err := net.Listen("tcp", ":0")
	if err != nil {
		log.Fatal("network error", err)
	}
	log.Println("start rpc server on ", l.Addr())
	addr <- l.Addr().String()
	MyRPC.Accept(l)
}

func startServerHTTPdebug(addr chan string) {
	var foo Foo
	l, _ := net.Listen("tcp", ":9999")
	_ = MyRPC.Register(&foo)
	MyRPC.HandleHTTP()
	addr <- l.Addr().String()
	_ = http.Serve(l, nil)
}

// 测试loadbalance
func startServerLoadbalance(addr chan string) {
	var foo Foo
	l, _ := net.Listen("tcp", ":0")
	server := MyRPC.NewServer()
	_ = server.Register(&foo)
	addr <- l.Addr().String()
	server.Accept(l)
}

// 封装一个方法 foo，便于在 Call 或 Broadcast 之后统一打印成功或失败的日志。
func foo(xc *xlient.XClient, ctx context.Context, typ, servivemethod string, args *Args) {
	var reply int
	var err error
	switch typ {
	case "call":
		err = xc.Call(ctx, servivemethod, args, &reply)
	case "broadcast":
		err = xc.Broadcast(ctx, servivemethod, args, &reply)
	}
	if err != nil {
		log.Println("foo error:", err)
	} else {
		log.Printf("foo successful:%d+%d=%d", args.Num1, args.Num2, reply)
	}
}

// callblc与broadcastblc
func callblc(addr1, addr2 string) {
	d := xlient.NewMultiServerDiscovery([]string{"tcp@" + addr1, "tcp@" + addr2})
	xc := xlient.NewXClient(d, xlient.RandomSelect, nil)
	defer func() {
		err := xc.Close()
		if err != nil {
			log.Println("callblc close error")
			return
		}
	}()
	// send request & receive response
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			foo(xc, context.Background(), "call", "Foo.Sum", &Args{
				Num1: i,
				Num2: i * i,
			})
		}(i)
	}
	wg.Wait()
}

func broadcastblc(addr1, addr2 string) {
	d := xlient.NewMultiServerDiscovery([]string{"tcp@" + addr1, "tcp@" + addr2})
	xc := xlient.NewXClient(d, xlient.RandomSelect, nil)
	defer func() {
		err := xc.Close()
		if err != nil {
			log.Println("callblc close error")
			return
		}
	}()
	// send request & receive response
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			foo(xc, context.Background(), "broadcast", "Foo.Sum", &Args{
				Num1: i,
				Num2: i * i,
			})
			ctx, _ := context.WithTimeout(context.Background(), time.Second*2)
			foo(xc, ctx, "broadcast", "Foo.Sleep", &Args{
				Num1: i,
				Num2: i * i,
			})
		}(i)
	}
	wg.Wait()
}

/* test server day1
func main() {
	//启动server
	addr := make(chan string)
	go startServer(addr)

	conn, _ := net.Dial("tcp", <-addr)
	defer func() {
		_ = conn.Close()
	}()
	time.Sleep(time.Second)

	_ = json.NewEncoder(conn).Encode(MyRPC.DefaultOption)
	cc := codec.NewGobCodec(conn)

	for i := 0; i < 5; i++ {
		header := &codec.Header{
			ServiceMethod: "Func.Test",
			Seq:           uint64(i),
		}
		_ = cc.Write(header, fmt.Sprintf("myrpc req %d", header.Seq))
		_ = cc.ReadHeader(header)
		reply := ""
		_ = cc.ReadBody(&reply)
		log.Println("reply:", reply)
	}
}*/

/*day4
func main() {
	log.SetFlags(0)
	addr := make(chan string)
	go startServer(addr)
	client, _ := MyRPC.Dial("tcp", <-addr)
	defer func() { _ = client.Close() }()

	time.Sleep(time.Second)
	//发送请求，并且接收恢复
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			args := &Args{Num1: i, Num2: i * 5}
			var reply int
			if err := client.Call(context.Background(), "Foo.Sum", args, &reply); err != nil {
				log.Fatal("call Foo.Sum error", err)
			}
			log.Printf("%d+%d = reply:%d", args.Num1, args.Num2, reply)
		}(i)
	}
	wg.Wait()
}
*/

func CallHTTPdebug(addr chan string) {
	client, _ := MyRPC.DialHTTP("tcp", <-addr)
	defer func() { _ = client.Close() }()

	time.Sleep(time.Second)
	//发送请求，并且接收恢复
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			args := &Args{Num1: i, Num2: i * 5}
			var reply int
			if err := client.Call(context.Background(), "Foo.Sum", args, &reply); err != nil {
				log.Fatal("call Foo.Sum error", err)
			}
			log.Printf("%d+%d = reply:%d", args.Num1, args.Num2, reply)
		}(i)
	}
	wg.Wait()
}

/*
func main() {
	log.SetFlags(0)
	addr := make(chan string)
	go CallHTTPdebug(addr)
	startServerHTTPdebug(addr)
}*/

func main() {
	log.SetFlags(0)
	ch1 := make(chan string)
	ch2 := make(chan string)
	go startServerLoadbalance(ch1)
	go startServerLoadbalance(ch2)
	addr1 := <-ch1
	addr2 := <-ch2
	time.Sleep(time.Second)
	callblc(addr1, addr2)
	broadcastblc(addr1, addr2)
}
