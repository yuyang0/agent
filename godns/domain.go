package godns

import (
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/Sirupsen/logrus"
	"github.com/yuyang0/agent/util"
)

const EtcdDnsExtraPrefixKey = "extra_domains"
const EtcdVipPrefixKey = "vip"

func (self *Godns) WatchGodnsExtra(watchCh <-chan struct{}) {
	util.WatchConfig(glog, self.lainlet, EtcdDnsExtraPrefixKey, watchCh, func(datas interface{}) {
		var domainAddrs []AddressItem
		ip := self.FetchVip()
		for key, value := range datas.(map[string]interface{}) {
			glog.WithFields(logrus.Fields{
				"key":    key,
				"domain": value,
			}).Debug("Get domain from lainlet")
			var domains []string
			err := json.Unmarshal([]byte(value.(string)), &domains)
			if err != nil {
				glog.WithFields(logrus.Fields{
					"key":    fmt.Sprintf("/lain/config/%s/%s", EtcdDnsExtraPrefixKey, key),
					"reason": err,
				}).Error("Cannot parse domain server config")
				continue
			}
			for _, domain := range domains {
				domainAddrs = append(domainAddrs, AddressItem{
					ip:     ip,
					domain: domain,
				})
			}
		}
		self.mu.Lock()
		defer self.mu.Unlock()
		if reflect.DeepEqual(self.addresses, domainAddrs) {
			return
		}
		self.addresses = domainAddrs
		self.eventCh <- 1
	})
}

func (self *Godns) WatchVip(watchCh <-chan struct{}) {
	util.WatchConfig(glog, self.lainlet, EtcdVipPrefixKey, watchCh, func(datas interface{}) {
		lastIp := self.FetchVip()
		if len(datas.(map[string]interface{})) == 0 {
			self.vip = ""
		} else {
			for key, value := range datas.(map[string]interface{}) {
				glog.WithFields(logrus.Fields{
					"key": key,
					"vip": value,
				}).Debug("Get vip from lainlet")
				self.vip = value.(string)
			}
		}
		ip := self.FetchVip()
		if lastIp == ip {
			return
		}

		self.mu.Lock()
		for i, _ := range self.addresses {
			self.addresses[i].ip = ip
		}
		self.mu.Unlock()

		self.eventCh <- 1
	})
}

func (self *Godns) FetchVip() string {
	ip := self.vip
	if ip == "" || self.vip == "0.0.0.0" {
		ip = self.ip
	}
	return ip
}
