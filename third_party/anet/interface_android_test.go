package anet

import (
	"net"
	"strconv"
	"testing"
)

// This can also run the exact Android enumeration sources on a Linux kernel
// via the explicit-file go test command in README.yudesk.md. It does not claim
// to simulate Android SELinux permissions or replace a device smoke test.
func TestAndroid11Enumeration(t *testing.T) {
	SetAndroidVersion(11)
	defer SetAndroidVersion(0)
	if androidApiLevel() != android11ApiLevel {
		t.Fatal("Android 11 path not selected")
	}
	interfaces, err := Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	if len(interfaces) == 0 {
		t.Fatal("no addressed interfaces returned")
	}
	all, err := InterfaceAddrs()
	if err != nil || len(all) == 0 {
		t.Fatalf("unicast enumeration: %v", err)
	}
	for _, ifi := range interfaces {
		if ifi.Index <= 0 || ifi.Name == "" || ifi.MTU <= 0 {
			t.Fatalf("incomplete ioctl metadata: %+v", ifi)
		}
		byIndex, err := InterfaceByIndex(ifi.Index)
		if err != nil || byIndex.Name != ifi.Name {
			t.Fatalf("lookup by index: %+v %v", byIndex, err)
		}
		byName, err := InterfaceByName(ifi.Name)
		if err != nil || byName.Index != ifi.Index {
			t.Fatalf("lookup by name: %+v %v", byName, err)
		}
		addrs, err := InterfaceAddrsByInterface(&ifi)
		if err != nil || len(addrs) == 0 {
			t.Fatalf("addresses for %s: %v", ifi.Name, err)
		}
		for _, addr := range addrs {
			switch addr := addr.(type) {
			case *net.IPNet:
				if addr.IP.To4() == nil && addr.IP.IsLinkLocalUnicast() {
					t.Fatal("link-local IPv6 lost its zone")
				}
			case *net.IPAddr:
				if addr.Zone != strconv.Itoa(ifi.Index) {
					t.Fatalf("wrong zone: %v", addr)
				}
			default:
				t.Fatalf("unexpected address type %T", addr)
			}
		}
	}
}

func TestAndroidInvalidInterface(t *testing.T) {
	SetAndroidVersion(11)
	defer SetAndroidVersion(0)
	if _, err := InterfaceAddrsByInterface(nil); err == nil {
		t.Fatal("nil interface accepted")
	}
	if _, err := InterfaceByIndex(0); err == nil {
		t.Fatal("invalid index accepted")
	}
	if _, err := InterfaceByName(""); err == nil {
		t.Fatal("empty name accepted")
	}
}
