package netboxtool

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// NBWebhook is extras.Webhook — the destination half of Netbox 3.7+'s
// webhook/event-rule split. Object types and events live on NBEventRule.
type NBWebhook struct {
	NetboxID        uint
	Name            string
	PayloadURL      string
	HTTPMethod      string
	HTTPContentType string
	BodyTemplate    string
}

// NBEventRule is extras.EventRule — binds object types and event types to
// an action (typically a webhook). ActionObjectID is the extras.Webhook id
// when ActionType is "webhook".
type NBEventRule struct {
	NetboxID       uint
	Name           string
	Enabled        bool
	ObjectTypes    []string
	EventTypes     []string
	ActionType     string
	ActionObjectID uint
	// Conditions is the optional JSON condition object; nil/empty means
	// the rule fires for every matching event.
	Conditions any
}

// netboxChoiceString accepts either a bare JSON string or Netbox's
// {value,label} choice object (action_type, http_method, ...).
type netboxChoiceString string

func (s *netboxChoiceString) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		var v string
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		*s = netboxChoiceString(v)
		return nil
	}
	var c struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return err
	}
	*s = netboxChoiceString(c.Value)
	return nil
}

type restWebhook struct {
	ID              uint               `json:"id"`
	Name            string             `json:"name"`
	PayloadURL      string             `json:"payload_url"`
	HTTPMethod      netboxChoiceString `json:"http_method"`
	HTTPContentType string             `json:"http_content_type"`
	BodyTemplate    string             `json:"body_template"`
}

func (w restWebhook) toNB() *NBWebhook {
	method := string(w.HTTPMethod)
	if method == "" {
		method = "POST"
	}
	return &NBWebhook{
		NetboxID:        w.ID,
		Name:            w.Name,
		PayloadURL:      w.PayloadURL,
		HTTPMethod:      method,
		HTTPContentType: w.HTTPContentType,
		BodyTemplate:    w.BodyTemplate,
	}
}

type restEventRule struct {
	ID             uint               `json:"id"`
	Name           string             `json:"name"`
	Enabled        bool               `json:"enabled"`
	ObjectTypes    []string           `json:"object_types"`
	EventTypes     []string           `json:"event_types"`
	ActionType     netboxChoiceString `json:"action_type"`
	ActionObjectID uint               `json:"action_object_id"`
	Conditions     any                `json:"conditions"`
}

func (r restEventRule) toNB() *NBEventRule {
	return &NBEventRule{
		NetboxID:       r.ID,
		Name:           r.Name,
		Enabled:        r.Enabled,
		ObjectTypes:    r.ObjectTypes,
		EventTypes:     r.EventTypes,
		ActionType:     string(r.ActionType),
		ActionObjectID: r.ActionObjectID,
		Conditions:     r.Conditions,
	}
}

type restListPage[T any] struct {
	Next    *string `json:"next"`
	Results []T     `json:"results"`
}

// restListAll walks a paginated extras/dcim REST list endpoint and returns
// every result. listPath is the path without a query (e.g. "/api/extras/webhooks/").
func restListAll[T any](nb *NetboxClient, listPath string) ([]T, error) {
	var all []T
	endpoint := listPath + "?limit=" + strconv.Itoa(restPageSize)
	for endpoint != "" {
		var page restListPage[T]
		if err := nb.restGet(endpoint, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Results...)
		if page.Next == nil {
			break
		}
		next, err := url.Parse(*page.Next)
		if err != nil {
			return nil, fmt.Errorf("netbox %s: parse next page url: %w", listPath, err)
		}
		endpoint = listPath + "?" + next.RawQuery
	}
	return all, nil
}

// GetWebhooks fetches every extras.Webhook via the REST API.
func (nb *NetboxClient) GetWebhooks() ([]*NBWebhook, error) {
	rows, err := restListAll[restWebhook](nb, "/api/extras/webhooks/")
	if err != nil {
		return nil, err
	}
	out := make([]*NBWebhook, 0, len(rows))
	for _, w := range rows {
		out = append(out, w.toNB())
	}
	return out, nil
}

// GetEventRules fetches every extras.EventRule via the REST API.
func (nb *NetboxClient) GetEventRules() ([]*NBEventRule, error) {
	rows, err := restListAll[restEventRule](nb, "/api/extras/event-rules/")
	if err != nil {
		return nil, err
	}
	out := make([]*NBEventRule, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toNB())
	}
	return out, nil
}

// HasEvent reports whether the rule lists eventType (e.g. "object_deleted").
func (r *NBEventRule) HasEvent(eventType string) bool {
	if r == nil {
		return false
	}
	for _, e := range r.EventTypes {
		if e == eventType {
			return true
		}
	}
	return false
}

// HasObjectType reports whether the rule lists objectType (e.g. "dcim.device").
func (r *NBEventRule) HasObjectType(objectType string) bool {
	if r == nil {
		return false
	}
	for _, t := range r.ObjectTypes {
		if t == objectType {
			return true
		}
	}
	return false
}

// HasConditions reports whether the rule has a non-empty condition object
// that would restrict which events actually fire.
func (r *NBEventRule) HasConditions() bool {
	if r == nil || r.Conditions == nil {
		return false
	}
	switch v := r.Conditions.(type) {
	case map[string]any:
		return len(v) > 0
	case []any:
		return len(v) > 0
	case string:
		s := strings.TrimSpace(v)
		return s != "" && s != "null" && s != "{}"
	default:
		return true
	}
}
