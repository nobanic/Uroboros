package notify

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"time"
)

// dingTalkChannel implements the DingTalk custom bot.
//
// Platform characteristics (which shape the implementation choices here):
//   - A single bot is rate-limited to 20 messages/minute, and overflow is
//     silently dropped (HTTP may still be 200), so rate limiting must be done
//     client-side, see DefaultRatePerMin.
//   - Security settings are one of three: signing / custom keyword / IP allow
//     list. Signing is the only option that does not depend on message content,
//     so only signing is supported (a bare webhook with none of the three enabled
//     is also supported).
//   - Both success and failure return HTTP 200, distinguished by the errcode in
//     the body — not checking errcode records a failed delivery as a success.
type dingTalkChannel = deliveryChannel[dingTalkProtocol]

type dingTalkProtocol struct{}

func (dingTalkProtocol) contract() deliveryDefinition {
	return deliveryDefinition{
		kind: KindDingTalk,
		rate: 20,
		// The DingTalk Webhook address carries access_token and is itself a credential,
		// so it is masked as a whole.
		secrets: []string{"webhook", "secret"},
		// The destination is the DingTalk Webhook address itself; changing it requires
		// re-affirming the signing secret for the new address.
		destinations: []string{"webhook"},
		validate:     validateBotWebhook,
		prepare:      prepareDingTalk,
		accept: decodeChannelReply[botCodeReply]("DingTalk", func(res botCodeReply) error {
			if res.ErrCode != 0 {
				// 301000 is signature-verification failure and 310000 is keyword mismatch —
				// both are config errors that retrying won't heal.
				return Permanent(fmt.Errorf("DingTalk returned error %d: %s", res.ErrCode, res.ErrMsg))
			}
			return nil
		}),
	}
}

func prepareDingTalk(cfg map[string]any, m Message) (channelDelivery, error) {
	hook := cfgString(cfg, "webhook")
	if err := validateBotWebhook(cfg); err != nil {
		return channelDelivery{}, Permanent(err)
	}
	endpoint, err := dingTalkSignedURL(hook, cfgString(cfg, "secret"), time.Now())
	if err != nil {
		return channelDelivery{}, Permanent(err)
	}

	title := markdownTitle(m)
	// DingTalk markdown body has no explicit byte limit, but we still cap it to
	// avoid an abnormally bloated evidence field.
	text, kept := markdownBody(m, 20000)

	var payload any
	if !m.Batch && len(m.Items) == 1 && m.Items[0].DetailURL != "" {
		payload = map[string]any{
			"msgtype": "actionCard",
			"actionCard": map[string]any{
				"title":          title,
				"text":           text,
				"btnOrientation": "0",
				"singleTitle":    "View details",
				"singleURL":      m.Items[0].DetailURL,
			},
		}
	} else {
		payload = map[string]any{
			"msgtype":  "markdown",
			"markdown": map[string]any{"title": title, "text": text},
		}
	}

	return channelDelivery{method: "POST", endpoint: endpoint, headers: nil, payload: payload, kept: kept}, nil
}

// dingTalkSignedURL appends the timestamp and sign parameters to the webhook per
// the official signing rules.
//
// Rule: string-to-sign = timestamp + "\n" + secret, where HMAC-SHA256's **key is
// also the secret**, and the result is base64-encoded then URL-encoded. The
// timestamp is in milliseconds. When secret is empty it is returned as-is, to
// support bots with signing disabled.
func dingTalkSignedURL(hook, secret string, now time.Time) (string, error) {
	if secret == "" {
		return hook, nil
	}
	ts := strconv.FormatInt(now.UnixMilli(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "\n" + secret))
	sign := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	u, err := url.Parse(hook)
	if err != nil {
		// Don't pass err through: url.Parse's error text carries the full address
		// (including access_token).
		return "", fmt.Errorf("failed to parse Webhook address: %s", redactRequestTarget(hook))
	}
	q := u.Query()
	q.Set("timestamp", ts)
	q.Set("sign", sign)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// validateHTTPURL checks that the address is usable, that the scheme is supported,
// and performs an internal-network check for a literal IP target.
//
// Two points of care:
//
//  1. **The error message must be redacted**. url.Parse itself returns a
//     *url.Error whose Error() carries the **full original address**, and these
//     providers' addresses have credentials embedded right in them (DingTalk
//     access_token, WeCom key, Telegram bot token, Feishu hook id). This used to
//     `return err` directly, so the "invalid address format" error carried the
//     credential out, flowing into the test endpoint's 400 response, the
//     last_error persisted on every delivery, the server log, and the
//     delivery-history endpoint.
//
//  2. **A literal IP is checked for internal network directly**, while domains are
//     left to the dial stage (blockInternalDial is where it actually takes effect
//     and also covers DNS rebinding). Doing it once here is to give a hint at
//     config-save time rather than waiting for the first delivery to fail.
//
// Restricting the scheme is defensive: file:/// gopher:// and the like could make
// http.Client behave unexpectedly (already blocked by the scheme check, but there
// is no reason to open up that surface).
func validateHTTPURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("address could not be parsed (%s)", redactRequestTarget(raw))
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("only http/https is supported, got %q", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("missing host name")
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil && isBlockedDialIP(ip) && !allowLocalTargets() {
		return fmt.Errorf("refusing to deliver to the local/link-local address %s (if you really need to deliver to a local service, set %s=1)", ip, AllowLocalTargetsEnv)
	}
	return nil
}
