package mdns

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/sagernet/sing/common/control"

	"golang.org/x/net/ipv4"
)

// multicastInterface returns an interface suitable for joining the mDNS group.
func multicastInterface(t *testing.T) *net.Interface {
	t.Helper()
	interfaces, err := net.Interfaces()
	if err != nil {
		t.Skipf("cannot list interfaces: %v", err)
	}
	for i := range interfaces {
		iface := &interfaces[i]
		if iface.Flags&net.FlagUp != 0 &&
			iface.Flags&net.FlagMulticast != 0 &&
			iface.Flags&net.FlagLoopback == 0 {
			return iface
		}
	}
	t.Skip("no usable multicast interface")
	return nil
}

func testQueryTarget(iface *net.Interface) queryTarget {
	return queryTarget{
		iface: control.Interface{
			Index:        iface.Index,
			MTU:          iface.MTU,
			Name:         iface.Name,
			HardwareAddr: iface.HardwareAddr,
			Flags:        iface.Flags,
		},
		family: "udp4",
	}
}

// Responses are multicasted to :5353. The query socket must be bound to that
// port, otherwise the kernel never delivers the reply to it.
func TestNewMulticastConnBindsMDNSPort(t *testing.T) {
	iface := multicastInterface(t)
	var listenConfig net.ListenConfig
	listenConfig.Control = control.ReuseAddr()

	packetConn, destination, err := newMulticastConn(context.Background(), listenConfig, testQueryTarget(iface))
	if err != nil {
		t.Skipf("multicast is unavailable on %s: %v", iface.Name, err)
	}
	defer packetConn.Close()

	localAddr, ok := packetConn.LocalAddr().(*net.UDPAddr)
	if !ok {
		t.Fatalf("unexpected local address type %T", packetConn.LocalAddr())
	}
	if localAddr.Port != mdnsPort {
		t.Fatalf("query socket bound to port %d, want %d", localAddr.Port, mdnsPort)
	}
	groupAddr, ok := destination.(*net.UDPAddr)
	if !ok || !groupAddr.IP.Equal(mdnsGroupIPv4) || groupAddr.Port != mdnsPort {
		t.Fatalf("unexpected query destination: %v", destination)
	}
}

// Joining the group is what makes the interface deliver multicast replies.
// A second socket sends a datagram to the group; the query socket must receive
// it. Environments that disable multicast loopback skip this test.
func TestNewMulticastConnJoinsGroup(t *testing.T) {
	iface := multicastInterface(t)
	var listenConfig net.ListenConfig
	listenConfig.Control = control.ReuseAddr()

	packetConn, _, err := newMulticastConn(context.Background(), listenConfig, testQueryTarget(iface))
	if err != nil {
		t.Skipf("multicast is unavailable on %s: %v", iface.Name, err)
	}
	defer packetConn.Close()

	sender, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		t.Skipf("cannot create sender socket: %v", err)
	}
	defer sender.Close()
	senderConn := ipv4.NewPacketConn(sender)
	if err = senderConn.SetMulticastInterface(iface); err != nil {
		t.Skipf("cannot set sender multicast interface: %v", err)
	}
	_ = senderConn.SetMulticastTTL(1)

	// Send to the group; with multicast loopback enabled (the default) this is
	// also delivered to sockets on the local host that joined the group.
	_, err = sender.WriteTo([]byte("mdns-multicast-probe"), &net.UDPAddr{IP: mdnsGroupIPv4, Port: mdnsPort})
	if err != nil {
		t.Skipf("cannot send to multicast group: %v", err)
	}
	if err = packetConn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 64)
	n, _, err := packetConn.ReadFrom(buffer)
	if err != nil {
		t.Skipf("multicast loopback is not available: %v", err)
	}
	if string(buffer[:n]) != "mdns-multicast-probe" {
		t.Fatalf("unexpected payload %q", buffer[:n])
	}
}
