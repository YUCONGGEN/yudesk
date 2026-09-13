package relay

import (
	"encoding/json"
	"testing"
	"time"
)

func TestPortMapControlMessageRoundTrip(t *testing.T) {
	want := ControlMessage{
		Type:           "status",
		PortMapServer:  "www.yucg.cn:8232",
		PortMapSession: "session",
		PortMaps: []PortMap{{
			ID: "map-id", Name: "本地服务", Protocol: "tcp", LocalPort: 8080,
			RemotePort: 9123, PublicAddress: "www.yucg.cn:9123", Secret: "secret",
			Online: true, CreatedAt: time.Unix(123, 0).UTC(),
		}},
		PortMapResult: &PortMapResult{RequestID: "request", OK: true},
	}
	b, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got ControlMessage
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.PortMapServer != want.PortMapServer || len(got.PortMaps) != 1 || got.PortMaps[0].RemotePort != 9123 || got.PortMapResult == nil || !got.PortMapResult.OK {
		t.Fatalf("unexpected round trip: %+v", got)
	}
}
