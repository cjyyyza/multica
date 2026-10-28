package p4depot

import "strings"

// EndpointKey normalizes equivalent default IPv4 spellings without erasing
// TLS or the caller's explicit IPv6/address-family selection.
func EndpointKey(port string) string {
	port = strings.ToLower(strings.TrimSpace(port))
	for _, prefix := range []string{"tcp:", "tcp4:"} {
		if strings.HasPrefix(port, prefix) {
			return "tcp:" + strings.TrimPrefix(port, prefix)
		}
	}
	if strings.HasPrefix(port, "ssl4:") {
		return "ssl:" + strings.TrimPrefix(port, "ssl4:")
	}
	if portPrefix.MatchString(port) || strings.HasPrefix(port, "rsh:") {
		return port
	}
	return "tcp:" + port
}

func SameEndpoint(a, b string) bool { return EndpointKey(a) == EndpointKey(b) }

// Port-derived identities cannot prove that two different endpoints are peers.
func ClusterKey(key, port string) bool {
	return key != "" && !strings.EqualFold(key, strings.TrimSpace(port)) &&
		!strings.EqualFold(key, "port:"+strings.TrimSpace(port))
}

func SameTransport(a, b string) bool {
	return strings.HasPrefix(EndpointKey(a), "ssl") == strings.HasPrefix(EndpointKey(b), "ssl") &&
		!strings.HasPrefix(strings.ToLower(a), "rsh:") && !strings.HasPrefix(strings.ToLower(b), "rsh:")
}
