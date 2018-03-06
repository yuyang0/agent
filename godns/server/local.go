package server

import (
	"net"
	"strings"
    "bufio"
	"sync"
	"golang.org/x/net/publicsuffix"
	"bytes"
)

type LocalData struct {
	hosts map[string]string
	mu    sync.RWMutex
}

func NewLocalData() *LocalData{
	d := &LocalData{
		hosts: make(map[string]string),
	}
	return d
}

func (f *LocalData) Get(domain string) ([]string, bool) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	domain = strings.ToLower(domain)
	ip, ok := f.hosts[domain]
	if ok {
		return []string{ip}, true
	}

	sld, err := publicsuffix.EffectiveTLDPlusOne(domain)
	if err != nil {
		return nil, false
	}

	for host, ip := range f.hosts {
		if strings.HasPrefix(host, "*.") || strings.HasPrefix(host, ".") {
			old, err := publicsuffix.EffectiveTLDPlusOne(host)
			if err != nil {
				continue
			}
			if sld == old {
				return []string{ip}, true
			}
		}

		old, err := publicsuffix.EffectiveTLDPlusOne(host)
		if err != nil {
			continue
		}
		if sld == old {
			return []string{ip}, true
		}
	}

	return nil, false
}

func (d *LocalData) Set(domain, ip string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.hosts[domain] = ip
}

func (d *LocalData) ReplaceWithBytes(buf []byte) {
	hosts := parseAddressList(buf)
	d.Replace(hosts)
}

func (d *LocalData) Replace(hosts map[string]string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.hosts = hosts
}

func (h *LocalData) GetIpList(domain string, family int) ([]net.IP, bool) {
	var sips []string
	var ip net.IP
	var ips []net.IP

	sips, _ = h.Get(domain)

	if sips == nil {
		return nil, false
	}

	for _, sip := range sips {
		switch family {
		case _IP4Query:
			ip = net.ParseIP(sip).To4()
		case _IP6Query:
			ip = net.ParseIP(sip).To16()
		default:
			continue
		}
		if ip != nil {
			ips = append(ips, ip)
		}
	}

	return ips, ips != nil
}

func parseAddressList(buf []byte) map[string]string {
	hosts := make(map[string]string)
	scanner := bufio.NewScanner(bytes.NewReader(buf))
	for scanner.Scan() {
		line := scanner.Text()
		line = strings.TrimSpace(line)

		if !strings.HasPrefix(line, "address") {
			continue
		}

		sli := strings.Split(line, "=")
		if len(sli) != 2 {
			continue
		}

		line = strings.TrimSpace(sli[1])

		tokens := strings.Split(line, "/")
		switch len(tokens) {
		case 3:
			domain := tokens[1]
			ip := tokens[2]

			if !isDomain(domain) || !isIP(ip) {
				continue
			}
			hosts[domain] = ip
		}
	}
	return hosts
}

func parseHosts(buf []byte) map[string]string {
	hosts := make(map[string]string)
	scanner := bufio.NewScanner(bytes.NewReader(buf))
	for scanner.Scan() {

		line := scanner.Text()
		line = strings.TrimSpace(line)
		line = strings.Replace(line, "\t", " ", -1)

		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}

		sli := strings.Split(line, " ")

		if len(sli) < 2 {
			continue
		}

		ip := sli[0]
		if !isIP(ip) {
			continue
		}

		// Would have multiple columns of domain in line.
		// Such as "127.0.0.1  localhost localhost.domain" on linux.
		// The domains may not strict standard, like "local" so don't check with f.isDomain(domain).
		for i := 1; i <= len(sli)-1; i++ {
			domain := strings.TrimSpace(sli[i])
			if domain == "" {
				continue
			}

			hosts[strings.ToLower(domain)] = ip
		}
	}
	return hosts
}
