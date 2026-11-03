package notify

import (
	"fmt"
)

// weComMarkdownLimit is the hard cap on a WeCom group bot's markdown content
// (bytes, not characters). It is the tightest limit of all six channels and the
// main reason TruncateBytes exists.
const weComMarkdownLimit = 4096

// weComChannel implements the WeCom group bot.
//
// Platform characteristics:
//   - The only one that authenticates via a key in the URL and does not support
//     signing — so the webhook address itself is the entire credential.
//   - markdown content is capped at 4096 **bytes**, and exceeding it rejects the
//     whole thing (not truncation). A Chinese character is 3 bytes/char, meaning
//     the body has room for only a thousand-odd characters, so client-side
//     truncation is required.
//   - Rate-limited to 20 messages/minute, also backstopped by client-side rate
//     limiting.
type weComChannel = deliveryChannel[weComProtocol]

type weComProtocol struct{}

func (weComProtocol) contract() deliveryDefinition {
	return deliveryDefinition{
		kind: KindWeCom,
		rate: 20,
		// WeCom has only one credential, the Webhook (the key in the URL), and it does not
		// support signing — the whole address is the entire credential, and no other field
		// needs masking.
		secrets: []string{"webhook"},
		// WeCom has only the one Webhook field, which is both the destination and the
		// credential, so there is no "credential left over after an address change" to
		// speak of.
		destinations: []string{"webhook"},
		validate:     validateBotWebhook,
		prepare:      prepareWeCom,
		accept: decodeChannelReply[botCodeReply]("WeCom", func(res botCodeReply) error {
			if res.ErrCode != 0 {
				// 45009 is "API call over the limit" — the platform's rate-limit window
				// rolls, so retrying after backoff is effective, hence it is explicitly
				// classed as retryable. Reaching here means the client's rate_per_min is
				// configured too aggressively; retrying is only a backstop, and the real fix
				// is to lower that channel's rate limit.
				if res.ErrCode == 45009 {
					return fmt.Errorf("WeCom rate limited %d: %s", res.ErrCode, res.ErrMsg)
				}
				// 93000 is an invalid webhook key — a permanent failure that retrying won't
				// heal.
				return Permanent(fmt.Errorf("WeCom returned error %d: %s", res.ErrCode, res.ErrMsg))
			}
			return nil
		}),
	}
}

func prepareWeCom(cfg map[string]any, m Message) (channelDelivery, error) {
	if err := validateBotWebhook(cfg); err != nil {
		return channelDelivery{}, Permanent(err)
	}
	// A digest batch can be long (50 items × one line each + prefix), so 4096 bytes
	// is easily exceeded. Truncation is done here rather than relying on the
	// platform's rejection: a rejection means the whole batch is lost, whereas
	// truncation at least delivers the first several items.
	content, kept := markdownBody(m, weComMarkdownLimit)
	payload := map[string]any{
		"msgtype":  "markdown",
		"markdown": map[string]any{"content": content},
	}
	return channelDelivery{method: "POST", endpoint: cfgString(cfg, "webhook"), headers: nil, payload: payload, kept: kept}, nil
}
