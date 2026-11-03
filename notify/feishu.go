package notify

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strconv"
	"time"
)

// feishuChannel implements the Feishu (including Lark) custom bot, using
// interactive cards.
//
// Platform characteristics:
//   - The signing algorithm **differs** from DingTalk's and is very easy to get
//     wrong, see the feishuSign comment.
//   - Like DingTalk, it stuffs business errors into the HTTP 200 body (code != 0).
//   - The card header supports a color template; mapping the severity to a color
//     lets people spot the severity at a glance in the message list.
type feishuChannel = deliveryChannel[feishuProtocol]

type feishuProtocol struct{}

func (feishuProtocol) contract() deliveryDefinition {
	return deliveryDefinition{
		kind: KindFeishu,
		// A Feishu custom bot is about 5 times/second, i.e. 100 times/minute.
		rate: 100,
		// The tail of the Webhook address is the bot's unique identifier, so it is a
		// credential.
		secrets: []string{"webhook", "secret"},
		// Likewise: changing the Webhook address requires re-affirming the signing secret
		// for the new address.
		destinations: []string{"webhook"},
		validate:     validateBotWebhook,
		prepare:      prepareFeishu,
		accept: decodeChannelReply[feishuReply]("Feishu", func(res feishuReply) error {
			if res.Code != 0 {
				return Permanent(fmt.Errorf("Feishu returned error %d: %s", res.Code, res.Msg))
			}
			if res.StatusCode != 0 {
				return Permanent(fmt.Errorf("Feishu returned error %d: %s", res.StatusCode, res.StatusMessage))
			}
			return nil
		}),
	}
}

func prepareFeishu(cfg map[string]any, m Message) (channelDelivery, error) {
	if err := validateBotWebhook(cfg); err != nil {
		return channelDelivery{}, Permanent(err)
	}
	card, kept := feishuCard(m)
	payload := map[string]any{
		"msg_type": "interactive",
		"card":     card,
	}
	// The signing parameters sit at the same level as the message, and appear only
	// when a secret is configured.
	if secret := cfgString(cfg, "secret"); secret != "" {
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		payload["timestamp"] = ts
		payload["sign"] = feishuSign(ts, secret)
	}
	return channelDelivery{method: "POST", endpoint: cfgString(cfg, "webhook"), headers: nil, payload: payload, kept: kept}, nil
}

// feishuSign computes the signature per Feishu's official rules.
//
// This is especially easy to trip over: the official sample is
//
//	hmac.new(string_to_sign.encode(), digestmod=sha256)
//
// that is, **key = timestamp + "\n" + secret, with an empty message**, rather than
// the intuitive "key=secret, message=stringToSign" — which is exactly DingTalk's
// algorithm. The two algorithms are precisely reversed, so copying the other
// provider's implementation is guaranteed to fail signature verification (error
// 19021).
func feishuSign(timestamp, secret string) string {
	stringToSign := timestamp + "\n" + secret
	mac := hmac.New(sha256.New, []byte(stringToSign))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// feishuSeverityTemplate maps a vulnerability severity to a card-header color
// template. Unknown severities use grey — not blue, to avoid confusion with low.
func feishuSeverityTemplate(severity string) string {
	switch severity {
	case "critical":
		return "red"
	case "high":
		return "orange"
	case "medium":
		return "yellow"
	case "low":
		return "blue"
	default:
		return "grey"
	}
}

// feishuMaxCardBytes is a conservative cap on card content. Feishu has a card size
// limit and rejects the whole thing if exceeded; pick a value clearly below the
// official limit, accounting for the JSON wrapping overhead too.
const feishuMaxCardBytes = 24000

// feishuCard builds the interactive card, returning the card and the **number of
// items actually written**. kept serves the same purpose as in markdownBody: only
// items that truly made it into the card should be marked as delivered.
func feishuCard(m Message) (map[string]any, int) { return renderNoticeCard(m) }

func feishuMarkdownDiv(content string) map[string]any {
	return map[string]any{"tag": "div", "text": map[string]any{"tag": "lark_md", "content": content}}
}

func feishuButton(label, url string) map[string]any {
	return map[string]any{
		"tag": "action",
		"actions": []any{map[string]any{
			"tag":  "button",
			"text": map[string]any{"tag": "lark_md", "content": label},
			"url":  url,
			"type": "primary",
		}},
	}
}

// feishuItemLines renders the lark_md body of a single vulnerability.
//
// lark_md and markdown are text formats of the same family and both parse links
// and emphasis, so externally sourced fields all go through markdownText
// (single-lining + escaping) — otherwise a vulnerability title alone could become
// a clickable external link in Feishu.
func feishuItemLines(it Item) string { return cardFinding(it) }

// feishuBatchLine renders a single line in the digest card.
func feishuBatchLine(it Item, index int) string { return noticeFindingLine(it, index, true) }
