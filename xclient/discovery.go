package xclient

import (
	"errors"
	"math"
	"math/rand"
	"sync"
	"time"
)

//定义 2 个类型：
//SelectMode 代表不同的负载均衡策略，简单起见，GeeRPC 仅实现 Random 和 RoundRobin 两种策略。
//Discovery 是一个接口类型，包含了服务发现所需要的最基本的接口。
//Refresh() 从注册中心更新服务列表
//Update(servers []string) 手动更新服务列表
//Get(mode SelectMode) 根据负载均衡策略，选择一个服务实例
//GetAll() 返回所有的服务实例

type SelectMode int

const (
	RandomSelect SelectMode = iota
	RoundRobinSelect
)

type Discovery interface {
	Refresh() error
	Update(servers []string) error
	Get(mode SelectMode) (string, error)
	GetAll() ([]string, error)
}

// 实现一个不需要注册中心，服务列表由手工维护的服务发现的结构体：MultiServersDiscovery
// r 是一个产生随机数的实例，初始化时使用时间戳设定随机数种子，避免每次产生相同的随机数序列。
// index 记录 Round Robin 算法已经轮询到的位置，为了避免每次从 0 开始，初始化时随机设定一个值。
type MultiServerDiscovery struct {
	r       *rand.Rand
	mutex   sync.RWMutex
	servers []string
	index   int
}

func NewMultiServerDiscovery(servers []string) *MultiServerDiscovery {
	rs := &MultiServerDiscovery{
		r:       rand.New(rand.NewSource(time.Now().UnixNano())),
		servers: servers,
	}
	rs.index = rs.r.Intn(math.MaxInt32 - 1)
	return rs
}

var _ Discovery = (*MultiServerDiscovery)(nil)

func (d *MultiServerDiscovery) Refresh() error {
	return nil
}

// Update the servers of discovery dynamically if needed
func (d *MultiServerDiscovery) Update(servers []string) error {
	d.mutex.Lock()
	defer d.mutex.Unlock()
	d.servers = servers
	return nil
}

// Get a server according to mode
func (d *MultiServerDiscovery) Get(mode SelectMode) (string, error) {
	d.mutex.Lock()
	defer d.mutex.Unlock()
	n := len(d.servers)
	if n == 0 {
		return "", errors.New("rpc discovery:no available servers")
	}
	switch mode {
	case RandomSelect:
		return d.servers[d.r.Intn(n)], nil
	case RoundRobinSelect:
		server := d.servers[d.index%n]
		d.index++
		d.index = d.index % n
		return server, nil
	default:
		return "", errors.New("rpc discovery:mode not expected")
	}
}

func (d *MultiServerDiscovery) GetAll() ([]string, error) {
	d.mutex.RLock()
	defer d.mutex.RUnlock()
	servers := make([]string, len(d.servers), len(d.servers))
	copy(servers, d.servers)
	return servers, nil
}
