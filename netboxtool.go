package netboxtool

//
// Library to manage Netbox
//

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
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
	// All is the raw custom_fields object, including keys that also have
	// typed fields above. Callers that need an instance-specific field
	// this package does not model read it from All (or from
	// NBDevice.CustomFields, which is a copy).
	All map[string]any `json:"-"`
}

func (c *NetboxCustomFields) UnmarshalJSON(data []byte) error {
	type known NetboxCustomFields
	var k known
	if err := json.Unmarshal(data, &k); err != nil {
		return err
	}
	*c = NetboxCustomFields(k)
	return json.Unmarshal(data, &c.All)
}

// InterfaceCustomFields is dcim.Interface.custom_fields. interface_role is
// the one field this package copies onto NBInterface.CfRole; everything
// else is available via All / NBInterface.CustomFields.
type InterfaceCustomFields struct {
	InterfaceRole string         `json:"interface_role"`
	All           map[string]any `json:"-"`
}

func (c *InterfaceCustomFields) UnmarshalJSON(data []byte) error {
	type known struct {
		InterfaceRole string `json:"interface_role"`
	}
	var k known
	if err := json.Unmarshal(data, &k); err != nil {
		return err
	}
	c.InterfaceRole = k.InterfaceRole
	return json.Unmarshal(data, &c.All)
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
	Enabled     bool   `json:"enabled"`
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
	QinQSVlan    *NetboxVlanRef        `json:"qinq_svlan"`
	Tags         []NetboxTag           `json:"tags"`
	IPAddresses  []NetboxAddress       `json:"ip_addresses"`
	CustomFields InterfaceCustomFields `json:"custom_fields"`
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
		dbdevice.CfParents = device.CF.Parents
		dbdevice.CustomFields = device.CF.All
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
				NetboxID:     jintf.ID,
				Name:         jintf.Name,
				Description:  jintf.Description,
				Enabled:      jintf.Enabled,
				Type:         jintf.Type,
				Mode:         jintf.Mode,
				Label:        jintf.Label,
				CfRole:       jintf.CustomFields.InterfaceRole,
				CustomFields: jintf.CustomFields.All,
				Tags:         netboxTagsToNBTags(jintf.Tags),
				Addresses:    addresses,
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

// NetboxDeviceREST is a dcim.Device row as returned by the REST API
// (unlike JSONDevice, whose `id,string` tag matches the GraphQL ID scalar,
// this one's id is a plain JSON number).
type NetboxDeviceREST struct {
	ID   uint   `json:"id"`
	Name string `json:"name"`
}

// CreateDevice creates a physical device. extra may set site, device_type,
// role, platform, status, tags, custom_fields, and any other writable
// device field alongside name - same shape as CreateTenant.
func (nb *NetboxClient) CreateDevice(name string, extra map[string]any) (*NetboxDeviceREST, error) {
	payload := map[string]any{"name": name}
	for k, v := range extra {
		payload[k] = v
	}
	var result NetboxDeviceREST
	if err := nb.restPost("/api/dcim/devices/", payload, &result); err != nil {
		return nil, err
	}
	return &result, nil
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
	return nb.restPatchOut(endpoint, payload, nil)
}

func (nb *NetboxClient) restPatchOut(endpoint string, payload, out any) error {
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
	if out == nil {
		return nil
	}
	return json.Unmarshal(respData, out)
}

// restGet sends a GET to a Netbox REST endpoint and unmarshals the response
// into out.
func (nb *NetboxClient) restGet(endpoint string, out any) error {
	found, err := nb.restGetOptional(endpoint, out)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("netbox GET %s failed: 404 Not Found", endpoint)
	}
	return nil
}

// restGetOptional is restGet that treats HTTP 404 as (false, nil) rather
// than an error — used when the caller has to distinguish "Netbox has
// already deleted this object" from a transport/API failure.
func (nb *NetboxClient) restGetOptional(endpoint string, out any) (bool, error) {
	url := nb.P.URL + endpoint

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Token "+nb.P.Token)

	resp, err := nb.httpClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("netbox GET %s: %w", endpoint, err)
	}
	defer resp.Body.Close()

	respData, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, err
	}
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, fmt.Errorf("netbox GET %s failed: %s: %s", endpoint, resp.Status, string(respData))
	}
	if err := json.Unmarshal(respData, out); err != nil {
		return false, err
	}
	return true, nil
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

// NetboxNamedRef is a REST row that has id/name/slug - sites, device
// roles, platforms and tags all share this shape. GraphQL IDs are strings;
// REST IDs are numbers, so this is distinct from NetboxSite/NetboxRole.
type NetboxNamedRef struct {
	ID   uint   `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

type netboxNamedListREST struct {
	Results []NetboxNamedRef `json:"results"`
}

func (nb *NetboxClient) restGetNamed(endpoint string) (*NetboxNamedRef, error) {
	var page netboxNamedListREST
	if err := nb.restGet(endpoint, &page); err != nil {
		return nil, err
	}
	if len(page.Results) == 0 {
		return nil, nil
	}
	return &page.Results[0], nil
}

// GetSite fetches one site by id via REST. Returns nil, nil if the site is
// gone, is the placeholder "Default" site, or has no coordinates — the
// same filter GetSites applies, so a webhook create/update of an unplottable
// site is a no-op (or a delete of a previously-plotted row).
func (nb *NetboxClient) GetSite(id uint) (*NetboxSite, error) {
	var row struct {
		ID        uint     `json:"id"`
		Name      string   `json:"name"`
		Latitude  *float64 `json:"latitude"`
		Longitude *float64 `json:"longitude"`
	}
	found, err := nb.restGetOptional("/api/dcim/sites/"+strconv.FormatUint(uint64(id), 10)+"/", &row)
	if err != nil {
		return nil, err
	}
	if !found || row.Name == "Default" || row.Latitude == nil || row.Longitude == nil {
		return nil, nil
	}
	lat, lng := NBDecimal(*row.Latitude), NBDecimal(*row.Longitude)
	return &NetboxSite{ID: row.ID, Name: row.Name, Latitude: &lat, Longitude: &lng}, nil
}

// GetSiteByName looks up a site by its exact display name, including the
// placeholder "Default" site that GetSites skips. Returns nil, nil if none
// matches.
func (nb *NetboxClient) GetSiteByName(name string) (*NetboxNamedRef, error) {
	return nb.restGetNamed("/api/dcim/sites/?name=" + url.QueryEscape(name))
}

// GetDeviceRoleBySlug looks up a device role by slug. Returns nil, nil if
// none matches.
func (nb *NetboxClient) GetDeviceRoleBySlug(slug string) (*NetboxNamedRef, error) {
	return nb.restGetNamed("/api/dcim/device-roles/?slug=" + url.QueryEscape(slug))
}

// GetPlatformBySlug looks up a platform by slug. Returns nil, nil if none
// matches.
func (nb *NetboxClient) GetPlatformBySlug(slug string) (*NetboxNamedRef, error) {
	return nb.restGetNamed("/api/dcim/platforms/?slug=" + url.QueryEscape(slug))
}

// GetTagBySlug looks up a tag by slug. Returns nil, nil if none matches.
func (nb *NetboxClient) GetTagBySlug(slug string) (*NetboxNamedRef, error) {
	return nb.restGetNamed("/api/extras/tags/?slug=" + url.QueryEscape(slug))
}

// EnsureTag returns the tag with the given slug, creating it (name+slug)
// if it does not already exist.
func (nb *NetboxClient) EnsureTag(name, slug string) (*NetboxNamedRef, error) {
	tag, err := nb.GetTagBySlug(slug)
	if err != nil || tag != nil {
		return tag, err
	}
	var created NetboxNamedRef
	if err := nb.restPost("/api/extras/tags/", map[string]any{
		"name": name,
		"slug": slug,
	}, &created); err != nil {
		return nil, err
	}
	return &created, nil
}

type restCustomField struct {
	ID          uint     `json:"id"`
	Name        string   `json:"name"`
	Type        any      `json:"type"`
	Label       string   `json:"label"`
	Description string   `json:"description"`
	Required    bool     `json:"required"`
	GroupName   string   `json:"group_name"`
	ObjectTypes []string `json:"object_types"`
	ChoiceSet   any      `json:"choice_set"`
}

func (r restCustomField) toNBCustomField() *NBCustomField {
	cf := &NBCustomField{
		NetboxID:    r.ID,
		Name:        r.Name,
		Label:       r.Label,
		Description: r.Description,
		Required:    r.Required,
		GroupName:   r.GroupName,
		ObjectTypes: r.ObjectTypes,
	}
	switch t := r.Type.(type) {
	case string:
		cf.Type = t
	case map[string]any:
		if s, ok := t["value"].(string); ok {
			cf.Type = s
		}
	}
	cf.ChoiceSetID = parseChoiceSetID(r.ChoiceSet)
	return cf
}

func parseChoiceSetID(v any) uint {
	switch t := v.(type) {
	case float64:
		return uint(t)
	case map[string]any:
		switch id := t["id"].(type) {
		case float64:
			return uint(id)
		}
	}
	return 0
}

type restCustomFieldList struct {
	Results []restCustomField `json:"results"`
}

// GetCustomField looks up extras.CustomField by exact name via REST.
// Returns nil, nil if none matches.
func (nb *NetboxClient) GetCustomField(name string) (*NBCustomField, error) {
	var page restCustomFieldList
	if err := nb.restGet("/api/extras/custom-fields/?name="+url.QueryEscape(name), &page); err != nil {
		return nil, err
	}
	if len(page.Results) == 0 {
		return nil, nil
	}
	return page.Results[0].toNBCustomField(), nil
}

// RequireCustomField returns an error unless a custom field with the
// given name exists and is assigned to every object type in objectTypes
// (e.g. "dcim.device"). Callers that only care that the field exists can
// pass no object types.
func (nb *NetboxClient) RequireCustomField(name string, objectTypes ...string) error {
	cf, err := nb.GetCustomField(name)
	if err != nil {
		return err
	}
	if cf == nil {
		return fmt.Errorf("netbox custom field %q is not defined", name)
	}
	var missing []string
	for _, want := range objectTypes {
		if !cf.AssignedTo(want) {
			missing = append(missing, want)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("netbox custom field %q is not assigned to %s", name, strings.Join(missing, ", "))
	}
	return nil
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

// GetDevices fetches every physical device, via getDevicesFlat's
// flat-queries-fetched-concurrently strategy (netboxtool_flat.go) rather
// than GetDevices_'s single nested query - benchmarked at ~8x faster on a
// 1400-device/37000-interface instance (see cmd/benchmark.go). A
// name/id-filtered single-device lookup doesn't have the same problem, so
// GetDevice still goes through GetDevices_.
func (nb *NetboxClient) GetDevices() ([]*NBDevice, error) {
	return nb.getDevicesFlat()
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

// GetVMs is GetDevices' virtual-machine equivalent - see getVMsFlat.
func (nb *NetboxClient) GetVMs() ([]*NBDevice, error) {
	return nb.getVMsFlat()
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

// GetInterfaceCable is GetCable for the webhook/single-object path: a 404
// or a cable that is not exactly one dcim.interface on each end returns
// nil, nil (factum's Connection table cannot represent those).
func (nb *NetboxClient) GetInterfaceCable(cableID uint) (*NBCable, error) {
	var result netboxCableREST
	found, err := nb.restGetOptional("/api/dcim/cables/"+strconv.FormatUint(uint64(cableID), 10)+"/", &result)
	if err != nil {
		return nil, err
	}
	if !found || !result.isInterfaceToInterface() {
		return nil, nil
	}
	return result.toNBCable(), nil
}

// CreateCable creates a cable directly connecting two interfaces.
func (nb *NetboxClient) CreateCable(aInterfaceID, bInterfaceID uint) (*NBCable, error) {
	payload := map[string]any{
		"a_terminations": []netboxCableTermination{{ObjectType: "dcim.interface", ObjectID: aInterfaceID}},
		"b_terminations": []netboxCableTermination{{ObjectType: "dcim.interface", ObjectID: bInterfaceID}},
		"label":          "lldp",
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

// ---------------------------------------------------------------------------
//  L2VPN / L2VPN termination
// ---------------------------------------------------------------------------

type netboxL2VPNREST struct {
	ID         uint   `json:"id"`
	Name       string `json:"name"`
	Slug       string `json:"slug"`
	Identifier int    `json:"identifier"`
	Type       struct {
		Value string `json:"value"`
	} `json:"type"`
}

func (l *netboxL2VPNREST) toNBL2VPN() *NBL2VPN {
	return &NBL2VPN{
		NetboxID:   l.ID,
		Name:       l.Name,
		Slug:       l.Slug,
		Type:       l.Type.Value,
		Identifier: l.Identifier,
	}
}

// CreateL2VPN creates a new L2VPN (e.g. an ELINE service), identified by
// identifier (the pseudowire ID) - l2vpnType is one of Netbox's L2VPN type
// choices, e.g. "evpl". identifier is omitted from the payload when 0
// (Netbox treats it as optional; same-device patches often have no PWID).
func (nb *NetboxClient) CreateL2VPN(name, slug, l2vpnType string, identifier int) (*NBL2VPN, error) {
	payload := map[string]any{
		"name": name,
		"slug": slug,
		"type": l2vpnType,
	}
	if identifier > 0 {
		payload["identifier"] = identifier
	}
	var result netboxL2VPNREST
	if err := nb.restPost("/api/vpn/l2vpns/", payload, &result); err != nil {
		return nil, err
	}
	return result.toNBL2VPN(), nil
}

// GetL2VPN fetches one L2VPN by id.
func (nb *NetboxClient) GetL2VPN(l2vpnID uint) (*NBL2VPN, error) {
	var result netboxL2VPNREST
	if err := nb.restGet("/api/vpn/l2vpns/"+strconv.FormatUint(uint64(l2vpnID), 10)+"/", &result); err != nil {
		return nil, err
	}
	return result.toNBL2VPN(), nil
}

// GetL2VPNs fetches every L2VPN in Netbox via the REST API, paginating
// until "next" is null. When l2vpnType is non-empty (e.g. "evpl"), only
// that type is returned - used by factum-netbox's reverse import of
// device-synced ELINEs onto Service rows.
func (nb *NetboxClient) GetL2VPNs(l2vpnType string) ([]*NBL2VPN, error) {
	var out []*NBL2VPN
	endpoint := "/api/vpn/l2vpns/?limit=" + strconv.Itoa(restPageSize)
	if l2vpnType != "" {
		endpoint += "&type=" + url.QueryEscape(l2vpnType)
	}
	for endpoint != "" {
		var page struct {
			Next    *string           `json:"next"`
			Results []netboxL2VPNREST `json:"results"`
		}
		if err := nb.restGet(endpoint, &page); err != nil {
			return nil, err
		}
		for i := range page.Results {
			out = append(out, page.Results[i].toNBL2VPN())
		}
		if page.Next == nil {
			break
		}
		next, err := url.Parse(*page.Next)
		if err != nil {
			return nil, fmt.Errorf("netbox l2vpns: parse next page url: %w", err)
		}
		endpoint = "/api/vpn/l2vpns/?" + next.RawQuery
	}
	return out, nil
}

// UpdateL2VPN applies changes (field -> new value) to an existing L2VPN.
func (nb *NetboxClient) UpdateL2VPN(l2vpnID uint, changes map[string]any) error {
	return nb.restPatch("/api/vpn/l2vpns/"+strconv.FormatUint(uint64(l2vpnID), 10)+"/", changes)
}

// DeleteL2VPN deletes an L2VPN.
func (nb *NetboxClient) DeleteL2VPN(l2vpnID uint) error {
	return nb.restDelete("/api/vpn/l2vpns/" + strconv.FormatUint(uint64(l2vpnID), 10) + "/")
}

type netboxL2VPNTerminationREST struct {
	ID    uint `json:"id"`
	L2VPN struct {
		ID uint `json:"id"`
	} `json:"l2vpn"`
	// AssignedObjectID is the flat REST field; Object is the nested
	// expansion some Netbox versions return on detail endpoints. Either
	// may be populated depending on the call - toNBL2VPNTermination
	// prefers Object.ID when set.
	AssignedObjectID uint `json:"assigned_object_id"`
	Object           struct {
		ID uint `json:"id"`
	} `json:"assigned_object"`
}

func (t *netboxL2VPNTerminationREST) toNBL2VPNTermination() *NBL2VPNTermination {
	ifaceID := t.Object.ID
	if ifaceID == 0 {
		ifaceID = t.AssignedObjectID
	}
	return &NBL2VPNTermination{
		NetboxID:    t.ID,
		L2VPNID:     t.L2VPN.ID,
		InterfaceID: ifaceID,
	}
}

// GetL2VPNByName looks up an existing L2VPN by its exact name, returning
// nil (no error) if none exists. Used by device-sync's ELINE phase to
// reuse a service-provisioned (or previously synced) L2VPN rather than
// creating a duplicate.
func (nb *NetboxClient) GetL2VPNByName(name string) (*NBL2VPN, error) {
	var page struct {
		Results []netboxL2VPNREST `json:"results"`
	}
	if err := nb.restGet("/api/vpn/l2vpns/?name="+url.QueryEscape(name), &page); err != nil {
		return nil, err
	}
	if len(page.Results) == 0 {
		return nil, nil
	}
	return page.Results[0].toNBL2VPN(), nil
}

// GetL2VPNByIdentifier looks up an existing L2VPN by its identifier
// (pseudowire ID), returning nil (no error) if none exists. Fallback for
// device-sync when the on-device ELINE name doesn't match the Netbox
// L2VPN name but the pseudowire ID does (e.g. a service-provisioned
// "CN00570" L2VPN seen on a device under a slightly different patch name).
func (nb *NetboxClient) GetL2VPNByIdentifier(identifier int) (*NBL2VPN, error) {
	if identifier == 0 {
		return nil, nil
	}
	var page struct {
		Results []netboxL2VPNREST `json:"results"`
	}
	if err := nb.restGet("/api/vpn/l2vpns/?identifier="+strconv.Itoa(identifier), &page); err != nil {
		return nil, err
	}
	if len(page.Results) == 0 {
		return nil, nil
	}
	return page.Results[0].toNBL2VPN(), nil
}

// GetL2VPNTerminations returns every termination on l2vpnID (typically 1-2
// for an EVPL). Paginated the same way as GetCables in case a multipoint
// L2VPN ever lands here with more than one page of ends.
func (nb *NetboxClient) GetL2VPNTerminations(l2vpnID uint) ([]*NBL2VPNTermination, error) {
	var terms []*NBL2VPNTermination
	endpoint := "/api/vpn/l2vpn-terminations/?l2vpn_id=" +
		strconv.FormatUint(uint64(l2vpnID), 10) +
		"&limit=" + strconv.Itoa(restPageSize)
	for endpoint != "" {
		var page struct {
			Next    *string                      `json:"next"`
			Results []netboxL2VPNTerminationREST `json:"results"`
		}
		if err := nb.restGet(endpoint, &page); err != nil {
			return nil, err
		}
		for i := range page.Results {
			terms = append(terms, page.Results[i].toNBL2VPNTermination())
		}
		if page.Next == nil {
			break
		}
		next, err := url.Parse(*page.Next)
		if err != nil {
			return nil, fmt.Errorf("netbox l2vpn terminations: parse next page url: %w", err)
		}
		endpoint = "/api/vpn/l2vpn-terminations/?" + next.RawQuery
	}
	return terms, nil
}

// CreateL2VPNTermination terminates l2vpnID on interfaceID - unlike a
// cable's a_terminations/b_terminations (object_type/object_id), an L2VPN
// termination uses Netbox's generic "assigned object" fields
// (assigned_object_type/assigned_object_id).
func (nb *NetboxClient) CreateL2VPNTermination(l2vpnID, interfaceID uint) (*NBL2VPNTermination, error) {
	payload := map[string]any{
		"l2vpn":                l2vpnID,
		"assigned_object_type": "dcim.interface",
		"assigned_object_id":   interfaceID,
	}
	var result netboxL2VPNTerminationREST
	if err := nb.restPost("/api/vpn/l2vpn-terminations/", payload, &result); err != nil {
		return nil, err
	}
	return result.toNBL2VPNTermination(), nil
}

// DeleteL2VPNTermination deletes an L2VPN termination.
func (nb *NetboxClient) DeleteL2VPNTermination(terminationID uint) error {
	return nb.restDelete("/api/vpn/l2vpn-terminations/" + strconv.FormatUint(uint64(terminationID), 10) + "/")
}
