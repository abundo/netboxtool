package netboxtool

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestClient points a NetboxClient at a fake Netbox server, for tests
// that only need to assert on the request made / stub the response
// returned, not exercise real HTTP transport concerns.
func newTestClient(t *testing.T, handler http.HandlerFunc) *NetboxClient {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	nb, err := NewNetboxClient(ConfigNetbox{URL: srv.URL, Token: "test-token"})
	require.NoError(t, err)
	return nb
}

func TestGetTenant_Found(t *testing.T) {
	nb := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/tenancy/tenants/", r.URL.Path)
		assert.Equal(t, "factum", r.URL.Query().Get("cf_source"))
		assert.Equal(t, "42", r.URL.Query().Get("cf_source_id"))
		assert.Equal(t, "Token test-token", r.Header.Get("Authorization"))

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"results": [
				{
					"id": 7,
					"name": "Acme",
					"slug": "acme",
					"custom_fields": {"source": "factum", "source_id": "42"}
				}
			]
		}`))
	})

	tenant, err := nb.GetTenant("factum", "42")
	require.NoError(t, err)
	require.NotNil(t, tenant)
	assert.Equal(t, uint(7), tenant.NetboxID)
	assert.Equal(t, "Acme", tenant.Name)
	assert.Equal(t, "acme", tenant.Slug)
	assert.Equal(t, "factum", tenant.CfSource)
	assert.Equal(t, "42", tenant.CfSourceID)
}

func TestGetTenant_NotFound(t *testing.T) {
	nb := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results": []}`))
	})

	tenant, err := nb.GetTenant("factum", "999")
	require.NoError(t, err)
	assert.Nil(t, tenant)
}

func TestGetTenant_ServerError(t *testing.T) {
	nb := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`boom`))
	})

	tenant, err := nb.GetTenant("factum", "1")
	require.Error(t, err)
	assert.Nil(t, tenant)
}

func TestNetboxCustomFields_UnknownKeys(t *testing.T) {
	var cf NetboxCustomFields
	require.NoError(t, json.Unmarshal([]byte(`{
		"parents": "core1",
		"alarm_destination": "noc",
		"source_ref": 42
	}`), &cf))
	assert.Equal(t, "core1", cf.Parents)
	assert.Equal(t, "noc", cf.AlarmDestination)
	require.NotNil(t, cf.All)
	assert.Equal(t, float64(42), cf.All["source_ref"])
	assert.Equal(t, "core1", cf.All["parents"])
}

func TestCreateDevice(t *testing.T) {
	nb := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/api/dcim/devices/", r.URL.Path)
		assert.Equal(t, "Token test-token", r.Header.Get("Authorization"))

		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, "edge1", body["name"])
		assert.Equal(t, float64(1), body["site"])
		assert.Equal(t, float64(2), body["device_type"])
		cf, ok := body["custom_fields"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "inventory", cf["source"])

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id": 99, "name": "edge1"}`))
	})

	created, err := nb.CreateDevice("edge1", map[string]any{
		"site":        1,
		"device_type": 2,
		"custom_fields": map[string]any{
			"source": "inventory",
		},
	})
	require.NoError(t, err)
	require.NotNil(t, created)
	assert.Equal(t, uint(99), created.ID)
	assert.Equal(t, "edge1", created.Name)
}

func TestGetCustomField_Found(t *testing.T) {
	nb := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/extras/custom-fields/", r.URL.Path)
		assert.Equal(t, "source_id", r.URL.Query().Get("name"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"results": [{
				"id": 3,
				"name": "source_id",
				"type": {"value": "integer", "label": "Integer"},
				"object_types": ["dcim.device", "dcim.interface"]
			}]
		}`))
	})

	cf, err := nb.GetCustomField("source_id")
	require.NoError(t, err)
	require.NotNil(t, cf)
	assert.Equal(t, uint(3), cf.NetboxID)
	assert.Equal(t, "source_id", cf.Name)
	assert.Equal(t, "integer", cf.Type)
	assert.True(t, cf.AssignedTo("dcim.device"))
	assert.False(t, cf.AssignedTo("ipam.ipaddress"))
}

func TestGetCustomField_NotFound(t *testing.T) {
	nb := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	})

	cf, err := nb.GetCustomField("missing")
	require.NoError(t, err)
	assert.Nil(t, cf)
}

func TestRequireCustomField(t *testing.T) {
	nb := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"results": [{
				"id": 1,
				"name": "source_id",
				"type": "integer",
				"object_types": ["dcim.device"]
			}]
		}`))
	})

	require.NoError(t, nb.RequireCustomField("source_id", "dcim.device"))
	err := nb.RequireCustomField("source_id", "dcim.device", "dcim.interface")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dcim.interface")
}

func TestGetSiteByName_Found(t *testing.T) {
	nb := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/dcim/sites/", r.URL.Path)
		assert.Equal(t, "Default", r.URL.Query().Get("name"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"id":1,"name":"Default","slug":"default"}]}`))
	})

	site, err := nb.GetSiteByName("Default")
	require.NoError(t, err)
	require.NotNil(t, site)
	assert.Equal(t, uint(1), site.ID)
	assert.Equal(t, "Default", site.Name)
	assert.Equal(t, "default", site.Slug)
}

func TestGetSiteByName_NotFound(t *testing.T) {
	nb := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	})

	site, err := nb.GetSiteByName("missing")
	require.NoError(t, err)
	assert.Nil(t, site)
}

func TestEnsureTag_Existing(t *testing.T) {
	nb := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/api/extras/tags/", r.URL.Path)
		assert.Equal(t, "sync-source", r.URL.Query().Get("slug"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"id":5,"name":"Sync source","slug":"sync-source"}]}`))
	})

	tag, err := nb.EnsureTag("Sync source", "sync-source")
	require.NoError(t, err)
	require.NotNil(t, tag)
	assert.Equal(t, uint(5), tag.ID)
	assert.Equal(t, "sync-source", tag.Slug)
}

func TestEnsureTag_Creates(t *testing.T) {
	var sawPost bool
	nb := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"results":[]}`))
		case http.MethodPost:
			sawPost = true
			assert.Equal(t, "/api/extras/tags/", r.URL.Path)
			var body map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			assert.Equal(t, "Sync source", body["name"])
			assert.Equal(t, "sync-source", body["slug"])
			_, _ = w.Write([]byte(`{"id":8,"name":"Sync source","slug":"sync-source"}`))
		default:
			t.Fatalf("unexpected method %s", r.Method)
		}
	})

	tag, err := nb.EnsureTag("Sync source", "sync-source")
	require.NoError(t, err)
	require.NotNil(t, tag)
	assert.True(t, sawPost)
	assert.Equal(t, uint(8), tag.ID)
	assert.Equal(t, "Sync source", tag.Name)
}
