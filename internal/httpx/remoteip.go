package httpx

import (
	"net"
	"net/http"
)

// RemoteIP returns r.RemoteAddr with the client's ephemeral source port
// stripped. The port is different on every TCP connection, even from the
// same client, so keying anything (a rate-limit bucket, a coarse
// per-source throttle) on the whole RemoteAddr gives a fresh key to
// every new connection — this is the shared fix, used by internal/proxy
// for rate-limit bucketing and internal/identity for a pre-verification
// throttle ahead of the nonce cache. Falls back to the raw RemoteAddr if
// it isn't in host:port form.
func RemoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
