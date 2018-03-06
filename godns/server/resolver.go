package server

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
	"bytes"
)

type ResolvError struct {
	qname, net  string
	nameservers []string
}

func (e ResolvError) Error() string {
	errmsg := fmt.Sprintf("%s resolv failed on %s (%s)", e.qname, strings.Join(e.nameservers, "; "), e.net)
	return errmsg
}

type RResp struct {
	msg        *dns.Msg
	nameserver string
	rtt        time.Duration
}

type Resolver struct {
	mu            sync.RWMutex
	// the total upstream server list, contain 2 parts
	// 1. server list in resolv.conf
	// 2. server list from other place eg: server file
	servers       []string
	// server list in resolv.conf
	resolvServers []string
	// upstream server for specified domain
	domainServers *suffixTreeNode
}

func NewResolver() *Resolver {
	r := &Resolver{
		servers:       []string{},
		domainServers: newSuffixTreeRoot(),
	}

	clientConfig, err := dns.ClientConfigFromFile(RESOLV_CONF)
	if err != nil {
		glog.Errorf(":%s is not a valid resolv.conf file\n", RESOLV_CONF)
		glog.Errorf("%s", err)
		panic(err)
	}
	for _, server := range clientConfig.Servers {
		nameserver := net.JoinHostPort(server, clientConfig.Port)
		r.resolvServers = append(r.resolvServers, nameserver)
	}

	return r
}

func (r *Resolver) ParseServerList(buf []byte) {
	var servers []string
	domainServers := newSuffixTreeRoot()
	scanner := bufio.NewScanner(bytes.NewReader(buf))
	for scanner.Scan() {
		line := scanner.Text()
		line = strings.TrimSpace(line)

		if !strings.HasPrefix(line, "server") {
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
			addr := tokens[2]

			// if !isDomain(domain) {
			// 	glog.Warnf("%s is not a domain.", domain)
			// 	continue
			// }
			ip, port, err := parseServerAddr(addr)
			if err != nil {
				glog.Warnf("%s", err.Error())
				continue
			}
			domainServers.sinsert(strings.Split(domain, "."), net.JoinHostPort(ip, port))
		case 1:
			ip, port, err := parseServerAddr(line)
			if err != nil {
				glog.Warnf("%s", err.Error())
				continue
			}
			servers = append(servers, net.JoinHostPort(ip, port))
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	servers = append(servers, r.resolvServers...)
	r.servers = servers
	r.domainServers = domainServers
}

// Lookup will ask each nameserver in top-to-bottom fashion, starting a new request
// in every second, and return as early as possbile (have an answer).
// It returns an error if no request has succeeded.
func (r *Resolver) Lookup(net string, req *dns.Msg) (message *dns.Msg, err error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	c := &dns.Client{
		Net:          net,
		ReadTimeout:  r.Timeout(),
		WriteTimeout: r.Timeout(),
	}

	if net == "udp" && RESOLV_EDNS0_ON {
		req = req.SetEdns0(65535, true)
	}

	qname := req.Question[0].Name

	res := make(chan *RResp, 1)
	var wg sync.WaitGroup
	L := func(nameserver string) {
		defer wg.Done()
		r, rtt, err := c.Exchange(req, nameserver)
		if err != nil {
			glog.Warnf("%s socket error on %s", qname, nameserver)
			glog.Warnf("error:%s", err.Error())
			return
		}
		// If SERVFAIL happen, should return immediately and try another upstream resolver.
		// However, other Error code like NXDOMAIN is an clear response stating
		// that it has been verified no such domain existas and ask other resolvers
		// would make no sense. See more about #20
		if r != nil && r.Rcode != dns.RcodeSuccess {
			glog.Warnf("%s failed to get an valid answer on %s", qname, nameserver)
			if r.Rcode == dns.RcodeServerFailure {
				return
			}
		}
		re := &RResp{r, nameserver, rtt}
		select {
		case res <- re:
		default:
		}
	}

	ticker := time.NewTicker(time.Duration(RESOLV_INTERVAL) * time.Millisecond)
	defer ticker.Stop()
	// Start lookup on each nameserver top-down, in every second
	nameservers := r.nameservers(qname)
	glog.Debugf("qname: %s, nameservers: %v", qname, nameservers)
	for _, nameserver := range nameservers {
		wg.Add(1)
		go L(nameserver)
		// but exit early, if we have an answer
		select {
		case re := <-res:
			glog.Debugf("%s resolv on %s rtt: %v", UnFqdn(qname), re.nameserver, re.rtt)
			return re.msg, nil
		case <-ticker.C:
			continue
		}
	}
	// wait for all the namservers to finish
	wg.Wait()
	select {
	case re := <-res:
		glog.Debugf("%s resolv on %s rtt: %v", UnFqdn(qname), re.nameserver, re.rtt)
		return re.msg, nil
	default:
		return nil, ResolvError{qname, net, nameservers}
	}
}

// Namservers return the array of nameservers, with port number appended.
// '#' in the name is treated as port separator, as with dnsmasq.
func (r *Resolver) nameservers(qname string) []string {
	queryKeys := strings.Split(qname, ".")
	queryKeys = queryKeys[:len(queryKeys)-1] // ignore last '.'

	var ns []string
	if v, found := r.domainServers.search(queryKeys); found {
		glog.Debugf("%s be found in domain server list, upstream: %v", qname, v)
		ns = append(ns, v...)
		//Ensure query the specific upstream nameserver in async Lookup() function.
		return ns
	}

	for _, nameserver := range r.servers {
		ns = append(ns, nameserver)
	}
	return ns
}

func (r *Resolver) Timeout() time.Duration {
	return time.Duration(RESOLV_TIMEOUT) * time.Second
}
