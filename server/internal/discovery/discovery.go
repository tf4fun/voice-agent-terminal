// Package discovery advertises the server on the local IPv4 network.
package discovery

import (
	"crypto/sha256"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/grandcat/zeroconf"
)

const ServiceType = "_voiceagent._tcp"

type Publisher struct{ server *zeroconf.Server }

type lanInterface struct {
	iface     net.Interface
	addresses []net.IP
}

// Select one up multicast LAN, not a loopback or point-to-point VPN. Publish
// only that interface's IPv4, so SRV/A and the TXT fallback agree. An explicit
// interface must be usable; never silently switch networks after a typo.
func selectLAN(candidates []lanInterface, preferred string) (net.Interface, net.IP, error) {
	for _, candidate := range candidates {
		iface := candidate.iface
		if preferred != "" && iface.Name != preferred {
			continue
		}
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagMulticast == 0 ||
			iface.Flags&(net.FlagLoopback|net.FlagPointToPoint) != 0 {
			continue
		}
		for _, ip := range candidate.addresses {
			if v4 := ip.To4(); v4 != nil && v4.IsGlobalUnicast() && !v4.IsLinkLocalUnicast() {
				return iface, append(net.IP(nil), v4...), nil
			}
		}
	}
	return net.Interface{}, nil, fmt.Errorf("no active multicast IPv4 LAN for mDNS (configured interface %q)", preferred)
}

// The bundled registrar concatenates instance labels without escaping dots.
// macOS hostnames often already end in .local; turn the display name into one
// bounded DNS label, rather than advertising an extra domain as an instance.
func instanceLabel(name string) string {
	name = strings.TrimRight(strings.TrimSpace(name), ".")
	if strings.HasSuffix(strings.ToLower(name), ".local") {
		name = name[:len(name)-6]
	}
	var out strings.Builder
	for _, ch := range name {
		if !unicode.IsLetter(ch) && !unicode.IsNumber(ch) && ch != ' ' && ch != '-' && ch != '_' {
			ch = '-'
		}
		if out.Len()+utf8.RuneLen(ch) > 63 {
			break
		}
		out.WriteRune(ch)
	}
	label := strings.Trim(out.String(), " -_")
	if label == "" {
		return "VoiceAgent"
	}
	return label
}

type announcement struct {
	instance, host string
	text           []string
}

func makeAnnouncement(name string, ip net.IP) announcement {
	instance := instanceLabel(name)
	// Explicit ASCII FQDN avoids Register's macOS .local.local target and is
	// distinct for two hosts using the same display name on different addresses.
	digest := sha256.Sum256([]byte(instance + "\x00" + ip.String()))
	host := fmt.Sprintf("voiceagent-%x.local.", digest[:6])
	return announcement{instance, host, []string{"ver=1", "path=/api/v1", "caps=chat,drafts,pcm16", "ip=" + ip.String()}}
}

// Advertise publishes a single-label DNS-SD instance and an explicit host/IP on
// one LAN. HTTP may still run if discovery fails; callers log that failure.
func Advertise(instance string, port int, preferred string, log *slog.Logger) (*Publisher, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("list mDNS interfaces: %w", err)
	}
	candidates := make([]lanInterface, 0, len(interfaces))
	for _, iface := range interfaces {
		addresses, err := iface.Addrs()
		if err != nil {
			continue
		}
		candidate := lanInterface{iface: iface}
		for _, address := range addresses {
			if ipnet, ok := address.(*net.IPNet); ok {
				candidate.addresses = append(candidate.addresses, ipnet.IP)
			}
		}
		candidates = append(candidates, candidate)
	}
	iface, ip, err := selectLAN(candidates, preferred)
	if err != nil {
		return nil, err
	}
	record := makeAnnouncement(instance, ip)
	// Use a dotless domain argument and a fully qualified host to avoid the
	// bundled registrar appending .local to an already qualified macOS name.
	server, err := zeroconf.RegisterProxy(record.instance, ServiceType, "local", port,
		record.host, []string{ip.String()}, record.text, []net.Interface{iface})
	if err != nil {
		return nil, fmt.Errorf("advertise %s: %w", ServiceType, err)
	}
	if log != nil {
		log.Info("advertising", "service", ServiceType, "instance", record.instance,
			"port", port, "interface", iface.Name, "ip", ip.String(), "host", record.host)
	}
	return &Publisher{server: server}, nil
}

func (p *Publisher) Close() {
	if p == nil || p.server == nil {
		return
	}
	p.server.Shutdown()
	p.server = nil
}
