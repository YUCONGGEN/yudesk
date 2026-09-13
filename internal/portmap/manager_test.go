package portmap

import (
	"testing"

	"github.com/yudesk/yudesk/internal/relay"
)

func TestConfigurationSignatureIsStableAcrossServerOrdering(t *testing.T) {
	a := Configuration{DeviceID: "abc", Server: "example.com:8232", Session: "s", Mappings: []relay.PortMap{{ID: "b", RemotePort: 9200}, {ID: "a", RemotePort: 9100}}}
	b := Configuration{DeviceID: "ABC", Server: "example.com:8232", Session: "s", Mappings: []relay.PortMap{{ID: "a", RemotePort: 9100}, {ID: "b", RemotePort: 9200}}}
	normalize(&a)
	normalize(&b)
	if configSignature(a) != configSignature(b) {
		t.Fatal("equivalent configurations must not restart FRP")
	}
}

func TestConfigurationSignatureIgnoresDisplayAndOnlineState(t *testing.T) {
	a := Configuration{DeviceID: "ABC", Server: "example.com:8232", Session: "s", Certificate: "cert", Mappings: []relay.PortMap{{ID: "a", Protocol: "tcp", LocalPort: 8080, RemotePort: 9100, Secret: "secret", Name: "网站", PublicAddress: "example.com:9100"}}}
	b := a
	b.Mappings = append([]relay.PortMap(nil), a.Mappings...)
	b.Mappings[0].Online = true
	b.Mappings[0].Name = "新备注"
	if configSignature(a) != configSignature(b) {
		t.Fatal("server online/display updates must not restart a working FRP tunnel")
	}
}

func TestManagerRejectsInvalidMappingBeforeDial(t *testing.T) {
	m := New(t.TempDir())
	err := m.run(t.Context(), Configuration{
		DeviceID: "ABC", Server: "example.com:8232", Session: "session", Certificate: "certificate",
		Mappings: []relay.PortMap{{ID: "id", Protocol: "udp", LocalPort: 80, RemotePort: 9000, Secret: "secret"}},
	})
	if err == nil {
		t.Fatal("invalid protocol was accepted")
	}
}
