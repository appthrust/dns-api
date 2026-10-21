package dnsprobe

import (
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strings"
)

const (
	ModeCluster   = "Cluster"
	ModeRecursive = "Recursive"
)

// Policy is a provider-independent description of required DNS observations.
// Servers contains resolved canonical IP:port endpoints, never fallback views.
type Policy struct {
	Mode                string   `json:"mode,omitempty"`
	Servers             []string `json:"servers,omitempty"`
	ExpectedNameservers []string `json:"expectedNameservers,omitempty"`
}

// ValuesPreview is bounded diagnostic data, never input to an evidence decision.
type ValuesPreview struct {
	Values    []string `json:"values,omitempty"`
	Total     int32    `json:"total"`
	Truncated bool     `json:"truncated"`
}

var namePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$`)

func ValidName(name string) bool {
	_, ipErr := netip.ParseAddr(name)
	return len(name) > 0 && len(name) <= 253 && namePattern.MatchString(name) && ipErr != nil
}

func ValidateServers(servers []string) error {
	if len(servers) == 0 || len(servers) > 3 {
		return fmt.Errorf("resolver.servers must contain 1 through 3 endpoints")
	}
	for i, server := range servers {
		address, err := netip.ParseAddrPort(server)
		if err != nil || address.Port() == 0 || address.Addr().Zone() != "" || address.String() != server || strings.Contains(server, "%") || slices.Contains(servers[:i], server) {
			return fmt.Errorf("resolver.servers must contain distinct canonical IP addresses and ports from 1 through 65535")
		}
	}
	return nil
}
