package natmap

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func soapEnvelope(body string) string {
	return `<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body>` + body + `</s:Body></s:Envelope>`
}

func TestSSDPAndDescriptionParsing(t *testing.T) {
	packet := []byte("HTTP/1.1 200 OK\r\nCACHE-CONTROL: max-age=120\r\nLOCATION: http://192.168.1.1:1900/root.xml\r\nST: upnp:rootdevice\r\n\r\n")
	location, err := parseSSDPResponse(packet)
	if err != nil || location != "http://192.168.1.1:1900/root.xml" {
		t.Fatalf("location=%q err=%v", location, err)
	}
	for _, invalid := range [][]byte{
		[]byte("NOT HTTP\r\n\r\n"),
		[]byte("HTTP/1.1 200 OK\r\nLOCATION: file:///tmp/router\r\n\r\n"),
		[]byte("HTTP/1.1 500 Error\r\nLOCATION: http://192.0.2.1/\r\n\r\n"),
		[]byte("HTTP/1.1 200 OK\r\nLOCATION: http://203.0.113.9/router.xml\r\n\r\n"),
	} {
		if _, err := parseSSDPResponse(invalid); err == nil {
			t.Fatalf("invalid SSDP response accepted: %q", invalid)
		}
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<?xml version="1.0"?><root><device><deviceList><device><serviceList><service><serviceType>urn:schemas-upnp-org:service:WANIPConnection:2</serviceType><controlURL>/ctl/ip</controlURL></service></serviceList></device></deviceList></device></root>`)
	}))
	defer server.Close()
	services, err := fetchUPnPServices(context.Background(), server.Client(), server.URL+"/root.xml")
	if err != nil {
		t.Fatal(err)
	}
	if len(services) != 1 || services[0].controlURL.String() != server.URL+"/ctl/ip" {
		t.Fatalf("unexpected services: %+v", services)
	}
}

func TestUPnPMappingAndIdempotentDelete(t *testing.T) {
	var adds, deletes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		action := r.Header.Get("SOAPAction")
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "<NewProtocol>UDP</NewProtocol>") && strings.Contains(action, "PortMapping") {
			t.Errorf("mapping is not UDP: %s", body)
		}
		switch {
		case strings.Contains(action, "GetExternalIPAddress"):
			fmt.Fprint(w, soapEnvelope(`<u:GetExternalIPAddressResponse xmlns:u="urn:schemas-upnp-org:service:WANIPConnection:2"><NewExternalIPAddress>203.0.113.77</NewExternalIPAddress></u:GetExternalIPAddressResponse>`))
		case strings.Contains(action, "AddAnyPortMapping"):
			adds.Add(1)
			fmt.Fprint(w, soapEnvelope(`<u:AddAnyPortMappingResponse xmlns:u="urn:schemas-upnp-org:service:WANIPConnection:2"><NewReservedPort>45678</NewReservedPort></u:AddAnyPortMappingResponse>`))
		case strings.Contains(action, "DeletePortMapping"):
			deletes.Add(1)
			fmt.Fprint(w, soapEnvelope(`<u:DeletePortMappingResponse xmlns:u="urn:schemas-upnp-org:service:WANIPConnection:2"/>`))
		default:
			http.Error(w, "unexpected action", 500)
		}
	}))
	defer server.Close()
	controlURL := server.URL
	parsedURL, _ := url.Parse(controlURL)
	service := upnpService{serviceType: "urn:schemas-upnp-org:service:WANIPConnection:2", controlURL: parsedURL, client: server.Client()}
	mapping, err := service.addUDPMapping(context.Background(), netip.MustParseAddr("192.0.2.40"), mapConfig{localPort: 32000, lifetime: time.Hour, description: "YuDesk & test"})
	if err != nil {
		t.Fatal(err)
	}
	if mapping.Method != MethodUPnP || mapping.ExternalIP.String() != "203.0.113.77" || mapping.ExternalPort != 45678 {
		t.Fatalf("unexpected mapping: %+v", mapping)
	}
	if err := mapping.Lease.Close(); err != nil {
		t.Fatal(err)
	}
	if err := mapping.Lease.Close(); err != nil {
		t.Fatal(err)
	}
	if adds.Load() != 1 || deletes.Load() != 1 {
		t.Fatalf("add/delete counts = %d/%d", adds.Load(), deletes.Load())
	}
}

func TestUPnPFallsBackWhenAddAnyIsUnsupported(t *testing.T) {
	var addAny, addSpecific atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		action := r.Header.Get("SOAPAction")
		switch {
		case strings.Contains(action, "GetExternalIPAddress"):
			fmt.Fprint(w, soapEnvelope(`<u:GetExternalIPAddressResponse xmlns:u="urn:schemas-upnp-org:service:WANIPConnection:2"><NewExternalIPAddress>203.0.113.88</NewExternalIPAddress></u:GetExternalIPAddressResponse>`))
		case strings.Contains(action, "AddAnyPortMapping"):
			addAny.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, soapEnvelope(`<s:Fault><detail><UPnPError><errorCode>401</errorCode><errorDescription>Invalid Action</errorDescription></UPnPError></detail></s:Fault>`))
		case strings.Contains(action, "AddPortMapping"):
			addSpecific.Add(1)
			fmt.Fprint(w, soapEnvelope(`<u:AddPortMappingResponse xmlns:u="urn:schemas-upnp-org:service:WANIPConnection:2"/>`))
		case strings.Contains(action, "DeletePortMapping"):
			fmt.Fprint(w, soapEnvelope(`<u:DeletePortMappingResponse xmlns:u="urn:schemas-upnp-org:service:WANIPConnection:2"/>`))
		default:
			http.Error(w, "unexpected action", 500)
		}
	}))
	defer server.Close()
	controlURL, _ := url.Parse(server.URL)
	service := upnpService{serviceType: "urn:schemas-upnp-org:service:WANIPConnection:2", controlURL: controlURL, client: server.Client()}
	mapping, err := service.addUDPMapping(context.Background(), netip.MustParseAddr("192.0.2.40"), mapConfig{localPort: 32000, externalPort: 41000, lifetime: time.Hour, description: "fallback"})
	if err != nil {
		t.Fatal(err)
	}
	defer mapping.Lease.Close()
	if mapping.ExternalPort != 41000 || addAny.Load() != 1 || addSpecific.Load() != 1 {
		t.Fatalf("unexpected fallback: mapping=%+v addAny=%d addSpecific=%d", mapping, addAny.Load(), addSpecific.Load())
	}
}

func TestUPnPFaultAndTimeoutPaths(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("SOAPAction"), "GetExternalIPAddress") {
			fmt.Fprint(w, soapEnvelope(`<u:GetExternalIPAddressResponse xmlns:u="urn:schemas-upnp-org:service:WANIPConnection:1"><NewExternalIPAddress>203.0.113.9</NewExternalIPAddress></u:GetExternalIPAddressResponse>`))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, soapEnvelope(`<s:Fault><detail><UPnPError><errorCode>718</errorCode><errorDescription>ConflictInMappingEntry</errorDescription></UPnPError></detail></s:Fault>`))
	}))
	defer server.Close()
	parsedURL, _ := url.Parse(server.URL)
	service := upnpService{serviceType: "urn:schemas-upnp-org:service:WANIPConnection:1", controlURL: parsedURL, client: server.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := service.addUDPMapping(ctx, netip.MustParseAddr("192.0.2.10"), mapConfig{localPort: 30000, lifetime: time.Hour, description: "test"})
	var soapErr *upnpSOAPError
	if !errors.As(err, &soapErr) || soapErr.Code != 718 {
		t.Fatalf("expected conflict SOAP error, got %v", err)
	}

	release := make(chan struct{})
	blocked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer func() {
		close(release)
		blocked.Close()
	}()
	blockedURL, _ := url.Parse(blocked.URL)
	service = upnpService{serviceType: "urn:schemas-upnp-org:service:WANIPConnection:1", controlURL: blockedURL, client: blocked.Client()}
	short, stop := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer stop()
	started := time.Now()
	_, err = service.soap(short, "GetExternalIPAddress", nil)
	if err == nil || time.Since(started) > 300*time.Millisecond {
		t.Fatalf("timeout path did not return promptly: err=%v elapsed=%s", err, time.Since(started))
	}
}
