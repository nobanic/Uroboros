package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// A channel is a zero-value protocol marker. Definitions contain policy and
// functions only; destinations and credentials remain per-call inputs. Slice
// descriptors are built afresh, retaining callers' independent mutable copies.
type deliveryProtocol interface{ contract() deliveryDefinition }
type deliveryChannel[P deliveryProtocol] struct{}
type deliveryDefinition struct {
	kind                  string
	rate                  int
	secrets, destinations []string
	validate              func(map[string]any) error
	prepare               func(map[string]any, Message) (channelDelivery, error)
	accept                func([]byte) error
}
type channelDelivery struct {
	method, endpoint string
	headers          map[string]string
	payload          any
	kept             int
}

func (deliveryChannel[P]) Kind() string           { var protocol P; return protocol.contract().kind }
func (deliveryChannel[P]) DefaultRatePerMin() int { var protocol P; return protocol.contract().rate }
func (deliveryChannel[P]) SecretKeys() []string   { var protocol P; return protocol.contract().secrets }
func (deliveryChannel[P]) DestinationKeys() []string {
	var protocol P
	return protocol.contract().destinations
}
func (deliveryChannel[P]) Validate(cfg map[string]any) error {
	var protocol P
	return protocol.contract().validate(cfg)
}

func (deliveryChannel[P]) Send(ctx context.Context, cfg map[string]any, message Message) (int, error) {
	var protocol P
	definition := protocol.contract()
	request, err := definition.prepare(cfg, message)
	if err != nil {
		return 0, err
	}
	raw, err := doJSON(ctx, request.method, request.endpoint, request.headers, request.payload)
	if err != nil {
		return 0, err
	}
	if definition.accept != nil {
		if err := definition.accept(raw); err != nil {
			return 0, err
		}
	}
	return request.kept, nil
}

func validateBotWebhook(cfg map[string]any) error {
	hook := cfgString(cfg, "webhook")
	if hook == "" {
		return errors.New("missing Webhook address")
	}
	if err := validateHTTPURL(hook); err != nil {
		return fmt.Errorf("invalid Webhook address: %w", err)
	}
	return nil
}

// Aliases retain the anonymous response types used by the original decoders.
// Named structs would change encoding/json error fields and error text.
type botCodeReply = struct {
	ErrCode int    `json:"errcode"`
	ErrMsg  string `json:"errmsg"`
}

type feishuReply = struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	// Some versions of the Feishu hook use this set of field names; support
	// them too.
	StatusCode    int    `json:"StatusCode"`
	StatusMessage string `json:"StatusMessage"`
}

type telegramReply = struct {
	OK          bool   `json:"ok"`
	ErrorCode   int    `json:"error_code"`
	Description string `json:"description"`
}

func decodeChannelReply[T any](provider string, reject func(T) error) func([]byte) error {
	return func(raw []byte) error {
		var response T
		if err := json.Unmarshal(raw, &response); err != nil {
			return fmt.Errorf("failed to parse %s response: %w (%s)", provider, err, snippet(raw))
		}
		return reject(response)
	}
}
