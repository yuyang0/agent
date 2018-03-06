package godns

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Sirupsen/logrus"
	"github.com/docker/libkv/store"
	lainlet "github.com/laincloud/lainlet/client"
	"github.com/yuyang0/agent/util"
	"github.com/yuyang0/agent/godns/server"
)

const EtcdAddressPrefixKey = "dnsmasq_addresses"
const EtcdServerPrefixKey = "dnsmasq_servers"
const EtcdDomainPrefixKey = "domain"
const EtcdPrefixKey = "/lain/config"

var (
	glog *logrus.Logger
)

type AddressItem struct {
	ip     string
	domain string
}

type ServerItem struct {
	ip     string
	port   string
	domain string
}

type Godns struct {
	ip        string
	vip       string
	libkv     store.Store
	srv       *server.Server
	isRunning bool
	stopCh    chan struct{}
	eventCh   chan int
	cnfEvCh   chan int
	addresses []AddressItem
	servers   []ServerItem
	domains   []AddressItem
	staticAddr map[string]string
	lainlet   *lainlet.Client
	extra     bool
}

type JSONAddressConfig struct {
	Ips  []string `json:"ips"`
	Type string   `json:"type"`
}

type JSONServerConfig struct {
	Servers []string `json:"servers"` // ip#port
}

func New(dnsAddr string, ip string, kv store.Store, lainlet *lainlet.Client, log *logrus.Logger, extra bool) *Godns {
	glog = log
	srv := server.New(dnsAddr, log)
	staticAddr := map[string] string {
		"etcd.lain": ip,
		"docker.lain": ip,
		"lainlet.lain": ip,
		"metric.lain": ip,
	}
	// add lain.local
	key := fmt.Sprintf("%s/%s", EtcdPrefixKey, EtcdDomainPrefixKey)
	if pair, err := kv.Get(key); err == nil {
		domain := string(pair.Value[:])
		if domain == "lain.local" {
			key := fmt.Sprintf("%s/%s", EtcdPrefixKey, EtcdVipPrefixKey)
			if pair, err := kv.Get(key); err == nil {
				vip := string(pair.Value[:])
				staticAddr[domain] = vip
			}
		}
	}
	return &Godns{
		srv:       srv,
		ip:        ip,
		libkv:     kv,
		lainlet:   lainlet,
		stopCh:    make(chan struct{}),
		eventCh:   make(chan int),
		cnfEvCh:   make(chan int),
		isRunning: false,
		extra:     extra,
		staticAddr: staticAddr,
	}
}

func (self *Godns) Run() {
	self.isRunning = true
	go self.srv.Run()

	stopAddressCh := make(chan struct{})
	defer close(stopAddressCh)
	stopServerCh := make(chan struct{})
	defer close(stopServerCh)
	stopExtraCh := make(chan struct{})
	defer close(stopExtraCh)
	stopVipCh := make(chan struct{})
	defer close(stopVipCh)
	go self.WatchGodnsAddress(stopAddressCh)
	go self.WatchGodnsServer(stopServerCh)
	if self.extra {
		go self.WatchGodnsExtra(stopExtraCh)
		go self.WatchVip(stopVipCh)
	}
	for {
		select {
		case <-self.eventCh:
			glog.Debug("Received dnsmasq event")
			self.SaveAddresses()
			self.SaveServers()
		case <-self.cnfEvCh:
			glog.Debug("Received dnsmasq configure event")
			self.SaveAddresses()
		case <-self.stopCh:
			self.isRunning = false
			stopAddressCh <- struct{}{}
			stopServerCh <- struct{}{}
			stopExtraCh <- struct{}{}
			stopVipCh <- struct{}{}
			return
		}
	}
}

func (self *Godns) Stop() {
	self.srv.Stop()
	if self.isRunning {
		close(self.stopCh)
	}
}

func (self *Godns) WatchGodnsAddress(watchCh <-chan struct{}) {
	keyPrefixLength := len(EtcdAddressPrefixKey) + 1
	util.WatchConfig(glog, self.lainlet, EtcdAddressPrefixKey, watchCh, func(addrs interface{}) {
		var addresses []AddressItem
		for key, value := range addrs.(map[string]interface{}) {
			domain := key[keyPrefixLength:]
			glog.WithFields(logrus.Fields{
				"domain": domain,
				"value":  value.(string),
			}).Debug("Get domain from lainlet")

			var addr JSONAddressConfig
			err := json.Unmarshal([]byte(value.(string)), &addr)
			if err != nil {
				glog.WithFields(logrus.Fields{
					"key":    fmt.Sprintf("%s/%s/%s", EtcdPrefixKey, EtcdAddressPrefixKey, key),
					"reason": err,
				}).Warn("Cannot parse domain config")
				continue
			}

			glog.WithFields(logrus.Fields{
				"domain":  domain,
				"address": addr,
			}).Debug("Get domain config from lainlet")

			if addr.Type == "node" {
				// ip = host ip
				ip := self.ip
				item := AddressItem{
					ip:     ip,
					domain: domain,
				}
				addresses = append(addresses, item)
			} else {
				// TODO(xutao) validate ip
				for _, ip := range addr.Ips {
					item := AddressItem{
						ip:     ip,
						domain: domain,
					}
					addresses = append(addresses, item)
				}
			}

			self.addresses = addresses
		}
		self.eventCh <- 1
	})
}

func (self *Godns) WatchGodnsServer(watchCh <-chan struct{}) {
	keyPrefixLength := len(EtcdServerPrefixKey) + 1
	util.WatchConfig(glog, self.lainlet, EtcdServerPrefixKey, watchCh, func(addrs interface{}) {
		var servers []ServerItem
		for key, value := range addrs.(map[string]interface{}) {
			domain := key[keyPrefixLength:]
			glog.WithFields(logrus.Fields{
				"domain": domain,
				"value":  value.(string),
			}).Debug("Get domain from lainlet")

			var serv JSONServerConfig
			err := json.Unmarshal([]byte(value.(string)), &serv)
			if err != nil {
				glog.WithFields(logrus.Fields{
					"key":    fmt.Sprintf("/lain/config/%s/%s", EtcdServerPrefixKey, key),
					"reason": err,
				}).Error("Cannot parse domain server config")
				continue
			}
			for _, serverKey := range serv.Servers {
				// TODO(xutao) validate ip
				sharpCount := strings.Count(serverKey, "#")
				if sharpCount == 1 {
					splitKey := strings.SplitN(serverKey, "#", 2)
					ip, port := splitKey[0], splitKey[1]
					item := ServerItem{
						ip:     ip,
						port:   port,
						domain: domain,
					}
					servers = append(servers, item)
				} else {
					glog.WithFields(logrus.Fields{
						"domain": domain,
						"server": serverKey,
					}).Error("Invalid domain server config")
					continue
				}
			}
			glog.WithFields(logrus.Fields{
				"domain": domain,
				"server": serv,
			}).Debug("Get domain config from lainlet")
		}
		self.servers = servers
		self.eventCh <- 1
	})
}

func (self *Godns) SaveAddresses() {
	data := make(map[string]string)
	for domain, ip := range self.staticAddr {
		data[domain] = ip
	}
	for _, addr := range self.addresses {
		data[addr.domain] = addr.ip
	}

	for _, serv := range self.domains {
		data[serv.domain] = serv.ip
	}
	self.srv.ReplaceAddresses(data)
}

func (self *Godns) SaveServers() {
	data := []byte{}
	for _, serv := range self.servers {
		content := fmt.Sprintf("server=/%s/%s#%s\n", serv.domain, serv.ip, serv.port)
		data = append(data, content...)
	}
	self.srv.ReplaceDomainServers(data)
}

func (self *Godns) AddAddress(addressDomain string, addressIps []string, addressType string) {
	kv := self.libkv
	key := fmt.Sprintf("%s/%s/%s", EtcdPrefixKey, EtcdAddressPrefixKey, addressDomain)
	data := JSONAddressConfig{
		Ips:  addressIps,
		Type: addressType,
	}
	value, err := json.Marshal(data)
	if err != nil {
		glog.WithFields(logrus.Fields{
			"key":  key,
			"data": data,
			"err":  err,
		}).Error("Cannot convert address to json")
		return
	}
	// TODO(xutao) retry
	err = kv.Put(key, value, nil)
	if err != nil {
		glog.WithFields(logrus.Fields{
			"key":   key,
			"value": data,
			"err":   err,
		}).Error("Cannot put dnsmasq address")
		return
	}
}

// servers: [1.1.1.1#53, 2.2.2.2#53]
func (self *Godns) AddServer(domain string, servers []string) {
	kv := self.libkv
	key := fmt.Sprintf("%s/%s/%s", EtcdPrefixKey, EtcdServerPrefixKey, domain)
	data := JSONServerConfig{
		Servers: servers,
	}
	value, err := json.Marshal(data)
	if err != nil {
		glog.WithFields(logrus.Fields{
			"key":  key,
			"data": data,
			"err":  err,
		}).Error("Cannot convert server to json")
		return
	}
	// TODO(xutao) retry
	err = kv.Put(key, value, nil)
	if err != nil {
		glog.WithFields(logrus.Fields{
			"key":   key,
			"value": data,
			"err":   err,
		}).Error("Cannot put dnsmasq server")
		return
	}
}
