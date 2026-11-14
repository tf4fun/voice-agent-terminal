package discovery

import (
	"github.com/grandcat/zeroconf"
	"github.com/miekg/dns"
	"net"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestAnnouncementHasSingleInstanceLabelAndExplicitHost(t *testing.T) {
	for _, name := range []string{"Test-Mac.local", "desk.office.LOCAL.", "Voice Memo", "语音日记.local", strings.Repeat("语", 40), "a\\b.c", "."} {
		record := makeAnnouncement(name, net.ParseIP("192.0.2.20"))
		entry := zeroconf.NewServiceEntry(record.instance, ServiceType, "local")
		if parts := dns.SplitDomainName(entry.ServiceInstanceName()); len(parts) != 4 || parts[1] != "_voiceagent" {
			t.Fatalf("instance label shape: %q", entry.ServiceInstanceName())
		}
		if !utf8.ValidString(record.instance) || len(record.instance) > 63 {
			t.Fatalf("invalid instance %q", record.instance)
		}
		if !dns.IsFqdn(record.host) || len(dns.SplitDomainName(record.host)) != 2 || strings.Contains(record.host, ".local.local") {
			t.Fatalf("bad host: %s", record.host)
		}
		msg := &dns.Msg{Answer: []dns.RR{
			&dns.PTR{Hdr: dns.RR_Header{Name: entry.ServiceName(), Rrtype: dns.TypePTR, Class: dns.ClassINET, Ttl: 120}, Ptr: entry.ServiceInstanceName()},
			&dns.SRV{Hdr: dns.RR_Header{Name: entry.ServiceInstanceName(), Rrtype: dns.TypeSRV, Class: dns.ClassINET, Ttl: 120}, Target: record.host, Port: 8080},
		}}
		packed, err := msg.Pack()
		if err != nil {
			t.Fatal(err)
		}
		var decoded dns.Msg
		if err = decoded.Unpack(packed); err != nil {
			t.Fatal(err)
		}
		if record.text[len(record.text)-1] != "ip=192.0.2.20" {
			t.Fatal(record.text)
		}
	}
	if instanceLabel("Test-Mac.local") != "Test-Mac" {
		t.Fatal("macOS suffix remained")
	}
	if instanceLabel("desk.office.LOCAL.") != "desk-office" {
		t.Fatal("domain labels remained")
	}
	if makeAnnouncement("same", net.ParseIP("192.0.2.1")).host == makeAnnouncement("same", net.ParseIP("192.0.2.2")).host {
		t.Fatal("host collision")
	}
}

func TestSelectLANExcludesVPNAndUnusableInterfaces(t *testing.T) {
	up := net.FlagUp | net.FlagMulticast | net.FlagBroadcast
	candidate := func(name, address string, flags net.Flags) lanInterface {
		return lanInterface{iface: net.Interface{Name: name, Flags: flags}, addresses: []net.IP{net.ParseIP(address)}}
	}
	candidates := []lanInterface{
		candidate("lo0", "127.0.0.1", up|net.FlagLoopback),
		candidate("utun4", "198.18.0.1", up|net.FlagPointToPoint),
		candidate("en1", "169.254.1.1", up),
		candidate("down", "192.0.2.1", net.FlagMulticast),
		candidate("no-multicast", "192.0.2.2", net.FlagUp),
		candidate("ipv6", "2001:db8::1", up),
		candidate("en0", "192.0.2.20", up),
		candidate("en5", "198.51.100.10", up),
	}
	iface, ip, err := selectLAN(candidates, "")
	if err != nil || iface.Name != "en0" || ip.String() != "192.0.2.20" {
		t.Fatalf("selection: %v %v %v", iface, ip, err)
	}
	iface, ip, err = selectLAN(candidates, "en5")
	if err != nil || iface.Name != "en5" || ip.String() != "198.51.100.10" {
		t.Fatalf("override: %v %v %v", iface, ip, err)
	}
	for _, name := range []string{"missing", "utun4", "lo0", "down", "ipv6", "no-multicast", "en1"} {
		if _, _, err := selectLAN(candidates, name); err == nil {
			t.Fatalf("unsafe override accepted: %s", name)
		}
	}
	if _, _, err := selectLAN(candidates[:6], ""); err == nil {
		t.Fatal("no LAN silently accepted")
	}
}
