package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"syscall"
	"time"
)

// allowLocalTargets decides whether delivering a message to a loopback /
// link-local address is allowed.
//
// Denied by default. These address ranges are not where an IM bot or a public
// email server would appear, yet what they can reach is sensitive: the management
// port of another service on the same host, and the cloud environment's metadata
// endpoint (169.254.169.254, from which instance credentials can be read). The
// delivery address is configured by an admin, but an admin session borrowed via
// XSS/CSRF, or a second person sharing the same JWT, could read the response back
// by editing the config — doJSON writes the first 200 bytes of a 4xx/5xx response
// body into last_error, and the delivery-history endpoint echoes it back, which is
// a semi-blind read primitive.
//
// But a "local SMTP relay" (postfix on 127.0.0.1:25) is a common self-hosted email
// configuration, and a blanket ban would get people stuck. So there is an explicit
// escape hatch rather than a hard-coded allow: set UROBOROS_NOTIFY_ALLOW_LOCAL=1 to
// permit it.
//
// It is exported as AllowLocalTargetsEnv so tests can turn it on explicitly — the
// tests in this package and the server package make heavy use of httptest fake
// receivers on 127.0.0.1, all of which the guard would otherwise block.
const AllowLocalTargetsEnv = "UROBOROS_NOTIFY_ALLOW_LOCAL"

func allowLocalTargets() bool {
	v := strings.TrimSpace(os.Getenv(AllowLocalTargetsEnv))
	return v == "1" || strings.EqualFold(v, "true")
}

// isBlockedDialIP reports whether the target IP falls into the "not allowed for
// delivery by default" address ranges.
//
// Only loopback, link-local (including cloud metadata 169.254.169.254),
// unspecified, and multicast are rejected. RFC1918 private networks are **not**
// rejected: a self-hosted Mattermost / SMTP relay on an internal network is a very
// common legitimate use, and blocking those too would make the feature directly
// unusable in real environments. This trade-off is deliberate — the protection
// must block the genuinely sensitive targets without taking out normal
// deployments along with them.
func isBlockedDialIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	// IPv4-mapped IPv6 (::ffff:127.0.0.1) must be reduced to IPv4 before checking,
	// otherwise it's a bypass.
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast()
}

// blockInternalDial is the Control hook of the http.Transport dialer, checking the
// target address **at connection time**.
//
// Why it is placed at the dial stage rather than only validating on config save:
// this is where it actually takes effect. It also covers both config-validation
// bypasses — DNS rebinding (resolves to a public IP at validation time but to an
// internal one at actual connection time) and redirects (although we already
// refuse cross-host hops, a same-host hop could still point the path elsewhere).
func blockInternalDial(_, address string, _ syscall.RawConn) error {
	if allowLocalTargets() {
		return nil
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("cannot resolve target address %q", host)
	}
	if isBlockedDialIP(ip) {
		return fmt.Errorf("refusing to deliver to the local/link-local address %s (if you really need to deliver to a local service, set %s=1)", ip, AllowLocalTargetsEnv)
	}
	return nil
}

// notifyTransport adds only a dial guard on top of the default Transport. It uses
// Clone to keep all of the default's tuning (connection pool, HTTP/2, timeouts,
// proxy, etc.), so adding one check does not change other behavior.
var notifyTransport = func() *http.Transport {
	t, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return &http.Transport{}
	}
	clone := t.Clone()
	clone.DialContext = (&net.Dialer{Timeout: 10 * time.Second, Control: blockInternalDial}).DialContext
	return clone
}()

// httpClient is the client shared by all channel deliveries.
//
// It deliberately does **not** reuse the project's global egress proxy
// (GlobalProxy on the server side): that proxy is for penetration-target traffic
// and is often an unstable tunnel, and the availability of notifications should
// not be held hostage by the jitter of the target network. IM pushes can connect
// directly. The timeout is set to 15 seconds — a far end slower than that is
// effectively in a fault state already.
//
// It refuses cross-host redirects: this feature's delivery addresses are all a
// "single fixed endpoint" shape and normally would not redirect to another host;
// yet these providers' credentials (DingTalk's access_token, WeCom's key,
// Telegram's bot token) are **right in the URL**, so following a cross-host hop
// hands the credential to the redirect target. A same-host hop (such as a trailing
// slash) is still allowed.
var httpClient = &http.Client{
	Timeout:   15 * time.Second,
	Transport: notifyTransport,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		if len(via) > 0 && req.URL.Host != via[0].URL.Host {
			return fmt.Errorf("refusing cross-host redirect (%s → %s)", via[0].URL.Host, req.URL.Host)
		}
		return nil
	},
}

// respBodyLimit caps the size of the response body read. The far end may return
// oversized content when something goes wrong, and we only need the status code
// and a short error description to show in the delivery history.
const respBodyLimit = 8 << 10

// doJSON sends one request and returns the response body (already length-capped).
//
// A nil payload sends an empty body (for GET, or platforms that do not require a
// body). The keys/values in headers are attached as-is, for the generic Webhook's
// custom headers.
//
// Error classification is this function's core responsibility: network-layer
// failures and 5xx/408/429 are classed as "retryable", and all other 4xx as
// "permanent failure" — retrying a 403 only prints the same error to the log 3
// times.
func doJSON(ctx context.Context, method, url string, headers map[string]string, payload any) ([]byte, error) {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			// A serialization failure is a local bug (wrong config field type), and
			// retrying won't improve it.
			return nil, Permanent(fmt.Errorf("failed to build request body: %w", err))
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		// Invalid URL — most likely the user mistyped the address, a permanent
		// failure. Again, err must not be passed through: url.Parse's error text
		// contains the full address.
		return nil, Permanent(fmt.Errorf("invalid request address: %s", redactRequestTarget(url)))
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		// Connection refused, DNS failure, timeout — mostly transient faults, left
		// to backoff retries.
		//
		// The error text must be redacted before being passed outward. The reason:
		// http.Client.Do returns a *url.Error whose Error() is `Op "fullURL":
		// underlying error`, and these providers' credentials are **right in the
		// URL** (DingTalk access_token, WeCom key, Feishu hook id, Telegram
		// /bot<token>/). Without redaction, the credential would flow along this
		// error string to four places: notification_deliveries.last_error (stored in
		// plaintext), the delivery-history endpoint's response (**bypassing the
		// channel config mask**), the server log, and the 502 text the test-send
		// endpoint returns to the front end.
		return nil, fmt.Errorf("request failed: %s", redactTransportError(err))
	}
	defer resp.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, respBodyLimit))
	if readErr != nil {
		return nil, fmt.Errorf("failed to read response: %w", readErr)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return raw, nil
	}
	// 429 (rate limited) and 408 (timeout) are worth retrying; other 4xx are config
	// or permission problems where retrying is pointless.
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusRequestTimeout {
		return nil, fmt.Errorf("far end rate-limited or timed out (HTTP %d): %s", resp.StatusCode, snippet(raw))
	}
	if resp.StatusCode >= 500 {
		return nil, fmt.Errorf("far end service error (HTTP %d): %s", resp.StatusCode, snippet(raw))
	}
	return nil, Permanent(fmt.Errorf("far end rejected the request (HTTP %d): %s", resp.StatusCode, snippet(raw)))
}

// snippet collapses the response body into a short single line, for use in error
// messages. The response may contain newlines and large amounts of whitespace, and
// dropping it straight into last_error would break the layout of the delivery
// history page.
func snippet(raw []byte) string {
	return OneLine(string(raw), 200)
}

// redactRequestTarget collapses a delivery address to "scheme://host/…", for use
// in error messages.
//
// This is the only address-redaction criterion in this package, deliberately made
// **blunt enough**: everything but the scheme and host is discarded. The reason is
// that there is no "generic and safe" way to tell which part of a URL is the
// credential:
//
//	DingTalk  credential in query       /robot/send?access_token=xxx
//	WeCom     credential in query       /cgi-bin/webhook/send?key=xxx
//	Feishu    credential in the **tail** of the path /open-apis/bot/v2/hook/<hook_id>
//	Telegram  credential in the **middle** of the path /bot<token>/sendMessage
//
// Trying to "keep only the useful parts" would require patching per channel, and
// missing any one of them is a credential leak. Keeping the host is already enough
// for troubleshooting (unresolvable DNS, unreachable, wrong certificate can all be
// located), and which specific bot it is can be recognized from the masked tail
// hint in the channel config.
//
// On a parse failure it returns a fixed placeholder — it never echoes the original
// string.
func redactRequestTarget(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "(address not parseable)"
	}
	return u.Scheme + "://" + u.Host + "/…"
}

// redactTransportError strips the address from a transport-layer error, keeping
// only the underlying cause.
//
// A *url.Error has the structure {Op, URL, Err}, and its Error() prints the URL
// along with it. This takes the Err field explicitly, bypassing its Error() —
// more reliable than string-replacing after the fact, since replacement would have
// to correctly handle all the URL-encoded/escaped variants and easily misses some.
func redactTransportError(err error) string {
	var uerr *url.Error
	if errors.As(err, &uerr) {
		host := ""
		if u, parseErr := url.Parse(uerr.URL); parseErr == nil {
			host = u.Host
		}
		if uerr.Err != nil {
			return fmt.Sprintf("%s %s: %s", uerr.Op, host, uerr.Err)
		}
		return fmt.Sprintf("%s %s: unknown error", uerr.Op, host)
	}
	// A non-*url.Error (such as the error returned by the redirect policy) may also
	// carry an address, so redact them all uniformly.
	return redactURLsInText(err.Error())
}

// redactURLsInText replaces any http(s) addresses appearing in a piece of text
// with their redacted form.
//
// Used as a backstop for errors that don't expose structured fields (redirect
// policy errors, third-party libraries' custom errors). It recognizes only the
// http/https prefix and splits on whitespace and quotes — an address won't contain
// either class of character.
func redactURLsInText(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		rest := s[i:]
		if strings.HasPrefix(rest, "http://") || strings.HasPrefix(rest, "https://") {
			end := len(rest)
			if j := strings.IndexAny(rest, " \t\n\"'"); j >= 0 {
				end = j
			}
			b.WriteString(redactRequestTarget(rest[:end]))
			i += end
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
