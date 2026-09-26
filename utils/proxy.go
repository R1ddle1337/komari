package utils

import (
	"fmt"
	"net"
	"net/netip"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
)

// Forwarded identity and scheme headers are meaningful only from a configured
// reverse proxy. Native loopback proxies work by default; container gateways
// must be listed explicitly in KOMARI_TRUSTED_PROXIES.
func trustedProxyRanges() ([]string, error) {
	raw, configured := os.LookupEnv("KOMARI_TRUSTED_PROXIES")
	if !configured {
		return []string{"127.0.0.1/32", "::1/128"}, nil
	}
	var ranges []string
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if address, err := netip.ParseAddr(item); err == nil {
			address = address.Unmap()
			ranges = append(ranges, netip.PrefixFrom(address, address.BitLen()).String())
		} else if prefix, err := netip.ParsePrefix(item); err == nil && prefix.Bits() > 0 {
			ranges = append(ranges, prefix.Masked().String())
		} else {
			return nil, fmt.Errorf("invalid trusted proxy address or network: %q", item)
		}
	}
	return ranges, nil
}

func ConfigureTrustedProxies(engine *gin.Engine) error {
	ranges, err := trustedProxyRanges()
	if err != nil {
		return err
	}
	return engine.SetTrustedProxies(ranges)
}

func isTrustedProxy(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		return false
	}
	address, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	ranges, err := trustedProxyRanges()
	if err != nil {
		return false
	}
	for _, raw := range ranges {
		prefix, _ := netip.ParsePrefix(raw)
		if prefix.Contains(address.Unmap()) {
			return true
		}
	}
	return false
}
