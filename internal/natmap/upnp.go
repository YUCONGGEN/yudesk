package natmap

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	ssdpAddress      = "239.255.255.250:1900"
	maxUPnPDocument  = 1 << 20
	upnpCloseTimeout = 800 * time.Millisecond
)

type upnpService struct {
	serviceType string
	controlURL  *url.URL
	client      *http.Client
}

type upnpSOAPError struct {
	Code        int
	Description string
}

func (err *upnpSOAPError) Error() string {
	if err.Description == "" {
		return fmt.Sprintf("UPnP error %d", err.Code)
	}
	return fmt.Sprintf("UPnP error %d: %s", err.Code, err.Description)
}

type soapArg struct {
	name, value string
}

func mapUPnP(ctx context.Context, socket *net.UDPConn, config mapConfig) (Mapping, error) {
	localIP, err := localIPv4(socket, netip.Addr{})
	if err != nil {
		return Mapping{}, err
	}
	services, err := discoverUPnP(ctx, localIP)
	if err != nil && len(services) == 0 {
		return Mapping{}, err
	}
	var failures []error
	for _, service := range services {
		mapping, err := service.addUDPMapping(ctx, localIP, config)
		if err == nil {
			return mapping, nil
		}
		failures = append(failures, err)
		if ctx.Err() != nil {
			break
		}
	}
	if len(failures) == 0 {
		return Mapping{}, errors.New("natmap: no UPnP IGD service found")
	}
	return Mapping{}, errors.Join(failures...)
}

func discoverUPnP(ctx context.Context, localIP netip.Addr) ([]upnpService, error) {
	address := &net.UDPAddr{Port: 0}
	if localIP.Is4() {
		value := localIP.As4()
		address.IP = net.IP(value[:])
	}
	connection, err := net.ListenUDP("udp4", address)
	if err != nil {
		return nil, fmt.Errorf("natmap: start UPnP discovery: %w", err)
	}
	defer connection.Close()
	target, _ := net.ResolveUDPAddr("udp4", ssdpAddress)
	deadline := time.Now().Add(550 * time.Millisecond)
	if value, ok := ctx.Deadline(); ok && value.Before(deadline) {
		deadline = value
	}
	_ = connection.SetDeadline(deadline)
	searchTargets := []string{
		"urn:schemas-upnp-org:service:WANIPConnection:2",
		"urn:schemas-upnp-org:service:WANIPConnection:1",
		"urn:schemas-upnp-org:device:InternetGatewayDevice:2",
		"urn:schemas-upnp-org:device:InternetGatewayDevice:1",
	}
	for _, targetType := range searchTargets {
		request := "M-SEARCH * HTTP/1.1\r\nHOST: " + ssdpAddress + "\r\nMAN: \"ssdp:discover\"\r\nMX: 1\r\nST: " + targetType + "\r\n\r\n"
		_, _ = connection.WriteToUDP([]byte(request), target)
	}
	locations := make(map[string]struct{})
	buffer := make([]byte, 64<<10)
	shortened := false
	for {
		n, _, readErr := connection.ReadFromUDP(buffer)
		if readErr != nil {
			if netErr, ok := readErr.(net.Error); ok && netErr.Timeout() {
				break
			}
			if ctx.Err() != nil {
				break
			}
			return nil, readErr
		}
		location, parseErr := parseSSDPResponse(buffer[:n])
		if parseErr == nil {
			locations[location] = struct{}{}
			if !shortened {
				shortened = true
				grace := time.Now().Add(100 * time.Millisecond)
				if grace.Before(deadline) {
					_ = connection.SetDeadline(grace)
				}
			}
		}
	}
	client := shortHTTPClient()
	var services []upnpService
	var failures []error
	seen := make(map[string]struct{})
	for location := range locations {
		values, err := fetchUPnPServices(ctx, client, location)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		for _, service := range values {
			key := service.serviceType + "\x00" + service.controlURL.String()
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			services = append(services, service)
		}
	}
	if len(services) > 0 {
		return services, nil
	}
	if len(failures) > 0 {
		return nil, errors.Join(failures...)
	}
	return nil, errors.New("natmap: UPnP discovery returned no gateway")
}

func parseSSDPResponse(packet []byte) (string, error) {
	reader := textproto.NewReader(bufio.NewReader(bytes.NewReader(packet)))
	status, err := reader.ReadLine()
	if err != nil || !strings.HasPrefix(strings.ToUpper(status), "HTTP/1.1 200") {
		return "", errors.New("natmap: invalid SSDP response")
	}
	headers, err := reader.ReadMIMEHeader()
	if err != nil {
		return "", err
	}
	location := strings.TrimSpace(headers.Get("Location"))
	parsed, err := url.Parse(location)
	if err != nil || !safeUPnPURL(parsed) {
		return "", errors.New("natmap: invalid UPnP description URL")
	}
	return parsed.String(), nil
}

type upnpRoot struct {
	URLBase string     `xml:"URLBase"`
	Device  upnpDevice `xml:"device"`
}

type upnpDevice struct {
	Services []upnpDescriptionService `xml:"serviceList>service"`
	Devices  []upnpDevice             `xml:"deviceList>device"`
}

type upnpDescriptionService struct {
	ServiceType string `xml:"serviceType"`
	ControlURL  string `xml:"controlURL"`
}

func fetchUPnPServices(ctx context.Context, client *http.Client, location string) ([]upnpService, error) {
	descriptionURL, err := url.Parse(location)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, descriptionURL.String(), nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("natmap: read UPnP description: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		return nil, fmt.Errorf("natmap: UPnP description HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxUPnPDocument+1))
	if err != nil || len(body) > maxUPnPDocument {
		return nil, errors.New("natmap: invalid UPnP description size")
	}
	var root upnpRoot
	if err := xml.Unmarshal(body, &root); err != nil {
		return nil, fmt.Errorf("natmap: parse UPnP description: %w", err)
	}
	base := descriptionURL
	if strings.TrimSpace(root.URLBase) != "" {
		if parsed, err := url.Parse(strings.TrimSpace(root.URLBase)); err == nil && safeUPnPURL(parsed) {
			base = parsed
		}
	}
	var descriptions []upnpDescriptionService
	collectUPnPServices(root.Device, &descriptions)
	var services []upnpService
	for _, value := range descriptions {
		if !isWANConnectionService(value.ServiceType) {
			continue
		}
		control, err := url.Parse(strings.TrimSpace(value.ControlURL))
		if err != nil {
			continue
		}
		control = base.ResolveReference(control)
		if !safeUPnPURL(control) {
			continue
		}
		services = append(services, upnpService{serviceType: strings.TrimSpace(value.ServiceType), controlURL: control, client: client})
	}
	if len(services) == 0 {
		return nil, errors.New("natmap: description has no WAN connection service")
	}
	return services, nil
}

func safeUPnPURL(value *url.URL) bool {
	if value == nil || value.Host == "" || value.User != nil || value.Scheme != "http" && value.Scheme != "https" {
		return false
	}
	ip := net.ParseIP(value.Hostname())
	return ip != nil && (ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast())
}

func collectUPnPServices(device upnpDevice, output *[]upnpDescriptionService) {
	*output = append(*output, device.Services...)
	for _, child := range device.Devices {
		collectUPnPServices(child, output)
	}
}

func isWANConnectionService(value string) bool {
	return value == "urn:schemas-upnp-org:service:WANIPConnection:2" ||
		value == "urn:schemas-upnp-org:service:WANIPConnection:1" ||
		value == "urn:schemas-upnp-org:service:WANPPPConnection:1"
}

func shortHTTPClient() *http.Client {
	return &http.Client{CheckRedirect: func(request *http.Request, via []*http.Request) error {
		if len(via) >= 3 || !safeUPnPURL(request.URL) {
			return errors.New("natmap: unsafe UPnP redirect")
		}
		return nil
	}, Transport: &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 450 * time.Millisecond, KeepAlive: -1}).DialContext,
		DisableKeepAlives:     true,
		ResponseHeaderTimeout: 600 * time.Millisecond,
	}}
}

func (service upnpService) addUDPMapping(ctx context.Context, localIP netip.Addr, config mapConfig) (Mapping, error) {
	externalText, err := service.soap(ctx, "GetExternalIPAddress", nil)
	if err != nil {
		return Mapping{}, err
	}
	externalIP, err := netip.ParseAddr(findXMLText(externalText, "NewExternalIPAddress"))
	if err != nil || externalIP.IsUnspecified() {
		return Mapping{}, errors.New("natmap: UPnP returned an invalid external address")
	}
	externalIP = externalIP.Unmap()
	suggested := config.externalPort
	if suggested == 0 {
		suggested = config.localPort
	}
	lifetime := durationSeconds(config.lifetime)

	var externalPort uint16
	if strings.HasSuffix(service.serviceType, ":2") {
		response, addErr := service.addAnyPort(ctx, localIP, config, suggested, lifetime)
		if addErr == nil {
			value, parseErr := strconv.ParseUint(findXMLText(response, "NewReservedPort"), 10, 16)
			if parseErr != nil || value == 0 {
				return Mapping{}, errors.New("natmap: UPnP AddAnyPortMapping omitted the reserved port")
			}
			externalPort = uint16(value)
		}
	}
	if externalPort == 0 {
		externalPort, err = service.addSpecificPort(ctx, localIP, config, suggested, lifetime)
		if err != nil {
			return Mapping{}, err
		}
	}
	lease := &closeOnce{fn: func() error {
		closeCtx, cancel := context.WithTimeout(context.Background(), upnpCloseTimeout)
		defer cancel()
		_, err := service.soap(closeCtx, "DeletePortMapping", []soapArg{
			{name: "NewRemoteHost", value: ""},
			{name: "NewExternalPort", value: strconv.Itoa(int(externalPort))},
			{name: "NewProtocol", value: "UDP"},
		})
		var soapErr *upnpSOAPError
		if errors.As(err, &soapErr) && soapErr.Code == 714 {
			return nil
		}
		return err
	}}
	return Mapping{ExternalIP: externalIP, ExternalPort: externalPort, Method: MethodUPnP, Lease: lease}, nil
}

func (service upnpService) addAnyPort(ctx context.Context, localIP netip.Addr, config mapConfig, suggested uint16, lifetime uint32) ([]byte, error) {
	return service.soap(ctx, "AddAnyPortMapping", mappingArguments(localIP, config, suggested, lifetime))
}

func (service upnpService) addSpecificPort(ctx context.Context, localIP netip.Addr, config mapConfig, suggested uint16, lifetime uint32) (uint16, error) {
	ports := []uint16{suggested}
	for config.externalPort == 0 && len(ports) < 4 {
		candidate := randomHighPort()
		duplicate := false
		for _, value := range ports {
			duplicate = duplicate || value == candidate
		}
		if !duplicate {
			ports = append(ports, candidate)
		}
	}
	var failures []error
	for _, port := range ports {
		_, err := service.soap(ctx, "AddPortMapping", mappingArguments(localIP, config, port, lifetime))
		if err == nil {
			return port, nil
		}
		failures = append(failures, err)
		if ctx.Err() != nil {
			break
		}
	}
	return 0, errors.Join(failures...)
}

func mappingArguments(localIP netip.Addr, config mapConfig, externalPort uint16, lifetime uint32) []soapArg {
	return []soapArg{
		{name: "NewRemoteHost", value: ""},
		{name: "NewExternalPort", value: strconv.Itoa(int(externalPort))},
		{name: "NewProtocol", value: "UDP"},
		{name: "NewInternalPort", value: strconv.Itoa(int(config.localPort))},
		{name: "NewInternalClient", value: localIP.String()},
		{name: "NewEnabled", value: "1"},
		{name: "NewPortMappingDescription", value: config.description},
		{name: "NewLeaseDuration", value: strconv.FormatUint(uint64(lifetime), 10)},
	}
}

func randomHighPort() uint16 {
	var value [2]byte
	if _, err := rand.Read(value[:]); err != nil {
		return 49152
	}
	return 49152 + binary.BigEndian.Uint16(value[:])%(65535-49152+1)
}

func (service upnpService) soap(ctx context.Context, action string, arguments []soapArg) ([]byte, error) {
	var body strings.Builder
	body.WriteString(`<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/"><s:Body><u:`)
	body.WriteString(action)
	body.WriteString(` xmlns:u="`)
	writeXMLEscaped(&body, service.serviceType)
	body.WriteString(`">`)
	for _, argument := range arguments {
		body.WriteByte('<')
		body.WriteString(argument.name)
		body.WriteByte('>')
		writeXMLEscaped(&body, argument.value)
		body.WriteString("</")
		body.WriteString(argument.name)
		body.WriteByte('>')
	}
	body.WriteString("</u:")
	body.WriteString(action)
	body.WriteString("></s:Body></s:Envelope>")
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, service.controlURL.String(), strings.NewReader(body.String()))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", `text/xml; charset="utf-8"`)
	request.Header.Set("SOAPAction", `"`+service.serviceType+`#`+action+`"`)
	client := service.client
	if client == nil {
		client = shortHTTPClient()
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("natmap: UPnP %s: %w", action, err)
	}
	defer response.Body.Close()
	payload, readErr := io.ReadAll(io.LimitReader(response.Body, maxUPnPDocument+1))
	if readErr != nil || len(payload) > maxUPnPDocument {
		return nil, errors.New("natmap: invalid UPnP SOAP response")
	}
	if response.StatusCode/100 != 2 || bytes.Contains(payload, []byte("<errorCode>")) {
		code, _ := strconv.Atoi(findXMLText(payload, "errorCode"))
		return nil, &upnpSOAPError{Code: code, Description: findXMLText(payload, "errorDescription")}
	}
	return payload, nil
}

func writeXMLEscaped(writer io.Writer, value string) {
	_ = xml.EscapeText(writer, []byte(value))
}

func findXMLText(payload []byte, localName string) string {
	decoder := xml.NewDecoder(bytes.NewReader(payload))
	for {
		token, err := decoder.Token()
		if err != nil {
			return ""
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != localName {
			continue
		}
		var value string
		if decoder.DecodeElement(&value, &start) == nil {
			return strings.TrimSpace(value)
		}
		return ""
	}
}
