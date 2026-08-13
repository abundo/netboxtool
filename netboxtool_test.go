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

func TestFlexUint_Unmarshal(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    FlexUint
		wantErr bool
	}{
		{name: "number", in: `123`, want: 123},
		{name: "string", in: `"456"`, want: 456},
		{name: "null", in: `null`, want: 0},
		{name: "empty string", in: `""`, want: 0},
		{name: "invalid", in: `"nope"`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var v FlexUint
			err := v.UnmarshalJSON([]byte(tt.in))
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, v)
		})
	}
}

func TestCreateDevice(t *testing.T) {
	nb := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/api/dcim/devices/", r.URL.Path)
		assert.Equal(t, "Token test-token", r.Header.Get("Authorization"))

		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, "test-lab2", body["name"])
		assert.Equal(t, float64(1), body["site"])
		assert.Equal(t, float64(2), body["device_type"])
		cf, ok := body["custom_fields"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, float64(3641645), cf["becs_oid"])

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id": 99, "name": "test-lab2"}`))
	})

	created, err := nb.CreateDevice("test-lab2", map[string]any{
		"site":        1,
		"device_type": 2,
		"custom_fields": map[string]any{
			"becs_oid": 3641645,
		},
	})
	require.NoError(t, err)
	require.NotNil(t, created)
	assert.Equal(t, uint(99), created.ID)
	assert.Equal(t, "test-lab2", created.Name)
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
		assert.Equal(t, "becs", r.URL.Query().Get("slug"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"id":5,"name":"BECS","slug":"becs"}]}`))
	})

	tag, err := nb.EnsureTag("BECS", "becs")
	require.NoError(t, err)
	require.NotNil(t, tag)
	assert.Equal(t, uint(5), tag.ID)
	assert.Equal(t, "becs", tag.Slug)
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
			assert.Equal(t, "BECS", body["name"])
			assert.Equal(t, "becs", body["slug"])
			_, _ = w.Write([]byte(`{"id":8,"name":"BECS","slug":"becs"}`))
		default:
			t.Fatalf("unexpected method %s", r.Method)
		}
	})

	tag, err := nb.EnsureTag("BECS", "becs")
	require.NoError(t, err)
	require.NotNil(t, tag)
	assert.True(t, sawPost)
	assert.Equal(t, uint(8), tag.ID)
	assert.Equal(t, "BECS", tag.Name)
}
