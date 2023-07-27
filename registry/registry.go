package registry

import (
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

/*
注册中心的好处在于，客户端和服务端都只需要感知注册中心的存在，而无需感知对方的存在。更具体一些：

1.服务端启动后，向注册中心发送注册消息，注册中心得知该服务已经启动，处于可用状态。一般来说，服务端还需要定期向注册中心发送心跳，证明自己还活着。
2.客户端向注册中心询问，当前哪天服务是可用的，注册中心将可用的服务列表返回客户端。
3.客户端根据注册中心得到的服务列表，选择其中一个发起调用。
如果没有注册中心，客户端需要硬编码服务端的地址，而且没有机制保证服务端是否处于可用状态。当然注册中心的功能还有很多，比如配置的动态同步、通知机制等。比较常用的注册中心有 etcd、zookeeper、consul，一般比较出名的微服务或者 RPC 框架，这些主流的注册中心都是支持的。
*/

// GeeRegistry is a simple register center, provide following functions.
// add a server and receive heartbeat to keep it alive.
// returns all alive servers and delete dead servers sync simultaneously.

type MyRegistry struct {
	timeout time.Duration
	mutex   sync.Mutex
	servers map[string]*ServerItem
}

type ServerItem struct {
	Addr  string
	start time.Time
}

const (
	defaultPath    = "/_myrpc_/registry"
	defaultTimeout = time.Minute * 5
)

func NewRegistry(timeout time.Duration) *MyRegistry {
	r := &MyRegistry{
		timeout: timeout,
		servers: make(map[string]*ServerItem),
	}
	return r
}

var DefaultRegistry = NewRegistry(defaultTimeout)

//为 GeeRegistry 实现添加服务实例和返回服务列表的方法。
//putServer：添加服务实例，如果服务已经存在，则更新 start。
//aliveServers：返回可用的服务列表，如果存在超时的服务，则删除。

func (regis *MyRegistry) putServer(addr string) {
	regis.mutex.Lock()
	defer regis.mutex.Unlock()
	serv := regis.servers[addr]
	if serv == nil {
		regis.servers[addr] = &ServerItem{
			Addr:  addr,
			start: time.Now(),
		}
	} else {
		regis.servers[addr].start = time.Now()
	}
}

func (regis *MyRegistry) aliveServeres() []string {
	regis.mutex.Lock()
	defer regis.mutex.Unlock()
	var res []string
	for addr, svitem := range regis.servers {
		if regis.timeout == 0 || svitem.start.Add(regis.timeout).After(time.Now()) {
			res = append(res, addr)
		} else {
			delete(regis.servers, addr)
		}
	}
	sort.Strings(res)
	return res
}

//采用 HTTP 协议提供服务，且所有的有用信息都承载在 HTTP Header 中。
//Get：返回所有可用的服务列表，通过自定义字段 X-rpc-Servers 承载。
//Post：添加服务实例或发送心跳，通过自定义字段 X-rpc-Server 承载。

func (regis *MyRegistry) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	switch req.Method {
	case "GET":
		w.Header().Set("X-rpc-Servers", strings.Join(regis.aliveServeres(), ","))
	case "POST":
		addr := req.Header.Get("X-rpc-Server")
		if addr == "" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		regis.putServer(addr)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// HandleHTTP registers an HTTP handler for MyRegistry messages on registryPath
func (regis *MyRegistry) HandleHTTP(registrypath string) {
	http.Handle(registrypath, regis)
	log.Println("rpc registry path:", registrypath)
}

func HandleHTTP() {
	DefaultRegistry.HandleHTTP(defaultPath)
}

// 提供 Heartbeat 方法，便于服务启动时定时向注册中心发送心跳，默认周期比注册中心设置的过期时间少 1 min。
// Heartbeat send a heartbeat message every once in a while
// it's a helper function for a server to register or send heartbeat
func Heartbeat(registry, addr string, duration time.Duration) {
	if duration == 0 {
		duration = defaultTimeout - time.Duration(1)*time.Minute
	}
	var err error
	err = sendHeartbeat(registry, addr)
	go func() {
		t := time.NewTicker(duration)
		for err == nil {
			<-t.C
			err = sendHeartbeat(registry, addr)
		}
	}()
}

func sendHeartbeat(registry, addr string) error {
	log.Println(addr, " send heart beat to registry ", registry)
	httpClient := &http.Client{}
	req, _ := http.NewRequest("POST", registry, nil)
	req.Header.Set("X-rpc-Server", addr)
	if _, err := httpClient.Do(req); err != nil {
		log.Println("rpc server:heart beat error:", err)
		return err
	}
	return nil
}
