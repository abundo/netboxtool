package netboxtool

//
// Library to manage Netbox
//

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"text/template"
)

// graphqlPageSize is the number of rows requested per page. Netbox caps
// a single query's result at this count regardless of the requested
// limit, so fetchAllPages loops until a page comes back short.
const graphqlPageSize = 1000

// restPageSize is the page size used for paginated REST list endpoints
// (e.g. GetCables), analogous to graphqlPageSize for GraphQL queries.
const restPageSize = 1000

// Configuration file YAML structure
type ConfigNetbox struct {
	URL      string `boa:"configonly" yaml:"url"`
	Token    string `boa:"configonly" yaml:"token"`
	Insecure bool   `boa:"configonly" yaml:"insecure"`
}

type ConfigRoot struct {
	Netbox ConfigNetbox `yaml:"netbox"`
}

var Config ConfigRoot

// client
type NetboxClient struct {
	P          ConfigNetbox
	Device     []*NBDevice
	httpClient *http.Client
}

// API structures
type Response struct {
	Status  string `json:"status"`
	Message string `json:"message"`
	Data    any    `json:"data"`
}
type NetboxAddress struct {
	ID      uint          `json:"id,string"`
	Address string        `json:"address"`
	Role    string        `json:"role"`
	VRF     *NetboxVRFRef `json:"vrf"`
}

// NetboxVRFRef is the minimal VRF reference nested under an interface or ip
// address in GraphQL responses.
type NetboxVRFRef struct {
	ID   uint   `json:"id,string"`
	Name string `json:"name"`
}
type NetboxDeviceManufacturer struct {
	ID   uint `json:"id,string"`
	Name string
}
type NetboxDeviceType struct {
	ID           uint   `json:"id,string"`
	Model        string `json:"model"`
	Manufacturer *NetboxDeviceManufacturer
}
type NetboxInterface struct {
	ID           uint            `json:"id,string"`
	Name         string          `json:"name"`
	IP_addresses []NetboxAddress `json:"ip_addresses"`
	CustomFields struct {
		InterfaceRole string `json:"interface_role"`
	} `json:"custom_fields"`
}
type NetboxPlatform struct {
	ID   uint   `json:"id,string"`
	Name string `json:"name"`
}
type NetboxRole struct {
	ID   uint   `json:"id,string"`
	Name string `json:"name"`
}
type NetboxSite struct {
	ID        uint       `json:"id,string"`
	Name      string     `json:"name"`
	Latitude  *NBDecimal `json:"latitude"`
	Longitude *NBDecimal `json:"longitude"`
}

// NBDecimal unmarshals a Netbox GraphQL Decimal scalar (used for
// latitude/longitude), which may be serialized as either a bare JSON
// number or a JSON string, into a float64.
type NBDecimal float64

func (d *NBDecimal) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fmt.Errorf("netbox decimal %q: %w", s, err)
	}
	*d = NBDecimal(f)
	return nil
}

type NetboxTag struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type NetboxCustomFields struct {
	AlarmDestination string `json:"alarm_destination"`
	AlarmInterfaces  bool   `json:"alarm_interfaces"`
	AlarmTimeperiod  string `json:"alarm_timeperiod"`
	Alias            string `json:"alias"`
	BackupOxidized   bool   `json:"backup_oxidized"`
	Location         string `json:"location"`
	MonitorIcinga    bool   `json:"monitor_icinga"`
	MonitorGrafana   bool   `json:"monitor_grafana"`
	// MonitorLibrenms is a *bool, not bool: netbox returns null for a
	// custom field that has never been set on a device (no default
	// configured), and that must be distinguished from an explicit
	// false - unset devices default to monitored, matching the old
	// (pre-Go) sync script's behavior. See the copy into
	// dbdevice.CfMonitorLibrenms below.
	MonitorLibrenms *bool  `json:"monitor_librenms"`
	Parents         string `json:"parents"`
	ConnectMethod   string `json:"connection_method"`
	LibrenmsID      int    `json:"librenms_id"`
}

// Response from Graphql device_list / virtual_machine_list query.
// device_type/role/site/platform and interfaces (with their ip
// addresses) are nested inline in the query (see deviceListGraphQLbody/
// ids.
type JSONDevice struct {
	ID          uint               `json:"id,string"`
	Name        string             `json:"name"`
	Comments    string             `json:"comments"`
	Status      string             `json:"status"`
	DeviceType  *NetboxDeviceType  `json:"device_type"`
	Platform    *NetboxPlatform    `json:"platform"`
	Role        *NetboxRole        `json:"role"`
	Site        *NetboxSite        `json:"site"`
	Latitude    *NBDecimal         `json:"latitude"`
	Longitude   *NBDecimal         `json:"longitude"`
	PrimaryIPv4 *NetboxAddress     `json:"primary_ip4"`
	PrimaryIPv6 *NetboxAddress     `json:"primary_ip6"`
	Tags        []NetboxTag        `json:"tags"`
	CF          NetboxCustomFields `json:"custom_fields"`
	Interfaces  []JSONInterface    `json:"interfaces"`
}
type JSONDevices struct {
	Data struct {
		// virtualMachineListGraphQLbody), so this shape carries names, not just
		Devices []JSONDevice `json:"device_list"`
	} `json:"data"`
}
type JSONVMs struct {
	Data struct {
		Devices []JSONDevice `json:"virtual_machine_list"`
	} `json:"data"`
}

// Response from the interfaces field nested under
// - device_list          (dcim.Interface)
// - virtual_machine_list (virtualization.VMInterface)
type NetboxVlanRef struct {
	ID  uint `json:"id,string"`
	VID int  `json:"vid"`
}

type JSONInterface struct {
	ID          uint   `json:"id,string"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Type        string `json:"type"`
	// Mode is dcim.Interface.mode ("access", "tagged", "tagged-all" or
	// "q-in-q"), "" if the interface isn't a switchport at all.
	Mode   string `json:"mode"`
	Label  string `json:"label"`
	Parent *struct {
		ID uint `json:"id,string"`
	} `json:"parent"`
	VRF   *NetboxVRFRef `json:"vrf"`
	Cable *struct {
		ID uint `json:"id,string"`
	} `json:"cable"`
	UntaggedVLAN *NetboxVlanRef  `json:"untagged_vlan"`
	TaggedVLANs  []NetboxVlanRef `json:"tagged_vlans"`
	// QinQSVlan is dcim.Interface.qinq_svlan - the S-VLAN Netbox uses in
	// place of untagged_vlan when mode is "q-in-q", mutually exclusive with
	// UntaggedVLAN.
	QinQSVlan    *NetboxVlanRef  `json:"qinq_svlan"`
	Tags         []NetboxTag     `json:"tags"`
	IPAddresses  []NetboxAddress `json:"ip_addresses"`
	CustomFields struct {
		InterfaceRole string `json:"interface_role"`
	} `json:"custom_fields"`
}

// ---------------------------------------------------------------------------
//
//	Functions
//
// ---------------------------------------------------------------------------
func NewNetboxClient(p ConfigNetbox) (*NetboxClient, error) {
	netbox := new(NetboxClient)
	netbox.P = p
	netbox.httpClient = &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: p.Insecure},
		},
	}
	return netbox, nil
}

// parseNetboxID converts a Netbox GraphQL "ID" scalar (a decimal string,
// e.g. tag.id) into the uint used by the NB* models.
func parseNetboxID(s string) uint {
	v, _ := strconv.ParseUint(s, 10, 64)
	return uint(v)
}

func netboxTagsToNBTags(tags []NetboxTag) []NBTag {
	var out []NBTag
	for _, t := range tags {
		out = append(out, NBTag{NetboxID: parseNetboxID(t.ID), Name: t.Name})
	}
	return out
}

// parseDevices parses device or virtual machine rows, including their
// nested interfaces and ip addresses.
func (nb *NetboxClient) parseDevices(devices []JSONDevice, vm bool) ([]*NBDevice, error) {
	var dbdevices []*NBDevice
	dbdevices = make([]*NBDevice, 0, len(devices))
	for _, device := range devices {
		var dbdevice NBDevice
		if device.Name == "" {
			slog.Warn("Netbox device does not have a name", "id", device.ID)
			continue
		}

		// Todo, verify name for valid format/characters
		dbdevice.VM = vm
		dbdevice.NetboxID = device.ID
		dbdevice.Name = device.Name
		dbdevice.Status = device.Status
		if device.DeviceType != nil {
			dbdevice.ModelID = device.DeviceType.ID
			dbdevice.ModelName = device.DeviceType.Model
			if device.DeviceType.Manufacturer != nil {
				dbdevice.ManufacturerID = device.DeviceType.Manufacturer.ID
				dbdevice.Manufacturer = device.DeviceType.Manufacturer.Name
			}
		}
		if device.Role != nil {
			dbdevice.RoleID = device.Role.ID
			dbdevice.Role = device.Role.Name
		}
		if device.Site != nil && device.Site.Name != "Default" {
			dbdevice.SiteID = device.Site.ID
			dbdevice.Site = device.Site.Name
		}
		// Prefer the device's own GPS coordinates; fall back to its site's
		// only when both site coordinates are set (never mix one axis from
		// each source). A device on the placeholder "Default" site is
		// treated the same as having no site above, so it gets no
		// site-inherited coordinates either.
		switch {
		case device.Latitude != nil && device.Longitude != nil:
			lat, lng := float64(*device.Latitude), float64(*device.Longitude)
			dbdevice.Latitude, dbdevice.Longitude = &lat, &lng
		case device.Site != nil && device.Site.Name != "Default" &&
			device.Site.Latitude != nil && device.Site.Longitude != nil:
			lat, lng := float64(*device.Site.Latitude), float64(*device.Site.Longitude)
			dbdevice.Latitude, dbdevice.Longitude = &lat, &lng
		}
		if device.Platform != nil {
			dbdevice.PlatformID = device.Platform.ID
			dbdevice.Platform = device.Platform.Name
		}
		if device.PrimaryIPv4 != nil {
			dbdevice.PrimaryIPv4ID = device.PrimaryIPv4.ID
			dbdevice.PrimaryIPv4 = device.PrimaryIPv4.Address
		}
		if device.PrimaryIPv6 != nil {
			dbdevice.PrimaryIPv6ID = device.PrimaryIPv6.ID
			dbdevice.PrimaryIPv6 = device.PrimaryIPv6.Address
		}
		if device.Status == "active" {
			dbdevice.Enabled = true
		} else {
			dbdevice.Enabled = false
		}
		dbdevice.CfAlarmDestination = device.CF.AlarmDestination
		dbdevice.CfAlarmInterfaces = device.CF.AlarmInterfaces
		dbdevice.CfAlarmTimeperiod = device.CF.AlarmTimeperiod
		dbdevice.CfBackupOxidized = device.CF.BackupOxidized
		dbdevice.CfConnectionMethod = device.CF.ConnectMethod
		dbdevice.CfLocation = device.CF.Location
		dbdevice.CfMonitorGrafana = device.CF.MonitorGrafana
		dbdevice.CfMonitorIcinga = device.CF.MonitorIcinga
		if device.CF.MonitorLibrenms == nil {
			dbdevice.CfMonitorLibrenms = true
		} else {
			dbdevice.CfMonitorLibrenms = *device.CF.MonitorLibrenms
		}
		dbdevice.CfSource = "netbox"
		dbdevice.CfSourceID = device.ID
		dbdevice.LibrenmsID = uint(device.CF.LibrenmsID)
		dbdevice.Tags = netboxTagsToNBTags(device.Tags)

		for _, jintf := range device.Interfaces {
			var addresses []NBAddress
			for _, addr := range jintf.IPAddresses {
				nbaddr := NBAddress{
					NetboxID:      addr.ID,
					NBInterfaceID: jintf.ID,
					Address:       addr.Address,
					Role:          addr.Role,
				}
				if addr.VRF != nil {
					nbaddr.VRF = addr.VRF.Name
				}
				addresses = append(addresses, nbaddr)
			}
			iface := NBInterface{
				NetboxID:    jintf.ID,
				Name:        jintf.Name,
				Description: jintf.Description,
				Type:        jintf.Type,
				Mode:        jintf.Mode,
				Label:       jintf.Label,
				CfRole:      jintf.CustomFields.InterfaceRole,
				Tags:        netboxTagsToNBTags(jintf.Tags),
				Addresses:   addresses,
			}
			if jintf.VRF != nil {
				iface.VRF = jintf.VRF.Name
			}
			if jintf.Cable != nil {
				iface.CableID = jintf.Cable.ID
			}
			if jintf.Parent != nil {
				iface.ParentID = jintf.Parent.ID
			}
			switch {
			case jintf.UntaggedVLAN != nil:
				iface.UntaggedVLAN = jintf.UntaggedVLAN.VID
			case jintf.QinQSVlan != nil:
				// Netbox uses qinq_svlan instead of untagged_vlan when mode
				// is "q-in-q" (the two are mutually exclusive) - merged into
				// the same field here since callers treat UntaggedVLAN as
				// "whichever single outer/native VLAN this interface has",
				// regardless of which Netbox mode it's carried under.
				iface.UntaggedVLAN = jintf.QinQSVlan.VID
			}
			for _, v := range jintf.TaggedVLANs {
				iface.TaggedVLANs = append(iface.TaggedVLANs, v.VID)
			}
			dbdevice.Interfaces = append(dbdevice.Interfaces, iface)
		}

		dbdevices = append(dbdevices, &dbdevice)
	}
	return dbdevices, nil
}

// Create a device in netbox
// assumes there is a "device template" for the device in netbox
// todo: if no device template, send error?
// Note: tags must exist in Netbox
// Note: tags must be the slug name
// Returns True if ok
func (nb *NetboxClient) CreateDevice(
	name string,
	site_id int,
	device_type_id int,
	role string,
	platform string,
	manufacturer_id int,
	model string,
) (*JSONDevices, error) {

	return nil, errors.New("not implemented")
}

type graphqlErrors struct {
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

// Helper function, to fetch a page of
//   - all devices/vms
//   - one device/vm, selected by name
//   - one device/vm, selected by id
//
// start is a cursor (the Netbox object id to resume from, "id >= start"),
// not a row offset - Netbox's offset-based pagination costs more the
// further into the table you page (Postgres has to scan and discard
// `offset` rows), while cursor pagination is a constant-cost index seek
// regardless of position. See fetchAllPages.
func (nb *NetboxClient) NetboxAPICall(grapqlQuery string, name string, id int, start, limit int) ([]byte, error) {
	args := ""
	if name != "" {
		args = "filters: { name: { exact: \"" + name + "\" } }"
	}
	if id > 0 {
		args = "filters: { id: { exact: \"" + strconv.Itoa(id) + "\" } }"
	}
	if args != "" {
		args += ", "
	}
	return nb.netboxAPICall(grapqlQuery, args, start, limit)
}

// netboxAPICall executes grapqlQuery against the graphql endpoint, with
// filterArgs (a pre-built "filters: {...}, " fragment, or "") spliced in
// ahead of the pagination args.
func (nb *NetboxClient) netboxAPICall(grapqlQuery string, filterArgs string, start, limit int) ([]byte, error) {
	var err error
	url := nb.P.URL + "/graphql/"

	args := filterArgs + fmt.Sprintf("pagination: { start: %d, limit: %d }", start, limit)
	filter_ := map[string]string{
		"filter": "(" + args + ")",
	}
	templ, buf := new(template.Template), new(strings.Builder)
	err = template.Must(templ.Parse(grapqlQuery)).Execute(buf, filter_)
	if err != nil {
		return nil, err
	}

	query := map[string]string{
		"query": buf.String(),
	}
	var jsonPayload []byte
	jsonPayload, err = json.Marshal(query)
	if err != nil {
		return nil, err
	}
	var req *http.Request
	req, err = http.NewRequest("POST", url, bytes.NewBuffer(jsonPayload))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Token "+nb.P.Token)
	req.Header.Set("Content-Type", "application/json")

	response, err := nb.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("netbox graphql request: %w", err)
	}
	defer response.Body.Close()
	respData, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}

	var gerr graphqlErrors
	if err := json.Unmarshal(respData, &gerr); err == nil && len(gerr.Errors) > 0 {
		msgs := make([]string, len(gerr.Errors))
		for i, e := range gerr.Errors {
			msgs[i] = e.Message
		}
		return nil, fmt.Errorf("netbox graphql error: %s", strings.Join(msgs, "; "))
	}

	return respData, nil
}

// fetchAllPages repeatedly calls NetboxAPICall, cursoring by the last row
// id seen, until a page returns fewer than graphqlPageSize rows.
// unmarshalPage must unmarshal respdata, return how many rows the page
// contained, and the id of the last (highest-id) row in the page - Netbox
// returns cursor-paginated rows ordered by id ascending, so that's the
// next start.
func (nb *NetboxClient) fetchAllPages(grapqlQuery string, name string, id int, unmarshalPage func(respdata []byte) (count int, lastID uint, err error)) error {
	return nb.fetchAllPagesRaw(func(start int) ([]byte, error) {
		return nb.NetboxAPICall(grapqlQuery, name, id, start, graphqlPageSize)
	}, unmarshalPage)
}

// fetchAllPagesRaw drives the cursor-pagination loop used by
// fetchAllPages: it repeatedly calls fetchPage, cursoring by the last row
// id seen (via unmarshalPage), until a page returns fewer than
// graphqlPageSize rows.
func (nb *NetboxClient) fetchAllPagesRaw(fetchPage func(start int) ([]byte, error), unmarshalPage func(respdata []byte) (count int, lastID uint, err error)) error {
	start := 0
	for {
		respdata, err := fetchPage(start)
		if err != nil {
			return err
		}
		count, lastID, err := unmarshalPage(respdata)
		if err != nil {
			return err
		}
		if count < graphqlPageSize {
			return nil
		}
		start = int(lastID) + 1
	}
}

// restPatch sends a PATCH with a JSON body to a Netbox REST endpoint (as
// opposed to NetboxAPICall, which only talks to the read-only GraphQL
// endpoint) and returns an error if the response is not 2xx.
func (nb *NetboxClient) restPatch(endpoint string, payload any) error {
	url := nb.P.URL + endpoint

	jsonPayload, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequest("PATCH", url, bytes.NewReader(jsonPayload))
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Token "+nb.P.Token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := nb.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("netbox PATCH %s: %w", endpoint, err)
	}
	defer resp.Body.Close()

	respData, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("netbox PATCH %s failed: %s: %s", endpoint, resp.Status, string(respData))
	}
	return nil
}

// restGet sends a GET to a Netbox REST endpoint and unmarshals the response
// into out.
func (nb *NetboxClient) restGet(endpoint string, out any) error {
	url := nb.P.URL + endpoint

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Token "+nb.P.Token)

	resp, err := nb.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("netbox GET %s: %w", endpoint, err)
	}
	defer resp.Body.Close()

	respData, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("netbox GET %s failed: %s: %s", endpoint, resp.Status, string(respData))
	}
	return json.Unmarshal(respData, out)
}

// restPost sends a POST with a JSON body to a Netbox REST endpoint and
// unmarshals the response into out (skipped if out is nil).
func (nb *NetboxClient) restPost(endpoint string, payload any, out any) error {
	url := nb.P.URL + endpoint

	jsonPayload, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequest("POST", url, bytes.NewReader(jsonPayload))
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Token "+nb.P.Token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := nb.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("netbox POST %s: %w", endpoint, err)
	}
	defer resp.Body.Close()

	respData, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("netbox POST %s failed: %s: %s", endpoint, resp.Status, string(respData))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(respData, out)
}

// restDelete sends a DELETE to a Netbox REST endpoint and returns an error
// if the response is not 2xx.
func (nb *NetboxClient) restDelete(endpoint string) error {
	url := nb.P.URL + endpoint

	req, err := http.NewRequest("DELETE", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Token "+nb.P.Token)

	resp, err := nb.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("netbox DELETE %s: %w", endpoint, err)
	}
	defer resp.Body.Close()

	respData, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("netbox DELETE %s failed: %s: %s", endpoint, resp.Status, string(respData))
	}
	return nil
}

// ---------------------------------------------------------------------------
//  Manufacturer
// ---------------------------------------------------------------------------

// NetboxManufacturer is a dcim.Manufacturer row, fetched via GraphQL (see
// manufacturerListGraphQLbody).
type NetboxManufacturer struct {
	ID          uint   `json:"id,string"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Comments    string `json:"comments"`
}

// GetManufacturer looks up a manufacturer by its display name (e.g.
// "Cisco") or, if id is set (id > 0), by id.
func (nb *NetboxClient) GetManufacturer(name string, id int) (*NetboxManufacturer, error) {
	respdata, err := nb.NetboxAPICall(manufacturerListGraphQLbody, name, id, 0, graphqlPageSize)
	if err != nil {
		return nil, err
	}

	var page struct {
		Data struct {
			Manufacturers []NetboxManufacturer `json:"manufacturer_list"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respdata, &page); err != nil {
		return nil, err
	}
	if len(page.Data.Manufacturers) == 0 {
		return nil, fmt.Errorf("netbox manufacturer not found: name=%q id=%d", name, id)
	}
	return &page.Data.Manufacturers[0], nil
}

// ---------------------------------------------------------------------------
//  Tenant
// ---------------------------------------------------------------------------

// TenantCustomFields holds the subset of a tenant's custom_fields this
// package knows about: "source"/"source_id" identify which external
// system (and which record within it, e.g. a CRM customer id) a tenant
// was synced from.
type TenantCustomFields struct {
	Source   string `json:"source"`
	SourceID string `json:"source_id"`
}

// JSONTenant is a tenancy.Tenant row, fetched via GraphQL (see
// tenantListGraphQLbody).
type JSONTenant struct {
	ID   uint               `json:"id,string"`
	Name string             `json:"name"`
	Slug string             `json:"slug"`
	CF   TenantCustomFields `json:"custom_fields"`
}

type JSONTenants struct {
	Data struct {
		Tenants []JSONTenant `json:"tenant_list"`
	} `json:"data"`
}

func parseTenants(tenants []JSONTenant) []*NBTenant {
	dbtenants := make([]*NBTenant, 0, len(tenants))
	for _, t := range tenants {
		dbtenants = append(dbtenants, &NBTenant{
			NetboxID:   t.ID,
			Name:       t.Name,
			Slug:       t.Slug,
			CfSource:   t.CF.Source,
			CfSourceID: t.CF.SourceID,
		})
	}
	return dbtenants
}

// GetTenants fetches all tenants via a single tenant_list query.
func (nb *NetboxClient) GetTenants() ([]*NBTenant, error) {
	var tenants JSONTenants
	err := nb.fetchAllPages(tenantListGraphQLbody, "", -1, func(respdata []byte) (int, uint, error) {
		var page JSONTenants
		if err := json.Unmarshal(respdata, &page); err != nil {
			return 0, 0, err
		}
		tenants.Data.Tenants = append(tenants.Data.Tenants, page.Data.Tenants...)
		var lastID uint
		if n := len(page.Data.Tenants); n > 0 {
			lastID = page.Data.Tenants[n-1].ID
		}
		return len(page.Data.Tenants), lastID, nil
	})
	if err != nil {
		return nil, err
	}
	return parseTenants(tenants.Data.Tenants), nil
}

// NetboxTenantREST is a tenancy.Tenant row as returned by the REST API
// (unlike JSONTenant, whose `id,string` tag matches the GraphQL ID scalar,
// this one's id is a plain JSON number).
type NetboxTenantREST struct {
	ID   uint   `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

// CreateTenant creates a tenant. Netbox requires both name and slug to be
// unique; changes may set "custom_fields" (map[string]any) and any other
// writable tenant field alongside name/slug.
func (nb *NetboxClient) CreateTenant(name, slug string, changes map[string]any) (*NetboxTenantREST, error) {
	payload := map[string]any{
		"name": name,
		"slug": slug,
	}
	for k, v := range changes {
		payload[k] = v
	}
	var result NetboxTenantREST
	if err := nb.restPost("/api/tenancy/tenants/", payload, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// UpdateTenant updates a Netbox tenant with arbitrary (possibly nested,
// e.g. custom_fields) values.
func (nb *NetboxClient) UpdateTenant(tenantID uint, changes map[string]any) error {
	return nb.restPatch("/api/tenancy/tenants/"+strconv.FormatUint(uint64(tenantID), 10)+"/", changes)
}

// netboxTenantListREST is one page of the REST tenants list endpoint, used
// only by GetTenant's filtered lookup - tenantListGraphQLbody has no filter
// for custom fields, so this goes through the REST API instead, which
// supports filtering directly on a custom field via a cf_<name> query
// param.
type netboxTenantListREST struct {
	Results []struct {
		NetboxTenantREST
		CF TenantCustomFields `json:"custom_fields"`
	} `json:"results"`
}

// GetTenant looks up the single tenant whose custom fields match
// source/sourceID (see TenantCustomFields), via a server-side filtered
// REST call instead of GetTenants' full-table fetch - the point being
// O(1) instead of O(tenant count) for a caller (FindOrCreateTenant) that
// only ever wants one row. Returns nil, nil (not an error) if no tenant
// matches.
func (nb *NetboxClient) GetTenant(source, sourceID string) (*NBTenant, error) {
	endpoint := "/api/tenancy/tenants/?cf_source=" + url.QueryEscape(source) + "&cf_source_id=" + url.QueryEscape(sourceID)
	var page netboxTenantListREST
	if err := nb.restGet(endpoint, &page); err != nil {
		return nil, err
	}
	if len(page.Results) == 0 {
		return nil, nil
	}
	t := page.Results[0]
	return &NBTenant{
		NetboxID:   t.ID,
		Name:       t.Name,
		Slug:       t.Slug,
		CfSource:   t.CF.Source,
		CfSourceID: t.CF.SourceID,
	}, nil
}

// JSONSites is a page of the top-level site_list GraphQL query (see
// siteListGraphQLbody), reusing NetboxSite since its fields (id/name/
// latitude/longitude) already match what device_list's nested "site"
// selection returns.
type JSONSites struct {
	Data struct {
		Sites []NetboxSite `json:"site_list"`
	} `json:"data"`
}

// GetSites fetches every Netbox site, for callers that need the full site
// inventory rather than just the sites referenced by a device (e.g. the
// network map, which also plots sites with no devices of their own). Sites
// with no coordinates, and the placeholder "Default" site, are skipped -
// same filtering parseDevices applies to a device's own site reference.
func (nb *NetboxClient) GetSites() ([]*NetboxSite, error) {
	var sites JSONSites
	err := nb.fetchAllPages(siteListGraphQLbody, "", -1, func(respdata []byte) (int, uint, error) {
		var page JSONSites
		if err := json.Unmarshal(respdata, &page); err != nil {
			return 0, 0, err
		}
		sites.Data.Sites = append(sites.Data.Sites, page.Data.Sites...)
		var lastID uint
		if n := len(page.Data.Sites); n > 0 {
			lastID = page.Data.Sites[n-1].ID
		}
		return len(page.Data.Sites), lastID, nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]*NetboxSite, 0, len(sites.Data.Sites))
	for _, site := range sites.Data.Sites {
		if site.Name == "Default" || site.Latitude == nil || site.Longitude == nil {
			continue
		}
		out = append(out, &site)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
//  Device Type
// ---------------------------------------------------------------------------

// NetboxInterfaceTemplate is a dcim.InterfaceTemplate row, as attached to a device type.
type NetboxInterfaceTemplate struct {
	ID          uint   `json:"id,string"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Type        string `json:"type"`
}

// NetboxDeviceTypeDetail is a device type together with its interface
// templates, both resolved server-side in a single nested GraphQL query
// (see deviceTypeGraphQLbody).
type NetboxDeviceTypeDetail struct {
	ID              uint                      `json:"id,string"`
	Model           string                    `json:"model"`
	Manufacturer    NetboxDeviceManufacturer  `json:"manufacturer"`
	DefaultPlatform *NetboxPlatform           `json:"default_platform"`
	CF              NetboxCustomFields        `json:"custom_fields"`
	Interfaces      []NetboxInterfaceTemplate `json:"interfacetemplates"`
}

// GetDeviceType looks up a device type by manufacturer name+model, and
// fetches its interface templates in the same nested GraphQL query
// (device_type_list.interfacetemplates), rather than a separate REST
// request.
func (nb *NetboxClient) GetDeviceType(manufacturer string, model string) (*NetboxDeviceTypeDetail, error) {
	mfr, err := nb.GetManufacturer(manufacturer, 0)
	if err != nil {
		return nil, err
	}

	filterArgs := "filters: { manufacturer_id: \"" + strconv.FormatUint(uint64(mfr.ID), 10) + "\", model: { exact: \"" + model + "\" } }, "
	respdata, err := nb.netboxAPICall(deviceTypeGraphQLbody, filterArgs, 0, graphqlPageSize)
	if err != nil {
		return nil, err
	}

	var page struct {
		Data struct {
			DeviceTypes []NetboxDeviceTypeDetail `json:"device_type_list"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respdata, &page); err != nil {
		return nil, err
	}
	if len(page.Data.DeviceTypes) == 0 {
		return nil, fmt.Errorf("netbox device type not found: manufacturer=%q model=%q", manufacturer, model)
	}

	return &page.Data.DeviceTypes[0], nil
}

// ---------------------------------------------------------------------------
//  Device
// ---------------------------------------------------------------------------

// GetDevices_ fetches devices via a single nested device_list query
// (device_type/role/site/platform and interfaces/ip addresses are all
// resolved server-side, see deviceListGraphQLbody). name/id, if set,
// filter device_list itself so only the matching device is fetched.
func (nb *NetboxClient) GetDevices_(name string, id int) ([]*NBDevice, error) {
	var devices JSONDevices
	err := nb.fetchAllPages(deviceListGraphQLbody, name, id, func(respdata []byte) (int, uint, error) {
		var page JSONDevices
		if err := json.Unmarshal(respdata, &page); err != nil {
			return 0, 0, err
		}
		devices.Data.Devices = append(devices.Data.Devices, page.Data.Devices...)
		var lastID uint
		if n := len(page.Data.Devices); n > 0 {
			lastID = page.Data.Devices[n-1].ID
		}
		return len(page.Data.Devices), lastID, nil
	})
	if err != nil {
		return nil, err
	}

	dbdevices, err := nb.parseDevices(devices.Data.Devices, false)
	if err != nil {
		return nil, err
	}

	return dbdevices, nil
}

func (nb *NetboxClient) GetDevices() ([]*NBDevice, error) {
	return nb.GetDevices_("", -1)
}

// GetDevice fetches one device by name or id, returning nil, nil (not an
// error) if none matches - mirroring pynetbox's own .get(), which returns
// None rather than raising for an unknown name.
func (nb *NetboxClient) GetDevice(name string, id int) (*NBDevice, error) {
	// filter = "(filters: {id:{ exact: \"" + strconv.Itoa(id) + "\"}})"
	// filter = "(filters: {name:{ exact: \"" + name + "\"}})"
	devices, err := nb.GetDevices_(name, id)
	if err != nil {
		return nil, err
	}
	if len(devices) == 0 {
		return nil, nil
	}
	return devices[0], nil
}

// UpdateDevice updates a Netbox device with arbitrary (possibly nested, e.g. custom_fields) values.
// https://netboxlabs.com/docs/netbox/en/stable/rest-api/overview/#update
func (nb *NetboxClient) UpdateDevice(deviceID uint, changes map[string]any) error {
	return nb.restPatch("/api/dcim/devices/"+strconv.FormatUint(uint64(deviceID), 10)+"/", changes)
}

// UpdateVM updates a Netbox virtual machine with arbitrary (possibly nested) values
func (nb *NetboxClient) UpdateVM(vmID uint, changes map[string]any) error {
	return nb.restPatch("/api/virtualization/virtual-machines/"+strconv.FormatUint(uint64(vmID), 10)+"/", changes)
}

// Delete a device in netbox, specificed by device id
func (nb *NetboxClient) DeleteDevice(id int) error {
	return nb.restDelete("/api/dcim/devices/" + strconv.Itoa(id) + "/")
}

// ---------------------------------------------------------------------------
//  Virtual Machine
// ---------------------------------------------------------------------------

// GetVM_ is the virtual_machine equivalent of GetDevices_: a single
// nested virtual_machine_list query resolves site and interfaces/ip
// addresses server-side, and name/id, if set, filter virtual_machine_list
// itself so only the matching VM is fetched.
func (nb *NetboxClient) GetVM_(name string, id int) ([]*NBDevice, error) {
	var devices JSONVMs
	err := nb.fetchAllPages(virtualMachineListGraphQLbody, name, id, func(respdata []byte) (int, uint, error) {
		var page JSONVMs
		if err := json.Unmarshal(respdata, &page); err != nil {
			return 0, 0, err
		}
		devices.Data.Devices = append(devices.Data.Devices, page.Data.Devices...)
		var lastID uint
		if n := len(page.Data.Devices); n > 0 {
			lastID = page.Data.Devices[n-1].ID
		}
		return len(page.Data.Devices), lastID, nil
	})
	if err != nil {
		return nil, err
	}

	data, err := nb.parseDevices(devices.Data.Devices, true)
	if err != nil {
		return nil, err
	}

	return data, nil
}

func (nb *NetboxClient) GetVMs() ([]*NBDevice, error) {
	return nb.GetVM_("", -1)
}

// GetVM fetches one virtual machine by name or id, returning nil, nil (not
// an error) if none matches - mirroring GetDevice/pynetbox's own .get(),
// which returns None rather than raising for an unknown name.
func (nb *NetboxClient) GetVM(name string, id int) (*NBDevice, error) {
	devices, err := nb.GetVM_(name, id)
	if err != nil {
		return nil, err
	}
	if len(devices) == 0 {
		return nil, nil
	}
	return devices[0], nil
}

// ---------------------------------------------------------------------------
//  Interface
// ---------------------------------------------------------------------------

// NetboxInterfaceREST is a dcim.Interface row as returned by the REST API
// (unlike NetboxInterface, whose `id,string` tag matches the GraphQL ID
// scalar, this one's id is a plain JSON number).
type NetboxInterfaceREST struct {
	ID          uint   `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Create an interface on a device. Netbox's graphql API is read-only, so
// this goes through the REST API instead (POST /api/dcim/interfaces/).
// "virtual" is used as the interface type since callers don't supply
// hardware-specific type info.
func (nb *NetboxClient) InterfaceCreate(device_id int, name string) (*NetboxInterfaceREST, error) {
	payload := map[string]any{
		"device": device_id,
		"name":   name,
		"type":   "virtual",
	}
	var result NetboxInterfaceREST
	if err := nb.restPost("/api/dcim/interfaces/", payload, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// CreateInterfaceWithOptions is InterfaceCreate plus arbitrary extra REST
// fields (e.g. "type": "lag", "vrf": {"name": ...}) - for callers (like
// internal/device-sync) that know more about the interface being created
// than InterfaceCreate's fixed "virtual" type allows for.
func (nb *NetboxClient) CreateInterfaceWithOptions(deviceID uint, name string, extra map[string]any) (*NetboxInterfaceREST, error) {
	payload := map[string]any{
		"device": deviceID,
		"name":   name,
		"type":   "virtual",
	}
	for k, v := range extra {
		payload[k] = v
	}
	var result NetboxInterfaceREST
	if err := nb.restPost("/api/dcim/interfaces/", payload, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// Update an interface. changes is any, not map[string]string, so callers
// can send nested values (e.g. {"vrf": {"name": "MGMT"}}).
// PATCH /api/dcim/interfaces/{id}/
func (nb *NetboxClient) InterfaceUpdate(interface_id int, changes map[string]any) error {
	return nb.restPatch("/api/dcim/interfaces/"+strconv.Itoa(interface_id)+"/", changes)
}

// Delete an interface
func (nb *NetboxClient) InterfaceDelete(interface_id int) error {
	return nb.restDelete("/api/dcim/interfaces/" + strconv.Itoa(interface_id) + "/")
}

// ---------------------------------------------------------------------------
//  Address
// ---------------------------------------------------------------------------

// NetboxAddressREST is an ipam.IPAddress row as returned by the REST API
// (unlike NetboxAddress, whose `id,string` tag matches the GraphQL ID
// scalar, this one's id is a plain JSON number).
type NetboxAddressREST struct {
	ID      uint   `json:"id"`
	Address string `json:"address"`
}

// Create an address, not assigned to any interface.
func (nb *NetboxClient) AddressCreate(address string) (*NetboxAddressREST, error) {
	payload := map[string]any{
		"address": address,
	}
	var result NetboxAddressREST
	if err := nb.restPost("/api/ipam/ip-addresses/", payload, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// Create an address on an interface
func (nb *NetboxClient) InterfaceAddressCreate(intf NetboxInterface, address NetboxAddress, status string) (*NetboxAddressREST, error) {
	payload := map[string]any{
		"assigned_object_type": "dcim.interface",
		"assigned_object_id":   intf.ID,
		"address":              address.Address,
		"status":               status,
	}
	var result NetboxAddressREST
	if err := nb.restPost("/api/ipam/ip-addresses/", payload, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// CreateInterfaceAddress creates an ip address assigned to interfaceID,
// plus arbitrary extra REST fields (e.g. "role": "anycast",
// "vrf": {"name": ...}) - unlike InterfaceAddressCreate, which only sends
// address/status, this is what internal/device-sync's address_create needs
// (Netbox models VRF and role on the address, not the interface).
func (nb *NetboxClient) CreateInterfaceAddress(interfaceID uint, address string, extra map[string]any) (*NBAddress, error) {
	payload := map[string]any{
		"assigned_object_type": "dcim.interface",
		"assigned_object_id":   interfaceID,
		"address":              address,
	}
	for k, v := range extra {
		payload[k] = v
	}
	var result NetboxAddressREST
	if err := nb.restPost("/api/ipam/ip-addresses/", payload, &result); err != nil {
		return nil, err
	}
	return &NBAddress{NetboxID: result.ID, NBInterfaceID: interfaceID, Address: result.Address}, nil
}

// Update an address (e.g. its role or vrf).
// PATCH /api/ipam/ip-addresses/{id}/
func (nb *NetboxClient) AddressUpdate(address_id int, changes map[string]any) error {
	return nb.restPatch("/api/ipam/ip-addresses/"+strconv.Itoa(address_id)+"/", changes)
}

// Delete an address
func (nb *NetboxClient) AddressDelete(address_id int) error {
	return nb.restDelete("/api/ipam/ip-addresses/" + strconv.Itoa(address_id) + "/")
}

// ---------------------------------------------------------------------------
//  Cable
// ---------------------------------------------------------------------------

// netboxCableTermination is one entry of a cable's a_terminations/
// b_terminations array, as accepted/returned by the REST API. Netbox
// supports terminating a cable on more than one object per side (a
// distribution cable); this package only ever creates/expects exactly one,
// matching device_nb_sync's own connections-between-interfaces model.
type netboxCableTermination struct {
	ObjectType string `json:"object_type"`
	ObjectID   uint   `json:"object_id"`
}

// netboxCableREST is a dcim.Cable row as returned by the REST API.
type netboxCableREST struct {
	ID            uint                     `json:"id"`
	Label         string                   `json:"label"`
	ATerminations []netboxCableTermination `json:"a_terminations"`
	BTerminations []netboxCableTermination `json:"b_terminations"`
}

func (c *netboxCableREST) toNBCable() *NBCable {
	cable := &NBCable{NetboxID: c.ID, Label: c.Label}
	if len(c.ATerminations) > 0 {
		cable.AInterface = c.ATerminations[0].ObjectID
	}
	if len(c.BTerminations) > 0 {
		cable.BInterface = c.BTerminations[0].ObjectID
	}
	return cable
}

// isInterfaceToInterface reports whether both ends of the cable terminate
// on exactly one dcim.interface each - the only shape NBCable represents.
// Netbox cables can otherwise terminate on other object types (console
// ports, circuit terminations, ...) or on more than one object per side
// (distribution cables), neither of which toNBCable's ObjectID would
// resolve to the right thing.
func (c *netboxCableREST) isInterfaceToInterface() bool {
	return len(c.ATerminations) == 1 && c.ATerminations[0].ObjectType == "dcim.interface" &&
		len(c.BTerminations) == 1 && c.BTerminations[0].ObjectType == "dcim.interface"
}

// GetCable fetches one cable by id - interfaces already carry their own
// cable id (NBInterface.CableID, from the GraphQL device query), so this is
// how a caller resolves it to the cable's actual terminations.
func (nb *NetboxClient) GetCable(cableID uint) (*NBCable, error) {
	var result netboxCableREST
	if err := nb.restGet("/api/dcim/cables/"+strconv.FormatUint(uint64(cableID), 10)+"/", &result); err != nil {
		return nil, err
	}
	return result.toNBCable(), nil
}

// CreateCable creates a cable directly connecting two interfaces.
func (nb *NetboxClient) CreateCable(aInterfaceID, bInterfaceID uint) (*NBCable, error) {
	payload := map[string]any{
		"a_terminations": []netboxCableTermination{{ObjectType: "dcim.interface", ObjectID: aInterfaceID}},
		"b_terminations": []netboxCableTermination{{ObjectType: "dcim.interface", ObjectID: bInterfaceID}},
	}
	var result netboxCableREST
	if err := nb.restPost("/api/dcim/cables/", payload, &result); err != nil {
		return nil, err
	}
	return result.toNBCable(), nil
}

// netboxCableListREST is one page of the REST cables list endpoint.
// "next" is the full URL (scheme+host+query) of the following page, or
// nil on the last page - Netbox's standard DRF pagination shape.
type netboxCableListREST struct {
	Next    *string           `json:"next"`
	Results []netboxCableREST `json:"results"`
}

// GetCables fetches every cable in Netbox via the REST API, paginating
// until "next" is null. Unlike GetCable, which resolves one already-known
// cable id, this is how a caller discovers the full set of cables - e.g.
// to build a device connectivity map, not just the ones this package
// itself created. Cables not directly connecting two interfaces (see
// isInterfaceToInterface) are silently skipped rather than represented
// with a wrong/zero AInterface or BInterface.
func (nb *NetboxClient) GetCables() ([]*NBCable, error) {
	var cables []*NBCable
	endpoint := "/api/dcim/cables/?limit=" + strconv.Itoa(restPageSize)
	for endpoint != "" {
		var page netboxCableListREST
		if err := nb.restGet(endpoint, &page); err != nil {
			return nil, err
		}
		for _, c := range page.Results {
			if c.isInterfaceToInterface() {
				cables = append(cables, c.toNBCable())
			}
		}
		if page.Next == nil {
			break
		}
		next, err := url.Parse(*page.Next)
		if err != nil {
			return nil, fmt.Errorf("netbox cables: parse next page url: %w", err)
		}
		endpoint = "/api/dcim/cables/?" + next.RawQuery
	}
	return cables, nil
}

// DeleteCable deletes a cable.
func (nb *NetboxClient) DeleteCable(cableID uint) error {
	return nb.restDelete("/api/dcim/cables/" + strconv.FormatUint(uint64(cableID), 10) + "/")
}

// UpdateCableTermination replaces one side of an existing cable with
// interfaceID - side 1 is the A side, side 2 is the B side.
func (nb *NetboxClient) UpdateCableTermination(cableID uint, side int, interfaceID uint) error {
	terminations := []netboxCableTermination{{ObjectType: "dcim.interface", ObjectID: interfaceID}}
	var field string
	switch side {
	case 1:
		field = "a_terminations"
	case 2:
		field = "b_terminations"
	default:
		return fmt.Errorf("unknown cable termination side %d", side)
	}
	return nb.restPatch("/api/dcim/cables/"+strconv.FormatUint(uint64(cableID), 10)+"/", map[string]any{field: terminations})
}

// ---------------------------------------------------------------------------
//  Prefix
// ---------------------------------------------------------------------------

type netboxPrefixREST struct {
	ID     uint   `json:"id"`
	Prefix string `json:"prefix"`
	Status struct {
		Value string `json:"value"`
	} `json:"status"`
}

func (p *netboxPrefixREST) toNBPrefix() *NBPrefix {
	return &NBPrefix{NetboxID: p.ID, Prefix: p.Prefix, Status: p.Status.Value}
}

// GetPrefix looks up an existing prefix by its exact CIDR value, returning
// nil (no error) if none exists.
func (nb *NetboxClient) GetPrefix(prefix string) (*NBPrefix, error) {
	var page struct {
		Results []netboxPrefixREST `json:"results"`
	}
	if err := nb.restGet("/api/ipam/prefixes/?prefix="+url.QueryEscape(prefix), &page); err != nil {
		return nil, err
	}
	if len(page.Results) == 0 {
		return nil, nil
	}
	return page.Results[0].toNBPrefix(), nil
}

// CreatePrefix creates a new prefix with status "active".
func (nb *NetboxClient) CreatePrefix(prefix string) (*NBPrefix, error) {
	payload := map[string]any{
		"prefix": prefix,
		"status": "active",
	}
	var result netboxPrefixREST
	if err := nb.restPost("/api/ipam/prefixes/", payload, &result); err != nil {
		return nil, err
	}
	return result.toNBPrefix(), nil
}

// ---------------------------------------------------------------------------
//  VLAN Group / VLAN
// ---------------------------------------------------------------------------

type netboxVlanGroupREST struct {
	ID   uint   `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

func (g *netboxVlanGroupREST) toNBVlanGroup() *NBVlanGroup {
	return &NBVlanGroup{NetboxID: g.ID, Name: g.Name, Slug: g.Slug}
}

// GetVlanGroup looks up an existing VLAN group by its exact name, returning
// nil (no error) if none exists.
func (nb *NetboxClient) GetVlanGroup(name string) (*NBVlanGroup, error) {
	var page struct {
		Results []netboxVlanGroupREST `json:"results"`
	}
	if err := nb.restGet("/api/ipam/vlan-groups/?name="+url.QueryEscape(name), &page); err != nil {
		return nil, err
	}
	if len(page.Results) == 0 {
		return nil, nil
	}
	return page.Results[0].toNBVlanGroup(), nil
}

// CreateVlanGroup creates a new, global (unscoped) VLAN group.
func (nb *NetboxClient) CreateVlanGroup(name, slug string) (*NBVlanGroup, error) {
	payload := map[string]any{
		"name": name,
		"slug": slug,
	}
	var result netboxVlanGroupREST
	if err := nb.restPost("/api/ipam/vlan-groups/", payload, &result); err != nil {
		return nil, err
	}
	return result.toNBVlanGroup(), nil
}

type netboxVlanREST struct {
	ID    uint   `json:"id"`
	VID   int    `json:"vid"`
	Name  string `json:"name"`
	Group *struct {
		ID uint `json:"id"`
	} `json:"group"`
}

func (v *netboxVlanREST) toNBVlan() *NBVlan {
	vlan := &NBVlan{NetboxID: v.ID, VID: v.VID, Name: v.Name}
	if v.Group != nil {
		vlan.GroupID = v.Group.ID
	}
	return vlan
}

// GetVlan looks up an existing VLAN by VID within groupID, returning nil (no
// error) if none exists.
func (nb *NetboxClient) GetVlan(vid int, groupID uint) (*NBVlan, error) {
	var page struct {
		Results []netboxVlanREST `json:"results"`
	}
	endpoint := fmt.Sprintf("/api/ipam/vlans/?vid=%d&group_id=%d", vid, groupID)
	if err := nb.restGet(endpoint, &page); err != nil {
		return nil, err
	}
	if len(page.Results) == 0 {
		return nil, nil
	}
	return page.Results[0].toNBVlan(), nil
}

// CreateVlan creates a new VLAN in groupID.
func (nb *NetboxClient) CreateVlan(vid int, name string, groupID uint) (*NBVlan, error) {
	payload := map[string]any{
		"vid":   vid,
		"name":  name,
		"group": groupID,
	}
	var result netboxVlanREST
	if err := nb.restPost("/api/ipam/vlans/", payload, &result); err != nil {
		return nil, err
	}
	return result.toNBVlan(), nil
}

// UpdateVlan applies changes (field -> new value) to an existing VLAN.
func (nb *NetboxClient) UpdateVlan(id uint, changes map[string]any) error {
	return nb.restPatch("/api/ipam/vlans/"+strconv.FormatUint(uint64(id), 10)+"/", changes)
}
