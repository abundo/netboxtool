package netboxtool

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetWebhooks(t *testing.T) {
	nb := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/extras/webhooks/", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"next": null,
			"results": [{
				"id": 3,
				"name": "factum-sync",
				"payload_url": "https://factum.example.com/api/netbox-webhook",
				"http_method": {"value": "POST", "label": "POST"},
				"http_content_type": "application/json",
				"body_template": ""
			}]
		}`))
	})

	hooks, err := nb.GetWebhooks()
	require.NoError(t, err)
	require.Len(t, hooks, 1)
	assert.Equal(t, uint(3), hooks[0].NetboxID)
	assert.Equal(t, "factum-sync", hooks[0].Name)
	assert.Equal(t, "https://factum.example.com/api/netbox-webhook", hooks[0].PayloadURL)
	assert.Equal(t, "POST", hooks[0].HTTPMethod)
	assert.Equal(t, "application/json", hooks[0].HTTPContentType)
	assert.Equal(t, "", hooks[0].BodyTemplate)
}

func TestGetEventRules(t *testing.T) {
	nb := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/extras/event-rules/", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"next": null,
			"results": [{
				"id": 7,
				"name": "factum",
				"enabled": true,
				"object_types": ["dcim.device", "dcim.interface"],
				"event_types": ["object_created", "object_updated", "object_deleted"],
				"action_type": {"value": "webhook", "label": "Webhook"},
				"action_object_id": 3,
				"conditions": null
			}]
		}`))
	})

	rules, err := nb.GetEventRules()
	require.NoError(t, err)
	require.Len(t, rules, 1)
	assert.Equal(t, uint(7), rules[0].NetboxID)
	assert.Equal(t, "factum", rules[0].Name)
	assert.True(t, rules[0].Enabled)
	assert.Equal(t, "webhook", rules[0].ActionType)
	assert.Equal(t, uint(3), rules[0].ActionObjectID)
	assert.True(t, rules[0].HasObjectType("dcim.device"))
	assert.True(t, rules[0].HasEvent("object_deleted"))
	assert.False(t, rules[0].HasConditions())
}

func TestNetboxChoiceString_BareAndObject(t *testing.T) {
	var a, b netboxChoiceString
	require.NoError(t, json.Unmarshal([]byte(`"POST"`), &a))
	require.NoError(t, json.Unmarshal([]byte(`{"value":"webhook","label":"Webhook"}`), &b))
	assert.Equal(t, netboxChoiceString("POST"), a)
	assert.Equal(t, netboxChoiceString("webhook"), b)
}

func TestNBEventRule_HasConditions(t *testing.T) {
	assert.False(t, (&NBEventRule{}).HasConditions())
	assert.False(t, (&NBEventRule{Conditions: map[string]any{}}).HasConditions())
	assert.True(t, (&NBEventRule{Conditions: map[string]any{"and": []any{}}}).HasConditions())
}
