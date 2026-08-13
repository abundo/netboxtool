package netboxtool

import (
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
