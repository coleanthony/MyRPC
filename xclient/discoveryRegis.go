package xclient

import (
	"log"
	"net/http"
	"strings"
	"time"
)

// 复用MultiServersDiscovery
// registry 即注册中心的地址
// timeout 服务列表的过期时间
// lastUpdate 是代表最后从注册中心更新服务列表的时间，默认 10s 过期，即 10s 之后，需要从注册中心更新新的列表。
type RegistryDiscovery struct {
	*MultiServerDiscovery
	registry   string
	timeout    time.Duration
	lastUpdate time.Time
}

const defaultUpdateTimeout = time.Second * 10

func NewRegistryDiscovery(registryaddr string, timeout time.Duration) *RegistryDiscovery {
	if timeout == 0 {
		timeout = defaultUpdateTimeout
	}
	rd := &RegistryDiscovery{
		MultiServerDiscovery: NewMultiServerDiscovery(make([]string, 0)),
		registry:             registryaddr,
		timeout:              timeout,
	}
	return rd
}

// 实现 Update 和 Refresh 方法
func (rd *RegistryDiscovery) Update(servers []string) error {
	rd.mutex.Lock()
	defer rd.mutex.Unlock()
	rd.servers = servers
	rd.lastUpdate = time.Now()
	return nil
}

func (rd *RegistryDiscovery) Refresh() error {
	rd.mutex.Lock()
	defer rd.mutex.Unlock()
	if rd.lastUpdate.Add(rd.timeout).After(time.Now()) {
		return nil
	}
	log.Println("rpc registry:refresh servers from registry ", rd.registry)
	resp, err := http.Get(rd.registry)
	if err != nil {
		log.Println("rpc registry refresh err:", err)
		return err
	}
	servers := strings.Split(resp.Header.Get("X-rpc-Servers"), ",")
	rd.servers = make([]string, 0, len(servers))
	for _, server := range servers {
		if strings.TrimSpace(server) != "" {
			rd.servers = append(rd.servers, strings.TrimSpace(server))
		}
	}
	rd.lastUpdate = time.Now()
	return nil
}

func (rd *RegistryDiscovery) Get(mode SelectMode) (string, error) {
	if err := rd.Refresh(); err != nil {
		return "", err
	}
	return rd.MultiServerDiscovery.Get(mode)
}

func (rd *RegistryDiscovery) GetAll() ([]string, error) {
	if err := rd.Refresh(); err != nil {
		return nil, err
	}
	return rd.MultiServerDiscovery.GetAll()
}
