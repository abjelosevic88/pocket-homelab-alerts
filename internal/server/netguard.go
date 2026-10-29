package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"syscall"
	"time"
)

// ---- sending to what people type in ----
//
// Channels and ntfy servers to follow are addresses a person typed in, and
// the server connects to them. Without a guard any account could reach the
// box's own network that way (Core on localhost, a router page). So, unless
// ALLOW_LOCAL_TARGETS allows it for the person, every outbound connection
// refuses loopback, private, link-local, CGNAT (Tailscale) and multicast
// addresses — checked on the address actually dialled, after DNS and on
// every redirect. By default admins may use local addresses (an ntfy or
// Apprise next to Pocket Alerts), members may not.

var errLocalAddress = errors.New("this address is on a private network, which only the box's admins may use (ALLOW_LOCAL_TARGETS)")

var cgnat = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

func blockedIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() ||
		ip.IsUnspecified() || ip.IsInterfaceLocalMulticast() || cgnat.Contains(ip)
}

// localOK reports whether this person may use private addresses.
func (s *Server) localOK(ctx context.Context, uid int64) bool {
	switch s.cfg.LocalTargets {
	case "all":
		return true
	case "none":
		return false
	}
	u, err := s.store.GetUser(ctx, uid)
	return err == nil && u.Role == "admin"
}

// outClient is an HTTP client for addresses people typed in. timeout 0 =
// none (an ntfy stream stays open); dialling and the response headers are
// still bounded.
func (s *Server) outClient(timeout time.Duration, local bool) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	if !local {
		dialer.Control = func(network, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			if ip := net.ParseIP(host); ip == nil || blockedIP(ip) {
				return errLocalAddress
			}
			return nil
		}
	}
	tr := &http.Transport{
		Proxy:                 nil,
		DialContext:           dialer.DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
		IdleConnTimeout:       60 * time.Second,
		MaxIdleConns:          20,
		ForceAttemptHTTP2:     true,
	}
	return &http.Client{Timeout: timeout, Transport: tr, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 6 {
			return errors.New("too many redirects")
		}
		if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
			return fmt.Errorf("redirect to %s is not allowed", req.URL.Scheme)
		}
		return nil
	}}
}
