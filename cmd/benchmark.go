package main

//
// Benchmark comparing three ways of fetching devices/interfaces/addresses
// from Netbox, to find out where factum2's full sync actually spends its
// time:
//
//   A) current production path: one deeply nested GraphQL query per page
//      (device_list { interfaces { ip_addresses { ... } } } }, via
//      nb.GetDevices()/nb.GetVMs()).
//
//   B) flat-ish GraphQL: device_list and interface_list as two separate
//      top-level queries (device_list no longer nests interfaces at all;
//      interface_list still nests ip_addresses one level down, since
//      Netbox's GraphQL exposes ip_address_list's device/VM link only via
//      a polymorphic "assigned_object" union that would need per-type
//      inline fragments to select - not worth the fragility for a
//      benchmark), stitched together in Go by device id.
//
//   C) flat REST: three separately paginated REST list endpoints
//      (/api/dcim/devices/, /api/dcim/interfaces/, /api/ipam/ip-addresses/),
//      none of which nest one-to-many relations at all - Netbox's REST
//      serializers never embed a reverse relation, only brief
//      representations of *-to-one fields (device_type, role, site, ...).
//      Addresses are matched back to their interface via the flat
//      assigned_object_id/assigned_object_type fields. This is the REST
//      equivalent of the "dump each table with no relations, stitch
//      client-side" approach.
//
// Only physical devices/interfaces are covered (not virtual_machine_list/
// vm_interface_list) - devices are the bulk of most Netbox inventories and
// the point here is comparing fetch strategies, not full feature parity.
//
// Run: netboxtool benchmark -c <config.yaml>
//

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/abundo/netboxtool"
)

// ---------------------------------------------------------------------------
// Approach B: flat-ish GraphQL, two top-level queries
// ---------------------------------------------------------------------------

const flatDeviceListGraphQLBody = `
{
    device_list{{.filter}} {
        id
        name
        status
        device_type {
            id
            model
            manufacturer { id name }
        }
        role { id name }
        site { id name }
        platform { id name }
        primary_ip4 { id address }
    }
}
`

const flatInterfaceListGraphQLBody = `
{
    interface_list{{.filter}} {
        id
        name
        description
        type
        mode
        label
        device { id }
        ip_addresses {
            id
            address
            role
        }
    }
}
`

// flatInterfaceListNoAddrGraphQLBody is flatInterfaceListGraphQLBody minus
// the nested ip_addresses selection, to isolate how much of that query's
// cost is the interface rows themselves vs. resolving their addresses -
// see fetchFlatInterfacesNoAddrGraphQL/approach D.
const flatInterfaceListNoAddrGraphQLBody = `
{
    interface_list{{.filter}} {
        id
        name
        description
        type
        mode
        label
        device { id }
    }
}
`

// flatIPAddressListGraphQLBody is a genuinely flat, top-level GraphQL query
// for addresses - unlike flatInterfaceListGraphQLBody's nested ip_addresses,
// this resolves each address's owner via assigned_object, a polymorphic
// union (IPAddressAssignmentType, confirmed via the introspect-ip-address
// command) that needs an inline fragment per concrete type it can be. Only
// InterfaceType and VMInterfaceType are selected - FHRPGroupType (the
// union's third member, virtual HSRP/VRRP addresses not tied to a real
// interface) is out of scope, matching the rest of this benchmark's
// devices/interfaces/addresses focus. __typename tells the two apart in Go.
const flatIPAddressListGraphQLBody = `
{
    ip_address_list{{.filter}} {
        id
        address
        role
        assigned_object {
            __typename
            ... on InterfaceType {
                id
            }
            ... on VMInterfaceType {
                id
            }
        }
    }
}
`

type gqlDevice struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	DeviceType *struct {
		ID           string `json:"id"`
		Model        string `json:"model"`
		Manufacturer struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"manufacturer"`
	} `json:"device_type"`
	Role *struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"role"`
	Site *struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"site"`
	Platform *struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"platform"`
	PrimaryIP4 *struct {
		ID      string `json:"id"`
		Address string `json:"address"`
	} `json:"primary_ip4"`
}

type gqlDeviceListPage struct {
	Data struct {
		Devices []gqlDevice `json:"device_list"`
	} `json:"data"`
}

type gqlInterface struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Type        string `json:"type"`
	Mode        string `json:"mode"`
	Label       string `json:"label"`
	Device      *struct {
		ID string `json:"id"`
	} `json:"device"`
	IPAddresses []struct {
		ID      string `json:"id"`
		Address string `json:"address"`
		Role    string `json:"role"`
	} `json:"ip_addresses"`
}

type gqlInterfaceListPage struct {
	Data struct {
		Interfaces []gqlInterface `json:"interface_list"`
	} `json:"data"`
}

type gqlAddress struct {
	ID             string `json:"id"`
	Address        string `json:"address"`
	Role           string `json:"role"`
	AssignedObject *struct {
		Typename string `json:"__typename"`
		ID       string `json:"id"`
	} `json:"assigned_object"`
}

type gqlAddressListPage struct {
	Data struct {
		Addresses []gqlAddress `json:"ip_address_list"`
	} `json:"data"`
}

// fetchAllGraphQL drives nb.NetboxAPICall's id-cursor pagination for an
// arbitrary top-level "_list" query, calling unmarshalPage on each raw page
// until it reports fewer than pageSize rows. Returns the number of pages
// (i.e. round trips) it took, for comparing against restGetPaginated's page
// count - GraphQL's pagination args aren't necessarily subject to the same
// server-side page-size cap REST's is.
func fetchAllGraphQL(nb *netboxtool.NetboxClient, query string, pageSize int, unmarshalPage func([]byte) (count int, lastID uint, err error)) (int, error) {
	start := 0
	pages := 0
	for {
		respData, err := nb.NetboxAPICall(query, "", -1, start, pageSize)
		if err != nil {
			return pages, err
		}
		pages++
		count, lastID, err := unmarshalPage(respData)
		if err != nil {
			return pages, err
		}
		if count < pageSize {
			return pages, nil
		}
		start = int(lastID) + 1
	}
}

func fetchFlatDevicesGraphQL(nb *netboxtool.NetboxClient) ([]gqlDevice, int, error) {
	var out []gqlDevice
	pages, err := fetchAllGraphQL(nb, flatDeviceListGraphQLBody, 1000, func(respData []byte) (int, uint, error) {
		var page gqlDeviceListPage
		if err := json.Unmarshal(respData, &page); err != nil {
			return 0, 0, err
		}
		out = append(out, page.Data.Devices...)
		var lastID uint
		if n := len(page.Data.Devices); n > 0 {
			id, _ := strconv.ParseUint(page.Data.Devices[n-1].ID, 10, 64)
			lastID = uint(id)
		}
		return len(page.Data.Devices), lastID, nil
	})
	return out, pages, err
}

func fetchFlatInterfacesGraphQL(nb *netboxtool.NetboxClient) ([]gqlInterface, int, error) {
	var out []gqlInterface
	pages, err := fetchAllGraphQL(nb, flatInterfaceListGraphQLBody, 1000, func(respData []byte) (int, uint, error) {
		var page gqlInterfaceListPage
		if err := json.Unmarshal(respData, &page); err != nil {
			return 0, 0, err
		}
		out = append(out, page.Data.Interfaces...)
		var lastID uint
		if n := len(page.Data.Interfaces); n > 0 {
			id, _ := strconv.ParseUint(page.Data.Interfaces[n-1].ID, 10, 64)
			lastID = uint(id)
		}
		return len(page.Data.Interfaces), lastID, nil
	})
	return out, pages, err
}

// fetchFlatInterfacesNoAddrGraphQL is fetchFlatInterfacesGraphQL without the
// nested ip_addresses selection - used by approach D to isolate how much of
// interface_list's cost is the interface rows themselves vs. resolving
// their addresses server-side.
func fetchFlatInterfacesNoAddrGraphQL(nb *netboxtool.NetboxClient) ([]gqlInterface, int, error) {
	var out []gqlInterface
	pages, err := fetchAllGraphQL(nb, flatInterfaceListNoAddrGraphQLBody, 1000, func(respData []byte) (int, uint, error) {
		var page gqlInterfaceListPage
		if err := json.Unmarshal(respData, &page); err != nil {
			return 0, 0, err
		}
		out = append(out, page.Data.Interfaces...)
		var lastID uint
		if n := len(page.Data.Interfaces); n > 0 {
			id, _ := strconv.ParseUint(page.Data.Interfaces[n-1].ID, 10, 64)
			lastID = uint(id)
		}
		return len(page.Data.Interfaces), lastID, nil
	})
	return out, pages, err
}

// fetchFlatAddressesGraphQL fetches every ip_address_list row via GraphQL,
// resolving assigned_object through flatIPAddressListGraphQLBody's inline
// fragments - the fully-flat GraphQL equivalent of fetchFlatAddressesREST,
// used by approach F/G to see whether GraphQL's per-row address
// serialization scales better than REST's did (REST's ballooned badly at
// the 1400-device scale - see approach C/D's addresses phase).
func fetchFlatAddressesGraphQL(nb *netboxtool.NetboxClient) ([]gqlAddress, int, error) {
	var out []gqlAddress
	pages, err := fetchAllGraphQL(nb, flatIPAddressListGraphQLBody, 1000, func(respData []byte) (int, uint, error) {
		var page gqlAddressListPage
		if err := json.Unmarshal(respData, &page); err != nil {
			return 0, 0, err
		}
		out = append(out, page.Data.Addresses...)
		var lastID uint
		if n := len(page.Data.Addresses); n > 0 {
			id, _ := strconv.ParseUint(page.Data.Addresses[n-1].ID, 10, 64)
			lastID = uint(id)
		}
		return len(page.Data.Addresses), lastID, nil
	})
	return out, pages, err
}

// ---------------------------------------------------------------------------
// Approach C: flat REST, three separately paginated list endpoints
// ---------------------------------------------------------------------------

type restDevice struct {
	ID     uint   `json:"id"`
	Name   string `json:"name"`
	Status struct {
		Value string `json:"value"`
	} `json:"status"`
	DeviceType *struct {
		ID           uint   `json:"id"`
		Model        string `json:"model"`
		Manufacturer struct {
			ID   uint   `json:"id"`
			Name string `json:"name"`
		} `json:"manufacturer"`
	} `json:"device_type"`
	Role *struct {
		ID   uint   `json:"id"`
		Name string `json:"name"`
	} `json:"role"`
	Site *struct {
		ID   uint   `json:"id"`
		Name string `json:"name"`
	} `json:"site"`
	Platform *struct {
		ID   uint   `json:"id"`
		Name string `json:"name"`
	} `json:"platform"`
	PrimaryIP4 *struct {
		ID      uint   `json:"id"`
		Address string `json:"address"`
	} `json:"primary_ip4"`
}

type restDeviceListPage struct {
	Next    *string      `json:"next"`
	Results []restDevice `json:"results"`
}

type restInterface struct {
	ID          uint   `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Label       string `json:"label"`
	Type        struct {
		Value string `json:"value"`
	} `json:"type"`
	Mode *struct {
		Value string `json:"value"`
	} `json:"mode"`
	Device struct {
		ID uint `json:"id"`
	} `json:"device"`
}

type restInterfaceListPage struct {
	Next    *string         `json:"next"`
	Results []restInterface `json:"results"`
}

type restAddress struct {
	ID      uint   `json:"id"`
	Address string `json:"address"`
	Role    *struct {
		Value string `json:"value"`
	} `json:"role"`
	AssignedObjectID   *uint  `json:"assigned_object_id"`
	AssignedObjectType string `json:"assigned_object_type"`
}

type restAddressListPage struct {
	Next    *string       `json:"next"`
	Results []restAddress `json:"results"`
}

// restGetPaginated fetches every page of a Netbox REST list endpoint,
// following "next" until it's null, decoding each page's body with decode
// and handing back the page's own "next" url and row count. Returns the
// number of pages (round trips) it took - if the server's MAX_PAGE_SIZE
// setting caps below the ?limit= requested here, this will be far higher
// than len(results)/1000, which is exactly the kind of thing that would
// make REST pagination much more expensive than GraphQL's.
func restGetPaginated(cfg netboxtool.ConfigNetbox, client *http.Client, endpoint string, decode func([]byte) (next *string, count int, err error)) (int, error) {
	url := cfg.URL + endpoint
	pages := 0
	for url != "" {
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return pages, err
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Authorization", "Token "+cfg.Token)

		resp, err := client.Do(req)
		if err != nil {
			return pages, fmt.Errorf("netbox GET %s: %w", url, err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return pages, err
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return pages, fmt.Errorf("netbox GET %s failed: %s: %s", url, resp.Status, string(body))
		}
		pages++

		next, _, err := decode(body)
		if err != nil {
			return pages, err
		}
		if next == nil {
			return pages, nil
		}
		url = *next
	}
	return pages, nil
}

func fetchFlatDevicesREST(cfg netboxtool.ConfigNetbox, client *http.Client) ([]restDevice, int, error) {
	var out []restDevice
	pages, err := restGetPaginated(cfg, client, "/api/dcim/devices/?limit=1000", func(body []byte) (*string, int, error) {
		var page restDeviceListPage
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, 0, err
		}
		out = append(out, page.Results...)
		return page.Next, len(page.Results), nil
	})
	return out, pages, err
}

func fetchFlatInterfacesREST(cfg netboxtool.ConfigNetbox, client *http.Client) ([]restInterface, int, error) {
	var out []restInterface
	pages, err := restGetPaginated(cfg, client, "/api/dcim/interfaces/?limit=1000", func(body []byte) (*string, int, error) {
		var page restInterfaceListPage
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, 0, err
		}
		out = append(out, page.Results...)
		return page.Next, len(page.Results), nil
	})
	return out, pages, err
}

func fetchFlatAddressesREST(cfg netboxtool.ConfigNetbox, client *http.Client) ([]restAddress, int, error) {
	var out []restAddress
	// assigned_object_type filters to addresses assigned to a dcim
	// interface, matching the scope of fetchFlatInterfacesREST - a full
	// sync also wants VM interface addresses, left out here for the same
	// devices-only scope as the rest of this benchmark.
	pages, err := restGetPaginated(cfg, client, "/api/ipam/ip-addresses/?limit=1000&assigned_object_type=dcim.interface", func(body []byte) (*string, int, error) {
		var page restAddressListPage
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, 0, err
		}
		out = append(out, page.Results...)
		return page.Next, len(page.Results), nil
	})
	return out, pages, err
}

// ---------------------------------------------------------------------------
// Stitching + benchmark driver
// ---------------------------------------------------------------------------

// stitchedDevice is the minimal shape both flat approaches build, just
// enough to prove out (and time) the join - not a full NBDevice.
type stitchedDevice struct {
	ID         uint
	Interfaces []uint // interface ids belonging to this device
}

func stitchGraphQL(devices []gqlDevice, interfaces []gqlInterface) map[uint]*stitchedDevice {
	byID := make(map[uint]*stitchedDevice, len(devices))
	for _, d := range devices {
		id, _ := strconv.ParseUint(d.ID, 10, 64)
		byID[uint(id)] = &stitchedDevice{ID: uint(id)}
	}
	for _, i := range interfaces {
		if i.Device == nil {
			continue
		}
		devID, _ := strconv.ParseUint(i.Device.ID, 10, 64)
		if dev, ok := byID[uint(devID)]; ok {
			ifaceID, _ := strconv.ParseUint(i.ID, 10, 64)
			dev.Interfaces = append(dev.Interfaces, uint(ifaceID))
		}
	}
	return byID
}

func stitchREST(devices []restDevice, interfaces []restInterface, addresses []restAddress) map[uint]*stitchedDevice {
	byID := make(map[uint]*stitchedDevice, len(devices))
	for _, d := range devices {
		byID[d.ID] = &stitchedDevice{ID: d.ID}
	}
	ifaceByID := make(map[uint]uint, len(interfaces)) // interface id -> device id
	for _, i := range interfaces {
		ifaceByID[i.ID] = i.Device.ID
		if dev, ok := byID[i.Device.ID]; ok {
			dev.Interfaces = append(dev.Interfaces, i.ID)
		}
	}
	// addresses aren't stored on stitchedDevice, but walking + matching
	// every row is the part being timed, so still do the lookup.
	matched := 0
	for _, a := range addresses {
		if a.AssignedObjectID == nil {
			continue
		}
		if _, ok := ifaceByID[*a.AssignedObjectID]; ok {
			matched++
		}
	}
	_ = matched
	return byID
}

// stitchGraphQLInterfacesRESTAddresses joins approach D's two independent
// fetches (GraphQL device_list/interface_list with no nested addresses, REST
// ip-addresses matched by assigned_object_id) - same shape as stitchREST,
// just sourced from two different APIs.
func stitchGraphQLInterfacesRESTAddresses(devices []gqlDevice, interfaces []gqlInterface, addresses []restAddress) map[uint]*stitchedDevice {
	byID := make(map[uint]*stitchedDevice, len(devices))
	for _, d := range devices {
		id, _ := strconv.ParseUint(d.ID, 10, 64)
		byID[uint(id)] = &stitchedDevice{ID: uint(id)}
	}
	ifaceIDs := make(map[uint]bool, len(interfaces)) // known interface ids, to validate address matches
	for _, i := range interfaces {
		if i.Device == nil {
			continue
		}
		devID, _ := strconv.ParseUint(i.Device.ID, 10, 64)
		ifaceID, _ := strconv.ParseUint(i.ID, 10, 64)
		ifaceIDs[uint(ifaceID)] = true
		if dev, ok := byID[uint(devID)]; ok {
			dev.Interfaces = append(dev.Interfaces, uint(ifaceID))
		}
	}
	matched := 0
	for _, a := range addresses {
		if a.AssignedObjectID == nil {
			continue
		}
		if ifaceIDs[*a.AssignedObjectID] {
			matched++
		}
	}
	_ = matched
	return byID
}

// stitchAllGraphQL joins approach F/G's three independent GraphQL fetches
// (device_list, interface_list with no nested addresses, and
// ip_address_list resolved via assigned_object's inline fragments) - only
// addresses whose assigned_object is an InterfaceType are matched, same
// devices-only scope the rest of this benchmark uses (VMInterfaceType
// addresses are fetched but intentionally left unmatched here).
func stitchAllGraphQL(devices []gqlDevice, interfaces []gqlInterface, addresses []gqlAddress) map[uint]*stitchedDevice {
	byID := make(map[uint]*stitchedDevice, len(devices))
	for _, d := range devices {
		id, _ := strconv.ParseUint(d.ID, 10, 64)
		byID[uint(id)] = &stitchedDevice{ID: uint(id)}
	}
	ifaceIDs := make(map[uint]bool, len(interfaces))
	for _, i := range interfaces {
		if i.Device == nil {
			continue
		}
		devID, _ := strconv.ParseUint(i.Device.ID, 10, 64)
		ifaceID, _ := strconv.ParseUint(i.ID, 10, 64)
		ifaceIDs[uint(ifaceID)] = true
		if dev, ok := byID[uint(devID)]; ok {
			dev.Interfaces = append(dev.Interfaces, uint(ifaceID))
		}
	}
	matched := 0
	for _, a := range addresses {
		if a.AssignedObject == nil || a.AssignedObject.Typename != "InterfaceType" {
			continue
		}
		ifaceID, _ := strconv.ParseUint(a.AssignedObject.ID, 10, 64)
		if ifaceIDs[uint(ifaceID)] {
			matched++
		}
	}
	_ = matched
	return byID
}

// RunBenchmark fetches devices/interfaces/addresses three ways and prints
// how long each phase took, so a real sync run's slowness can be attributed
// to "Netbox nested-query resolution" vs. "many small round trips" vs.
// something else entirely (e.g. factum's own DB writes, not measured here).
func RunBenchmark(cfg netboxtool.ConfigNetbox) error {
	nb, err := netboxtool.NewNetboxClient(cfg)
	if err != nil {
		return err
	}
	httpClient := &http.Client{}

	fmt.Println("=== A: current production path (nested GraphQL) ===")
	t0 := time.Now()
	devicesA, err := nb.GetDevices()
	if err != nil {
		return fmt.Errorf("approach A: %w", err)
	}
	elapsedA := time.Since(t0)
	ifaceCountA := 0
	addrCountA := 0
	for _, d := range devicesA {
		ifaceCountA += len(d.Interfaces)
		for _, i := range d.Interfaces {
			addrCountA += len(i.Addresses)
		}
	}
	fmt.Printf("  fetch: %-10s devices=%d interfaces=%d addresses=%d\n", elapsedA.Round(time.Millisecond), len(devicesA), ifaceCountA, addrCountA)

	fmt.Println()
	fmt.Println("=== B: flat-ish GraphQL (device_list, interface_list+ip_addresses) ===")
	t0 = time.Now()
	devicesB, pagesBDevices, err := fetchFlatDevicesGraphQL(nb)
	if err != nil {
		return fmt.Errorf("approach B, devices: %w", err)
	}
	elapsedBDevices := time.Since(t0)
	fmt.Printf("  fetch devices:    %-10s devices=%d pages=%d\n", elapsedBDevices.Round(time.Millisecond), len(devicesB), pagesBDevices)

	t0 = time.Now()
	interfacesB, pagesBInterfaces, err := fetchFlatInterfacesGraphQL(nb)
	if err != nil {
		return fmt.Errorf("approach B, interfaces: %w", err)
	}
	elapsedBInterfaces := time.Since(t0)
	addrCountB := 0
	for _, i := range interfacesB {
		addrCountB += len(i.IPAddresses)
	}
	fmt.Printf("  fetch interfaces: %-10s interfaces=%d addresses=%d pages=%d\n", elapsedBInterfaces.Round(time.Millisecond), len(interfacesB), addrCountB, pagesBInterfaces)

	t0 = time.Now()
	stitchedB := stitchGraphQL(devicesB, interfacesB)
	elapsedBStitch := time.Since(t0)
	fmt.Printf("  stitch:           %-10s devices=%d\n", elapsedBStitch.Round(time.Millisecond), len(stitchedB))
	elapsedB := elapsedBDevices + elapsedBInterfaces + elapsedBStitch
	fmt.Printf("  total:            %s\n", elapsedB.Round(time.Millisecond))

	fmt.Println()
	fmt.Println("=== C: flat REST (devices, interfaces, ip-addresses) ===")
	t0 = time.Now()
	devicesC, pagesCDevices, err := fetchFlatDevicesREST(cfg, httpClient)
	if err != nil {
		return fmt.Errorf("approach C, devices: %w", err)
	}
	elapsedCDevices := time.Since(t0)
	fmt.Printf("  fetch devices:    %-10s devices=%d pages=%d\n", elapsedCDevices.Round(time.Millisecond), len(devicesC), pagesCDevices)

	t0 = time.Now()
	interfacesC, pagesCInterfaces, err := fetchFlatInterfacesREST(cfg, httpClient)
	if err != nil {
		return fmt.Errorf("approach C, interfaces: %w", err)
	}
	elapsedCInterfaces := time.Since(t0)
	fmt.Printf("  fetch interfaces: %-10s interfaces=%d pages=%d\n", elapsedCInterfaces.Round(time.Millisecond), len(interfacesC), pagesCInterfaces)

	t0 = time.Now()
	addressesC, pagesCAddresses, err := fetchFlatAddressesREST(cfg, httpClient)
	if err != nil {
		return fmt.Errorf("approach C, addresses: %w", err)
	}
	elapsedCAddresses := time.Since(t0)
	fmt.Printf("  fetch addresses:  %-10s addresses=%d pages=%d\n", elapsedCAddresses.Round(time.Millisecond), len(addressesC), pagesCAddresses)

	t0 = time.Now()
	stitchedC := stitchREST(devicesC, interfacesC, addressesC)
	elapsedCStitch := time.Since(t0)
	fmt.Printf("  stitch:           %-10s devices=%d\n", elapsedCStitch.Round(time.Millisecond), len(stitchedC))
	elapsedC := elapsedCDevices + elapsedCInterfaces + elapsedCAddresses + elapsedCStitch
	fmt.Printf("  total:            %s\n", elapsedC.Round(time.Millisecond))

	fmt.Println()
	fmt.Println("=== D: GraphQL devices+interfaces (no nested addresses) + REST addresses ===")
	t0 = time.Now()
	interfacesD, pagesDInterfaces, err := fetchFlatInterfacesNoAddrGraphQL(nb)
	if err != nil {
		return fmt.Errorf("approach D, interfaces: %w", err)
	}
	elapsedDInterfaces := time.Since(t0)
	fmt.Printf("  fetch interfaces: %-10s interfaces=%d pages=%d\n", elapsedDInterfaces.Round(time.Millisecond), len(interfacesD), pagesDInterfaces)

	t0 = time.Now()
	addressesD, pagesDAddresses, err := fetchFlatAddressesREST(cfg, httpClient)
	if err != nil {
		return fmt.Errorf("approach D, addresses: %w", err)
	}
	elapsedDAddresses := time.Since(t0)
	fmt.Printf("  fetch addresses:  %-10s addresses=%d pages=%d\n", elapsedDAddresses.Round(time.Millisecond), len(addressesD), pagesDAddresses)

	t0 = time.Now()
	stitchedD := stitchGraphQLInterfacesRESTAddresses(devicesB, interfacesD, addressesD)
	elapsedDStitch := time.Since(t0)
	fmt.Printf("  stitch:           %-10s devices=%d\n", elapsedDStitch.Round(time.Millisecond), len(stitchedD))
	// Reuses devicesB's fetch time (same device_list query as approach B) rather
	// than fetching devices a third time.
	elapsedD := elapsedBDevices + elapsedDInterfaces + elapsedDAddresses + elapsedDStitch
	fmt.Printf("  total (incl. devices fetch above): %s\n", elapsedD.Round(time.Millisecond))

	fmt.Println()
	fmt.Println("=== E: same as D, but interfaces (GraphQL) and addresses (REST) fetched concurrently ===")
	// Nothing links these two fetches until the stitch step, so there's no
	// reason to wait for one before starting the other - if Netbox's uwsgi
	// workers have spare CPU (as observed: one worker pegged, five idle on
	// a 4-CPU box), this should land close to max(interfaces, addresses)
	// instead of their sum.
	var interfacesE []gqlInterface
	var addressesE []restAddress
	var pagesEInterfaces, pagesEAddresses int
	var errIface, errAddr error
	t0 = time.Now()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		interfacesE, pagesEInterfaces, errIface = fetchFlatInterfacesNoAddrGraphQL(nb)
	}()
	go func() {
		defer wg.Done()
		addressesE, pagesEAddresses, errAddr = fetchFlatAddressesREST(cfg, httpClient)
	}()
	wg.Wait()
	if errIface != nil {
		return fmt.Errorf("approach E, interfaces: %w", errIface)
	}
	if errAddr != nil {
		return fmt.Errorf("approach E, addresses: %w", errAddr)
	}
	elapsedEFetch := time.Since(t0)
	fmt.Printf("  fetch (concurrent): %-10s interfaces=%d pages=%d addresses=%d pages=%d\n",
		elapsedEFetch.Round(time.Millisecond), len(interfacesE), pagesEInterfaces, len(addressesE), pagesEAddresses)

	t0 = time.Now()
	stitchedE := stitchGraphQLInterfacesRESTAddresses(devicesB, interfacesE, addressesE)
	elapsedEStitch := time.Since(t0)
	fmt.Printf("  stitch:             %-10s devices=%d\n", elapsedEStitch.Round(time.Millisecond), len(stitchedE))
	elapsedE := elapsedBDevices + elapsedEFetch + elapsedEStitch
	fmt.Printf("  total (incl. devices fetch above): %s\n", elapsedE.Round(time.Millisecond))

	fmt.Println()
	fmt.Println("=== F: fully flat GraphQL (device_list, interface_list, ip_address_list), sequential ===")
	t0 = time.Now()
	interfacesF, pagesFInterfaces, err := fetchFlatInterfacesNoAddrGraphQL(nb)
	if err != nil {
		return fmt.Errorf("approach F, interfaces: %w", err)
	}
	elapsedFInterfaces := time.Since(t0)
	fmt.Printf("  fetch interfaces: %-10s interfaces=%d pages=%d\n", elapsedFInterfaces.Round(time.Millisecond), len(interfacesF), pagesFInterfaces)

	t0 = time.Now()
	addressesF, pagesFAddresses, err := fetchFlatAddressesGraphQL(nb)
	if err != nil {
		return fmt.Errorf("approach F, addresses: %w", err)
	}
	elapsedFAddresses := time.Since(t0)
	fmt.Printf("  fetch addresses:  %-10s addresses=%d pages=%d\n", elapsedFAddresses.Round(time.Millisecond), len(addressesF), pagesFAddresses)

	t0 = time.Now()
	stitchedF := stitchAllGraphQL(devicesB, interfacesF, addressesF)
	elapsedFStitch := time.Since(t0)
	fmt.Printf("  stitch:           %-10s devices=%d\n", elapsedFStitch.Round(time.Millisecond), len(stitchedF))
	elapsedF := elapsedBDevices + elapsedFInterfaces + elapsedFAddresses + elapsedFStitch
	fmt.Printf("  total (incl. devices fetch above): %s\n", elapsedF.Round(time.Millisecond))

	fmt.Println()
	fmt.Println("=== G: same as F, but interfaces and addresses (both GraphQL) fetched concurrently ===")
	var interfacesG []gqlInterface
	var addressesG []gqlAddress
	var pagesGInterfaces, pagesGAddresses int
	var errIfaceG, errAddrG error
	t0 = time.Now()
	var wgG sync.WaitGroup
	wgG.Add(2)
	go func() {
		defer wgG.Done()
		interfacesG, pagesGInterfaces, errIfaceG = fetchFlatInterfacesNoAddrGraphQL(nb)
	}()
	go func() {
		defer wgG.Done()
		addressesG, pagesGAddresses, errAddrG = fetchFlatAddressesGraphQL(nb)
	}()
	wgG.Wait()
	if errIfaceG != nil {
		return fmt.Errorf("approach G, interfaces: %w", errIfaceG)
	}
	if errAddrG != nil {
		return fmt.Errorf("approach G, addresses: %w", errAddrG)
	}
	elapsedGFetch := time.Since(t0)
	fmt.Printf("  fetch (concurrent): %-10s interfaces=%d pages=%d addresses=%d pages=%d\n",
		elapsedGFetch.Round(time.Millisecond), len(interfacesG), pagesGInterfaces, len(addressesG), pagesGAddresses)

	t0 = time.Now()
	stitchedG := stitchAllGraphQL(devicesB, interfacesG, addressesG)
	elapsedGStitch := time.Since(t0)
	fmt.Printf("  stitch:             %-10s devices=%d\n", elapsedGStitch.Round(time.Millisecond), len(stitchedG))
	elapsedG := elapsedBDevices + elapsedGFetch + elapsedGStitch
	fmt.Printf("  total (incl. devices fetch above): %s\n", elapsedG.Round(time.Millisecond))

	fmt.Println()
	fmt.Println("=== Summary ===")
	fmt.Printf("  A (nested GraphQL):                     %s\n", elapsedA.Round(time.Millisecond))
	fmt.Printf("  B (flat GraphQL, 2-step):                %s\n", elapsedB.Round(time.Millisecond))
	fmt.Printf("  C (flat REST, 3-step):                   %s\n", elapsedC.Round(time.Millisecond))
	fmt.Printf("  D (GraphQL ifaces + REST addresses):     %s\n", elapsedD.Round(time.Millisecond))
	fmt.Printf("  E (D, fetched concurrently):             %s\n", elapsedE.Round(time.Millisecond))
	fmt.Printf("  F (fully flat GraphQL, sequential):      %s\n", elapsedF.Round(time.Millisecond))
	fmt.Printf("  G (fully flat GraphQL, concurrent):      %s\n", elapsedG.Round(time.Millisecond))

	return nil
}
