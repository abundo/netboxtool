package netboxtool

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetCustomField_ParsesMetadata(t *testing.T) {
	nb := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/extras/custom-fields/", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"results": [{
				"id": 3,
				"name": "becs_oid",
				"type": {"value": "integer", "label": "Integer"},
				"label": "",
				"description": "identifies the element in becs",
				"required": false,
				"group_name": "sync",
				"object_types": ["dcim.device"],
				"choice_set": null
			}]
		}`))
	})
	cf, err := nb.GetCustomField("becs_oid")
	require.NoError(t, err)
	require.NotNil(t, cf)
	assert.Equal(t, "integer", cf.Type)
	assert.Equal(t, "sync", cf.GroupName)
	assert.Equal(t, "identifies the element in becs", cf.Description)
	assert.False(t, cf.Required)
}

func TestCreateCustomField(t *testing.T) {
	nb := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/api/extras/custom-fields/", r.URL.Path)
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, "location", body["name"])
		assert.Equal(t, "text", body["type"])
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":11,"name":"location","type":"text","object_types":["dcim.device"]}`))
	})
	cf, err := nb.CreateCustomField(CustomFieldWrite{
		Name: "location", Type: CFTypeText, ObjectTypes: []string{"dcim.device"},
	})
	require.NoError(t, err)
	assert.Equal(t, uint(11), cf.NetboxID)
}

func TestEnsureCustomFieldChoiceSet_Creates(t *testing.T) {
	var posts int
	nb := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"results":[]}`))
			return
		}
		posts++
		assert.Equal(t, "/api/extras/custom-field-choice-sets/", r.URL.Path)
		body, _ := io.ReadAll(r.Body)
		assert.Contains(t, string(body), `"ssh"`)
		_, _ = w.Write([]byte(`{
			"id": 5,
			"name": "factum_connection_method",
			"extra_choices": [["ssh","ssh"],["telnet","telnet"]]
		}`))
	})
	set, err := nb.EnsureCustomFieldChoiceSet("factum_connection_method", [][2]string{{"ssh", "ssh"}, {"telnet", "telnet"}})
	require.NoError(t, err)
	require.Equal(t, 1, posts)
	assert.Equal(t, uint(5), set.NetboxID)
	assert.Len(t, set.ExtraChoices, 2)
}

func TestEnsureCustomFieldChoiceSet_Merges(t *testing.T) {
	var patched bool
	nb := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{
				"results": [{
					"id": 5,
					"name": "factum_connection_method",
					"extra_choices": [["ssh","ssh"]]
				}]
			}`))
			return
		}
		assert.Equal(t, http.MethodPatch, r.Method)
		patched = true
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		choices, _ := body["extra_choices"].([]any)
		assert.Len(t, choices, 2)
		_, _ = w.Write([]byte(`{
			"id": 5,
			"name": "factum_connection_method",
			"extra_choices": [["ssh","ssh"],["telnet","telnet"]]
		}`))
	})
	set, err := nb.EnsureCustomFieldChoiceSet("factum_connection_method", [][2]string{{"ssh", "ssh"}, {"telnet", "telnet"}})
	require.NoError(t, err)
	assert.True(t, patched)
	assert.Len(t, set.ExtraChoices, 2)
}

func TestMergeChoices_DoesNotDropExisting(t *testing.T) {
	merged, added := mergeChoices([][2]string{{"ssh", "SSH"}, {"console", "console"}}, [][2]string{{"ssh", "ssh"}, {"telnet", "telnet"}})
	assert.True(t, added)
	assert.Equal(t, [][2]string{{"ssh", "SSH"}, {"console", "console"}, {"telnet", "telnet"}}, merged)
}
