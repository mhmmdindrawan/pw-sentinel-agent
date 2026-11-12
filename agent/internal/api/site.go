package api

import (
	"net"
	"strings"
	"sync"
	"time"
)

// SiteInfo tells support where they are looking: which location this agent
// serves and how to reach it.
type SiteInfo struct {
	Code          string `json:"code,omitempty"`
	Name          string `json:"name,omitempty"`
	EnrollEnabled bool   `json:"enroll_enabled"`
}

// AgentInfo is the agent's own health as shown on the dashboard: version,
// addresses, and whether its database is near the configured limits.
type AgentInfo struct {
	Version       string `json:"version"`
	ListenAddr    string `json:"listen_addr"`
	TailscaleIP   string `json:"tailscale_ip,omitempty"`
	DBSizeBytes   int64  `json:"db_size_bytes"`
	DBMaxBytes    int64  `json:"db_max_bytes"`
	DiskFreeBytes int64  `json:"disk_free_bytes,omitempty"`
	RetentionDays int    `json:"retention_days"`
}

func (s *Server) siteInfo() SiteInfo {
	return SiteInfo{Code: s.cfg.Site.Code, Name: s.cfg.Site.Name, EnrollEnabled: s.cfg.Site.EnrollEnabled()}
}

func (s *Server) agentInfo() AgentInfo {
	size, free, _ := s.db.Usage()
	return AgentInfo{
		Version:       s.Version,
		ListenAddr:    s.cfg.Server.ListenAddr,
		TailscaleIP:   tailscaleIP(),
		DBSizeBytes:   size,
		DBMaxBytes:    int64(s.cfg.Retention.MaxDBMB) << 20,
		DiskFreeBytes: free,
		RetentionDays: s.cfg.Retention.Days,
	}
}

var (
	tsMu   sync.Mutex
	tsAt   time.Time
	tsAddr string
)

// tailscaleIP returns this host's IPv4 in the Tailscale range (100.64/10),
// which is what support types into a browser. Cached for a minute: the
// fleet view calls it on every refresh.
func tailscaleIP() string {
	tsMu.Lock()
	defer tsMu.Unlock()
	if time.Since(tsAt) < time.Minute {
		return tsAddr
	}
	tsAt, tsAddr = time.Now(), ""
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	_, cgnat, _ := net.ParseCIDR("100.64.0.0/10")
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		if ip4 := ipn.IP.To4(); ip4 != nil && cgnat.Contains(ip4) {
			tsAddr = ip4.String()
			break
		}
	}
	return tsAddr
}

// SiteLabel is "CODE · Name" or just the code.
func (i SiteInfo) SiteLabel() string {
	if i.Name == "" {
		return i.Code
	}
	return strings.TrimSpace(i.Code + " · " + i.Name)
}
