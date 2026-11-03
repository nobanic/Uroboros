package notify

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// telegramTextLimit is the upper bound (in characters) of the text field of
// Telegram's sendMessage.
const telegramTextLimit = 4096

// telegramChannel implements the Telegram Bot API.
//
// Platform characteristics:
//   - Auth is entirely in the URL path (/bot<token>/sendMessage); no signing
//     needed.
//   - It uses HTML parse mode rather than MarkdownV2: MarkdownV2 requires escaping
//     `_*[]()~`>#+-=|{}.!`, 18 characters in all, and missing one gets the whole
//     message rejected; HTML needs only & < > escaped.
//   - Business errors are likewise hidden in HTTP 200, judged by the ok field.
type telegramChannel = deliveryChannel[telegramProtocol]

type telegramProtocol struct{}

func (telegramProtocol) contract() deliveryDefinition {
	return deliveryDefinition{
		kind: KindTelegram,
		// Telegram is about 1 message/second for a single chat and 20 messages/minute for
		// a group. Use the conservative value.
		rate: 20,
		// The Bot Token is the full credential; chat_id is only the recipient and is not a
		// secret (having it without the Token still can't send a message).
		secrets: []string{"bot_token"},
		// base_url decides which API endpoint the Token is sent to (such as a self-hosted
		// reverse proxy), so changing it requires re-affirming the Token.
		destinations: []string{"base_url"},
		validate:     validateTelegramConfig,
		prepare:      prepareTelegram,
		accept: decodeChannelReply[telegramReply]("Telegram", func(res telegramReply) error {
			if res.OK {
				return nil
			}
			// 429 is rate limiting, where retrying after backoff works; the rest (400 bad
			// parameter, 401 bad token, 403 blocked, 404 chat not found) are config
			// problems that retrying won't heal.
			if res.ErrorCode == 429 {
				return fmt.Errorf("Telegram rate limited: %s", res.Description)
			}
			return Permanent(fmt.Errorf("Telegram returned error %d: %s", res.ErrorCode, res.Description))
		}),
	}
}

func validateTelegramConfig(cfg map[string]any) error {
	if cfgString(cfg, "bot_token") == "" {
		return errors.New("missing Bot Token")
	}
	if cfgString(cfg, "chat_id") == "" {
		return errors.New("missing Chat ID")
	}
	if base := cfgString(cfg, "base_url"); base != "" {
		if err := validateHTTPURL(base); err != nil {
			return fmt.Errorf("invalid API address: %w", err)
		}
	}
	return nil
}

func prepareTelegram(cfg map[string]any, m Message) (channelDelivery, error) {
	if err := validateTelegramConfig(cfg); err != nil {
		return channelDelivery{}, Permanent(err)
	}
	endpoint, err := telegramEndpoint(cfg)
	if err != nil {
		return channelDelivery{}, Permanent(err)
	}
	text, kept := telegramHTML(m)
	payload := map[string]any{
		"chat_id":                  cfgString(cfg, "chat_id"),
		"text":                     text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": false,
	}
	return channelDelivery{method: "POST", endpoint: endpoint, headers: nil, payload: payload, kept: kept}, nil
}

// telegramEndpoint builds the sendMessage address. When base_url is empty it uses
// the official API; when non-empty it is for a self-hosted Bot API reverse proxy
// (a common need on networks inside China).
func telegramEndpoint(cfg map[string]any) (string, error) {
	base := cfgString(cfg, "base_url")
	if base == "" {
		base = "https://api.telegram.org"
	}
	base = strings.TrimSuffix(base, "/")
	token := cfgString(cfg, "bot_token")
	raw := base + "/bot" + token + "/sendMessage"
	u, err := url.Parse(raw)
	if err != nil {
		// Don't pass err through: the address contains the Bot Token, and even addr
		// should not be echoed at this point.
		return "", fmt.Errorf("failed to assemble API address (API address: %s)", redactRequestTarget(base))
	}
	return u.String(), nil
}

// telegramHTML renders the HTML body, returning the body and the number of items
// actually written (see Channel.Send).
func telegramHTML(m Message) (string, int) {
	return renderNoticeText(m, telegramTextLimit, telegramNoticeFormat())
}

// telegramReservedRunes is reserved for the message title and a possible
// truncation notice (measured in characters).
const telegramReservedRunes = 160

// telegramBatchLine renders a single line in the digest (unescaped; the caller
// escapes uniformly).
func telegramBatchLine(it Item, idx int) string { return noticeFindingLine(it, idx, false) }

// telegramBatchTitle renders the title line of a digest message. The count is the
// number **actually included in this message**, not the batch's total — otherwise
// the reader assumes the number written in the header is the whole thing.
func telegramBatchTitle(m Message, items []Item, total int) string {
	return (noticeDigest{m, items, total}).telegram()
}

// telegramEscape escapes HTML text content.
// Telegram recognizes only these three entities, and after escaping an existing
// entity like &amp; gets double-escaped — which is exactly the intended behavior:
// we want to display the raw characters, not let the user inject HTML.
func telegramEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

// telegramEscapeAttr escapes an HTML attribute value. Beyond text escaping it also
// handles quotes — a quote in the URL would close the href attribute early and
// turn what follows into an injection point.
func telegramEscapeAttr(s string) string {
	s = telegramEscape(s)
	s = strings.ReplaceAll(s, "\"", "&quot;")
	return s
}
