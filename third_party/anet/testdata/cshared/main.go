// Android c-shared regression probe: this exported function keeps the Android
// API detector and both interface-enumeration paths reachable by the linker.
package main

import "C"

import "github.com/wlynxg/anet"

//export AnetProbe
func AnetProbe() C.int {
	anet.SetAndroidVersion(0)
	interfaces, err := anet.Interfaces()
	if err != nil {
		return -1
	}
	count := 0
	for i := range interfaces {
		addresses, err := anet.InterfaceAddrsByInterface(&interfaces[i])
		if err != nil {
			return -2
		}
		count += len(addresses)
	}
	return C.int(count)
}

func main() {}
