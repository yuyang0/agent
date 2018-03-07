package apiserver

import (
	"github.com/go-martini/martini"
	"github.com/yuyang0/agent/godns"
)

type Server struct {
	addr string
	godns *godns.Godns
	m *martini.ClassicMartini
}

func New(addr string, godns *godns.Godns) *Server {
	return &Server {
		addr:addr,
		godns:godns,
		m: martini.Classic(),
	}
}

func (srv *Server) Run() {
	m := srv.m
	m.Get("/v1/dns/config", srv.handleDnsConfig)
	m.Get("/v1/dns/hosts", handleDnsHosts)
	m.Get("/v1/dns/addresses", handleDnsAddresses)
	m.Get("/v1/dns/servers", handleDnsServers)
	m.RunOnAddr(srv.addr)
}

func (srv *Server) handleDnsConfig() string {
	return srv.godns.DumpConfig()
}

func handleDnsHosts() string {
	return ""
}

func handleDnsAddresses() string {
	return ""
}

func handleDnsServers() string {
	return ""
}
