package server

import (
	"net"
	"net/http"
	"strconv"

	"github.com/greatbody/portly/internal/config"
)

// validateTarget returns an error if the host:port is not allowed by policy.
func validateTarget(cfg *config.Config, host string, port int) error {
	if port <= 0 || port > 65535 {
		return errBadTarget("invalid port")
	}
	for _, bp := range cfg.Security.BlockedPorts {
		if bp == port {
			return errBadTarget("port " + strconv.Itoa(port) + " is blocked")
		}
	}
	if cfg.Security.AllowAnyDestination {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil {
		// resolve once
		ips, err := net.LookupIP(host)
		if err != nil || len(ips) == 0 {
			return errBadTarget("cannot resolve host: " + host)
		}
		ip = ips[0]
	}
	for _, n := range cfg.AllowedNetworks() {
		if n.Contains(ip) {
			return nil
		}
	}
	return errBadTarget("host " + ip.String() + " is not in allowed networks; add it to security.allowed_cidrs to permit")
}

type httpError struct {
	code int
	msg  string
}

func (e *httpError) Error() string { return e.msg }
func (e *httpError) Code() int     { return e.code }

func errBadTarget(msg string) error { return &httpError{code: http.StatusBadRequest, msg: msg} }
