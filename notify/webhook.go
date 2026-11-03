package notify

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"text/template"
	"time"
)

// webhookChannel is the generic Webhook adapter: user-defined URL, method,
// request headers, and JSON template. Its existence saves this feature from
// writing a separate implementation for Slack / Mattermost / Discord / in-house
// systems — all of those can be covered by one configurable template.
type webhookChannel = deliveryChannel[webhookProtocol]

type webhookProtocol struct{}

func (webhookProtocol) contract() deliveryDefinition {
	return deliveryDefinition{
		kind: KindWebhook,
		// A generic Webhook has no official limit; returning 0 means no rate limit by
		// default, to be set by the user per the far end's capacity.
		rate: 0,
		// Mask url and headers: the target address itself often carries a token, and
		// custom headers usually hold auth credentials, both of which appear in the API's
		// echoed values, so both must be shielded.
		// The cost is that to change one header while editing, the whole header set must
		// be re-entered (a masked value is interpreted as "keep the original") — this
		// trade-off is deliberate: better to fill it in once more than to echo credentials
		// to the browser.
		secrets: []string{"url", "headers"},
		// The destination is url. When url changes, headers must be re-affirmed —
		// otherwise the original Authorization header would be sent as-is to the new
		// address, which is exactly the main mask-bypass path.
		destinations: []string{"url"},
		validate:     validateWebhookConfig,
		prepare:      prepareWebhook,
	}
}

// webhookDefaultTemplate is the fallback request body when no template is filled
// in: a straightforward JSON structure that covers the vast majority of in-house
// receivers that "take a JSON and store it".
const webhookDefaultTemplate = `{
  "title": {{json .Title}},
  "batch": {{.Batch}},
  "count": {{.Count}},
  "items": [
{{- range $i, $it := .Items}}
{{- if $i}},{{end}}
    {
      "finding_id": {{$it.FindingID}},
      "name": {{json $it.Name}},
      "vulnclass": {{json $it.VulnClass}},
      "severity": {{json $it.Severity}},
      "summary": {{json $it.Summary}},
      "assets": {{json $it.Assets}},
      "detail_url": {{json $it.DetailURL}}
    }
{{- end}}
  ]
}`

// webhookTemplateData is the context exposed to the user template.
type webhookTemplateData struct {
	Title   string
	Batch   bool
	Count   int
	Items   []webhookItem
	HomeURL string
	// SentAt is this delivery's time (RFC3339), for the receiver to record.
	SentAt string
}

type webhookItem struct {
	FindingID     int64
	Name          string
	VulnClass     string
	Severity      string
	SeverityLabel string
	Summary       string
	Assets        []string
	DetailURL     string
	FromStatus    string
	ToStatus      string
	// StatusLabel is the human-readable description of a status change, such as
	// "Pending → Fixed"; empty for non-status-change events.
	StatusLabel string
}

func validateWebhookConfig(cfg map[string]any) error {
	raw := cfgString(cfg, "url")
	if raw == "" {
		return errors.New("missing target URL")
	}
	if err := validateHTTPURL(raw); err != nil {
		return fmt.Errorf("invalid target URL: %w", err)
	}
	if m := strings.ToUpper(cfgString(cfg, "method")); m != "" && m != http.MethodGet && m != http.MethodPost && m != http.MethodPut && m != http.MethodPatch {
		return fmt.Errorf("unsupported method %s (allowed: GET/POST/PUT/PATCH)", m)
	}
	if tpl := cfgString(cfg, "body_template"); tpl != "" {
		if _, err := parseWebhookTemplate(tpl); err != nil {
			return fmt.Errorf("request body template syntax error: %w", err)
		}
	}
	return nil
}

func prepareWebhook(cfg map[string]any, m Message) (channelDelivery, error) {
	if err := validateWebhookConfig(cfg); err != nil {
		return channelDelivery{}, Permanent(err)
	}
	method := strings.ToUpper(cfgString(cfg, "method"))
	if method == "" {
		method = http.MethodPost
	}

	// GET carries no request body: stuffing the content into the query is beyond
	// the template's reach and does not fit GET semantics, so GET is suitable only
	// for "fire the hook on hit" receivers.
	var payload any
	if method != http.MethodGet {
		body, err := renderWebhookBody(cfgString(cfg, "body_template"), m)
		if err != nil {
			return channelDelivery{}, Permanent(err)
		}
		// The template renders a string form of JSON; convert it to json.RawMessage
		// to send as-is, avoiding a second round of escaping that would wrap the
		// user's carefully built structure inside a JSON string.
		if !json.Valid([]byte(body)) {
			return channelDelivery{}, Permanent(errors.New("the request body template did not render to valid JSON"))
		}
		payload = json.RawMessage(body)
	}

	headers := cfgMap(cfg, "headers")
	if ct := cfgString(cfg, "content_type"); ct != "" {
		// Allow overriding, but apply it after headers so explicit config takes
		// priority.
		if headers == nil {
			headers = map[string]string{}
		}
		headers["Content-Type"] = ct
	}
	return channelDelivery{method: method, endpoint: cfgString(cfg, "url"), headers: headers, payload: payload, kept: len(m.Items)}, nil
}

// renderWebhookBody renders the request body using the user template (or the
// default template).
func renderWebhookBody(tpl string, m Message) (string, error) {
	if strings.TrimSpace(tpl) == "" {
		tpl = webhookDefaultTemplate
	}
	t, err := parseWebhookTemplate(tpl)
	if err != nil {
		return "", fmt.Errorf("request body template syntax error: %w", err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, newWebhookTemplateData(m)); err != nil {
		return "", fmt.Errorf("failed to render request body template: %w", err)
	}
	return buf.String(), nil
}

// parseWebhookTemplate parses the template.
//
// missingkey=zero makes a missing map key render as the zero value rather than
// erroring — but the context in this file is a struct, so its main effect is to
// keep range from failing when .Items is empty. What really needs guarding is
// .Items being nil.
func parseWebhookTemplate(tpl string) (*template.Template, error) {
	return template.New("body").Funcs(webhookTemplateFuncs).Option("missingkey=zero").Parse(tpl)
}

// webhookTemplateFuncs are the helper functions exposed to the template.
var webhookTemplateFuncs = template.FuncMap{
	// json serializes any value to JSON.
	//
	// This function is not a nicety but a necessity: without it, the user can only
	// write {{.Title}} as direct interpolation, and the moment a vulnerability
	// title contains a quote or a newline the whole request body is no longer valid
	// JSON — the receiver rejects it, and the error points at "JSON parse failed",
	// with no hint that the real cause is a quote in the title.
	"json": func(v any) (string, error) {
		raw, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		return string(raw), nil
	},
	// jsons embeds a JSON fragment inside another JSON string value (applying one
	// layer of string escaping).
	"jsons": func(v any) (string, error) {
		raw, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		quoted, err := json.Marshal(string(raw))
		if err != nil {
			return "", err
		}
		// Strip the outer quotes: the caller decides whether to add quotes.
		return string(quoted[1 : len(quoted)-1]), nil
	},
}

func newWebhookTemplateData(m Message) webhookTemplateData {
	d := webhookTemplateData{
		Title:   markdownTitle(m),
		Batch:   m.Batch,
		Count:   len(m.Items),
		HomeURL: m.HomeURL,
		SentAt:  time.Now().Format(time.RFC3339),
		Items:   make([]webhookItem, 0, len(m.Items)),
	}
	for _, it := range m.Items {
		wi := webhookItem{
			FindingID:     it.FindingID,
			Name:          it.Name,
			VulnClass:     it.VulnClass,
			Severity:      it.Severity,
			SeverityLabel: SeverityLabel(it.Severity),
			Summary:       it.Summary,
			Assets:        append([]string{}, it.Assets...),
			DetailURL:     it.DetailURL,
			FromStatus:    it.FromStatus,
			ToStatus:      it.ToStatus,
		}
		if it.IsStatusChange() {
			wi.StatusLabel = StatusLabel(it.FromStatus) + " → " + StatusLabel(it.ToStatus)
		}
		d.Items = append(d.Items, wi)
	}
	return d
}
