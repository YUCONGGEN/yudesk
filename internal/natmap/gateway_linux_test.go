//go:build linux

package natmap

import (
	"strings"
	"testing"
)

func TestParseLinuxRoutesChoosesLowestMetricDefault(t *testing.T) {
	routes := `Iface Destination Gateway Flags RefCnt Use Metric Mask MTU Window IRTT
eth0 00000000 0101A8C0 0003 0 0 100 00000000 0 0 0
wlan0 00000000 0100000A 0003 0 0 20 00000000 0 0 0
eth0 0001A8C0 00000000 0001 0 0 0 00FFFFFF 0 0 0
`
	gateway, err := parseLinuxRoutes(strings.NewReader(routes))
	if err != nil {
		t.Fatal(err)
	}
	if gateway.String() != "10.0.0.1" {
		t.Fatalf("gateway = %s", gateway)
	}
}
