package netboxtool

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// graphqlBodies returns a handler that inspects each incoming GraphQL
// request's query text for a top-level field name (e.g. "device_list") and
// responds with the matching canned JSON body - a fake covering all of
// device_list/interface_list/ip_address_list/virtual_machine_list/
// vm_interface_list at once, since getDevicesFlat/getVMsFlat fire all three
// of their queries concurrently against the same server.
func graphqlBodies(t *testing.T, bodies map[string]string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		var req struct {
			Query string `json:"query"`
		}
		require.NoError(t, json.Unmarshal(raw, &req))

		for field, body := range bodies {
			if strings.Contains(req.Query, field) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(body))
				return
			}
		}
		t.Fatalf("unrecognized graphql query: %s", req.Query)
	}
}

func TestGetDevices_FlatFetchStitch(t *testing.T) {
	nb := newTestClient(t, graphqlBodies(t, map[string]string{
		"device_list": `{"data": {"device_list": [
			{"id": "1", "name": "sw1", "status": "active"},
			{"id": "2", "name": "sw2", "status": "active"}
		]}}`,
		"interface_list": `{"data": {"interface_list": [
			{"id": "10", "name": "eth0", "device": {"id": "1"}},
			{"id": "11", "name": "eth1", "device": {"id": "2"}}
		]}}`,
		"ip_address_list": `{"data": {"ip_address_list": [
			{"id": "100", "address": "10.0.0.1/24", "role": "", "assigned_object": {"__typename": "InterfaceType", "id": "10"}},
			{"id": "101", "address": "10.0.0.2/24", "role": "", "assigned_object": {"__typename": "VMInterfaceType", "id": "10"}},
			{"id": "102", "address": "10.0.0.3/24", "role": "", "assigned_object": null}
		]}}`,
	}))

	devices, err := nb.GetDevices()
	require.NoError(t, err)
	require.Len(t, devices, 2)

	byName := make(map[string]*NBDevice, len(devices))
	for _, d := range devices {
		byName[d.Name] = d
	}

	sw1 := byName["sw1"]
	require.NotNil(t, sw1)
	require.Len(t, sw1.Interfaces, 1)
	assert.Equal(t, "eth0", sw1.Interfaces[0].Name)
	// Only the InterfaceType-typed address (id 100) should attach - the
	// VMInterfaceType one sharing the same numeric id (101) and the
	// unassigned one (102) must not.
	require.Len(t, sw1.Interfaces[0].Addresses, 1)
	assert.Equal(t, "10.0.0.1/24", sw1.Interfaces[0].Addresses[0].Address)

	sw2 := byName["sw2"]
	require.NotNil(t, sw2)
	require.Len(t, sw2.Interfaces, 1)
	assert.Equal(t, "eth1", sw2.Interfaces[0].Name)
	assert.Empty(t, sw2.Interfaces[0].Addresses)
}

func TestGetVMs_FlatFetchStitch(t *testing.T) {
	nb := newTestClient(t, graphqlBodies(t, map[string]string{
		"virtual_machine_list": `{"data": {"virtual_machine_list": [
			{"id": "1", "name": "vm1", "status": "active"}
		]}}`,
		"vm_interface_list": `{"data": {"vm_interface_list": [
			{"id": "10", "name": "eth0", "virtual_machine": {"id": "1"}}
		]}}`,
		"ip_address_list": `{"data": {"ip_address_list": [
			{"id": "100", "address": "10.0.0.1/24", "role": "", "assigned_object": {"__typename": "VMInterfaceType", "id": "10"}},
			{"id": "101", "address": "10.0.0.2/24", "role": "", "assigned_object": {"__typename": "InterfaceType", "id": "10"}}
		]}}`,
	}))

	vms, err := nb.GetVMs()
	require.NoError(t, err)
	require.Len(t, vms, 1)
	require.Len(t, vms[0].Interfaces, 1)
	assert.True(t, vms[0].VM)
	require.Len(t, vms[0].Interfaces[0].Addresses, 1)
	assert.Equal(t, "10.0.0.1/24", vms[0].Interfaces[0].Addresses[0].Address)
}

func TestStitchDevices_SkipsInterfaceWithoutDevice(t *testing.T) {
	devices := []JSONDevice{{ID: 1, Name: "sw1"}}
	interfaces := []flatInterfaceRow{
		{JSONInterface: JSONInterface{ID: 10, Name: "eth0"}, Device: nil},
	}
	out := stitchDevices(devices, interfaces, nil)
	require.Len(t, out, 1)
	assert.Empty(t, out[0].Interfaces)
}

func TestStitchVMs_SkipsInterfaceWithoutVM(t *testing.T) {
	vms := []JSONDevice{{ID: 1, Name: "vm1"}}
	interfaces := []flatVMInterfaceRow{
		{ID: 10, Name: "eth0", VirtualMachine: nil},
	}
	out := stitchVMs(vms, interfaces, nil)
	require.Len(t, out, 1)
	assert.Empty(t, out[0].Interfaces)
}
