package geoip_test

import (
	"net"
	"os"
	"testing"

	"github.com/komari-monitor/komari/utils/geoip"
)

// 测试GeoIP数据库的初始化和更新功能
func TestMmdb(t *testing.T) {
	requireNetworkIntegration(t)
	geoip.CurrentProvider, _ = geoip.NewMaxMindGeoIPService()
	testIpAddr(t)
}
func TestIpApi(t *testing.T) {
	requireNetworkIntegration(t)
	geoip.CurrentProvider, _ = geoip.NewIPAPIService()
	testIpAddr(t)
}

func TestGeojs(t *testing.T) {
	requireNetworkIntegration(t)
	geoip.CurrentProvider, _ = geoip.NewGeoJSService()
	testIpAddr(t)
}

func TestIpInfo(t *testing.T) {
	requireNetworkIntegration(t)
	geoip.CurrentProvider, _ = geoip.NewIPInfoService()
	testIpAddr(t)
}

// 外部 API 与大体积数据库下载属于集成检查，不应阻塞离线发布测试。
func requireNetworkIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("KOMARI_NETWORK_TESTS") != "1" {
		t.Skip("set KOMARI_NETWORK_TESTS=1 to run external GeoIP integration checks")
	}
}
func testIpAddr(t *testing.T) {
	// IPv4
	ipaddr := "8.8.8.8"
	ip := net.ParseIP(ipaddr)
	record, err := geoip.GetGeoInfo(ip)
	if err != nil {
		t.Errorf("Failed to get GeoIP info for IP %s: %v", ipaddr, err)
	}

	if record != nil {
		if record.ISOCode == "" && record.Name == "" {
			t.Errorf("Country information is missing for IP %s", ipaddr)
		}
	} else {
		t.Errorf("GeoIP record is nil for IP %s", ipaddr)
	}

	t.Logf("IPv4:[%s]%s - %s", ipaddr, record.ISOCode, record.Name)

	// IPv6
	ipaddr = "2001:4860:4860::8888"
	ip = net.ParseIP(ipaddr)
	record, err = geoip.GetGeoInfo(ip)
	if err != nil {
		t.Errorf("Failed to get GeoIP info for IPv6 %s: %v", ipaddr, err)
	}
	if record != nil {
		if record.ISOCode == "" && record.Name == "" {
			t.Errorf("Country information is missing for IPv6 %s", ipaddr)
		}
	} else {
		t.Errorf("GeoIP record is nil for IPv6 %s", ipaddr)
	}
	t.Logf("IPv6:[%s]%s - %s", ipaddr, record.ISOCode, record.Name)
}

func TestUnicodeEmoji(t *testing.T) {
	ISOCode := "CN"
	emoji := geoip.GetRegionUnicodeEmoji(ISOCode)
	if emoji != "🇨🇳" {
		t.Errorf("Expected emoji for %s, got %s", ISOCode, emoji)
	}
	t.Logf("Emoji for %s: %s", ISOCode, emoji)
}
