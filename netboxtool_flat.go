package netboxtool

//
// GetDevices/GetVMs' fetch strategy: three (or four, for VMs plus the
// shared address query) independent flat GraphQL queries, run
// concurrently, stitched together in Go into the same []JSONDevice{
// Interfaces: []JSONInterface{IPAddresses: [...]} } shape the old single
// nested query produced - so parseDevices, which does the actual
// device/interface/address field mapping (status, lat/long inheritance,
// custom fields, ...), needs no changes at all. Only how the raw data is
// fetched changed, not how it's interpreted.
//
// GetDevices_/GetVM_ (the name/id-filtered single-row lookups behind
// GetDevice/GetVM) deliberately still use the nested queries in
// netboxtool_graphql.go - nesting is fine, even preferable, for fetching
// one row; the flat+concurrent split here only pays for itself once a
// query is paging through thousands of interfaces.
//

import (
	"encoding/json"
	"sync"
)

// flatInterfaceRow is interface_list's flat row shape: the same fields as
// JSONInterface, minus ip_addresses (stitched in separately from a shared
// ip_address_list fetch), plus the device back-reference interface_list
// needs that the nested query never did (nesting under device_list already
// implied it).
type flatInterfaceRow struct {
	JSONInterface
	Device *struct {
		ID uint `json:"id,string"`
	} `json:"device"`
}

type flatInterfaceListPage struct {
	Data struct {
		Interfaces []flatInterfaceRow `json:"interface_list"`
	} `json:"data"`
}

// flatVMInterfaceRow is vm_interface_list's flat row shape, mirroring
// virtualMachineListGraphQLbody's (much smaller) interfaces field set.
type flatVMInterfaceRow struct {
	ID           uint        `json:"id,string"`
	Name         string      `json:"name"`
	Description  string      `json:"description"`
	Tags         []NetboxTag `json:"tags"`
	CustomFields struct {
		InterfaceRole string `json:"interface_role"`
	} `json:"custom_fields"`
	VirtualMachine *struct {
		ID uint `json:"id,string"`
	} `json:"virtual_machine"`
}

type flatVMInterfaceListPage struct {
	Data struct {
		Interfaces []flatVMInterfaceRow `json:"vm_interface_list"`
	} `json:"data"`
}

// flatAddressRow is ip_address_list's flat row shape - see
// ipAddressListGraphQLbody for the assigned_object union this resolves.
type flatAddressRow struct {
	ID             uint          `json:"id,string"`
	Address        string        `json:"address"`
	Role           string        `json:"role"`
	VRF            *NetboxVRFRef `json:"vrf"`
	AssignedObject *struct {
		Typename string `json:"__typename"`
		ID       uint   `json:"id,string"`
	} `json:"assigned_object"`
}

type flatAddressListPage struct {
	Data struct {
		Addresses []flatAddressRow `json:"ip_address_list"`
	} `json:"data"`
}

func (nb *NetboxClient) fetchFlatDevices() ([]JSONDevice, error) {
	var out []JSONDevice
	err := nb.fetchAllPages(flatDeviceListGraphQLbody, "", -1, func(respdata []byte) (int, uint, error) {
		var page JSONDevices
		if err := json.Unmarshal(respdata, &page); err != nil {
			return 0, 0, err
		}
		out = append(out, page.Data.Devices...)
		var lastID uint
		if n := len(page.Data.Devices); n > 0 {
			lastID = page.Data.Devices[n-1].ID
		}
		return len(page.Data.Devices), lastID, nil
	})
	return out, err
}

func (nb *NetboxClient) fetchFlatVMs() ([]JSONDevice, error) {
	var out []JSONDevice
	err := nb.fetchAllPages(flatVirtualMachineListGraphQLbody, "", -1, func(respdata []byte) (int, uint, error) {
		var page JSONVMs
		if err := json.Unmarshal(respdata, &page); err != nil {
			return 0, 0, err
		}
		out = append(out, page.Data.Devices...)
		var lastID uint
		if n := len(page.Data.Devices); n > 0 {
			lastID = page.Data.Devices[n-1].ID
		}
		return len(page.Data.Devices), lastID, nil
	})
	return out, err
}

func (nb *NetboxClient) fetchFlatInterfaces() ([]flatInterfaceRow, error) {
	var out []flatInterfaceRow
	err := nb.fetchAllPages(interfaceListGraphQLbody, "", -1, func(respdata []byte) (int, uint, error) {
		var page flatInterfaceListPage
		if err := json.Unmarshal(respdata, &page); err != nil {
			return 0, 0, err
		}
		out = append(out, page.Data.Interfaces...)
		var lastID uint
		if n := len(page.Data.Interfaces); n > 0 {
			lastID = page.Data.Interfaces[n-1].ID
		}
		return len(page.Data.Interfaces), lastID, nil
	})
	return out, err
}

func (nb *NetboxClient) fetchFlatVMInterfaces() ([]flatVMInterfaceRow, error) {
	var out []flatVMInterfaceRow
	err := nb.fetchAllPages(vmInterfaceListGraphQLbody, "", -1, func(respdata []byte) (int, uint, error) {
		var page flatVMInterfaceListPage
		if err := json.Unmarshal(respdata, &page); err != nil {
			return 0, 0, err
		}
		out = append(out, page.Data.Interfaces...)
		var lastID uint
		if n := len(page.Data.Interfaces); n > 0 {
			lastID = page.Data.Interfaces[n-1].ID
		}
		return len(page.Data.Interfaces), lastID, nil
	})
	return out, err
}

func (nb *NetboxClient) fetchFlatAddresses() ([]flatAddressRow, error) {
	var out []flatAddressRow
	err := nb.fetchAllPages(ipAddressListGraphQLbody, "", -1, func(respdata []byte) (int, uint, error) {
		var page flatAddressListPage
		if err := json.Unmarshal(respdata, &page); err != nil {
			return 0, 0, err
		}
		out = append(out, page.Data.Addresses...)
		var lastID uint
		if n := len(page.Data.Addresses); n > 0 {
			lastID = page.Data.Addresses[n-1].ID
		}
		return len(page.Data.Addresses), lastID, nil
	})
	return out, err
}

// stitchDevices assembles flat devices/interfaces/addresses into the same
// []JSONDevice{Interfaces: []JSONInterface{IPAddresses: [...]}} shape the
// nested device_list query used to produce directly, so parseDevices can
// consume it unchanged. Only InterfaceType-typed addresses are attached -
// VMInterfaceType/FHRPGroupType ones aren't this device's.
func stitchDevices(devices []JSONDevice, interfaces []flatInterfaceRow, addresses []flatAddressRow) []JSONDevice {
	addrsByInterface := make(map[uint][]NetboxAddress, len(addresses))
	for _, a := range addresses {
		if a.AssignedObject == nil || a.AssignedObject.Typename != "InterfaceType" {
			continue
		}
		addrsByInterface[a.AssignedObject.ID] = append(addrsByInterface[a.AssignedObject.ID], NetboxAddress{
			ID: a.ID, Address: a.Address, Role: a.Role, VRF: a.VRF,
		})
	}

	ifacesByDevice := make(map[uint][]JSONInterface, len(interfaces))
	for _, row := range interfaces {
		if row.Device == nil {
			continue
		}
		jintf := row.JSONInterface
		jintf.IPAddresses = addrsByInterface[jintf.ID]
		ifacesByDevice[row.Device.ID] = append(ifacesByDevice[row.Device.ID], jintf)
	}

	for i := range devices {
		devices[i].Interfaces = ifacesByDevice[devices[i].ID]
	}
	return devices
}

// stitchVMs is stitchDevices' virtual_machine_list/vm_interface_list
// equivalent - only VMInterfaceType-typed addresses are attached.
func stitchVMs(vms []JSONDevice, interfaces []flatVMInterfaceRow, addresses []flatAddressRow) []JSONDevice {
	addrsByInterface := make(map[uint][]NetboxAddress, len(addresses))
	for _, a := range addresses {
		if a.AssignedObject == nil || a.AssignedObject.Typename != "VMInterfaceType" {
			continue
		}
		addrsByInterface[a.AssignedObject.ID] = append(addrsByInterface[a.AssignedObject.ID], NetboxAddress{
			ID: a.ID, Address: a.Address, Role: a.Role, VRF: a.VRF,
		})
	}

	ifacesByVM := make(map[uint][]JSONInterface, len(interfaces))
	for _, row := range interfaces {
		if row.VirtualMachine == nil {
			continue
		}
		jintf := JSONInterface{
			ID:           row.ID,
			Name:         row.Name,
			Description:  row.Description,
			Tags:         row.Tags,
			CustomFields: row.CustomFields,
			IPAddresses:  addrsByInterface[row.ID],
		}
		ifacesByVM[row.VirtualMachine.ID] = append(ifacesByVM[row.VirtualMachine.ID], jintf)
	}

	for i := range vms {
		vms[i].Interfaces = ifacesByVM[vms[i].ID]
	}
	return vms
}

// getDevicesFlat is GetDevices' implementation: device_list, interface_list
// and ip_address_list fetched concurrently (independent queries - nothing
// links them until stitchDevices), then run through the same parseDevices
// the nested-query path uses.
func (nb *NetboxClient) getDevicesFlat() ([]*NBDevice, error) {
	var devices []JSONDevice
	var interfaces []flatInterfaceRow
	var addresses []flatAddressRow
	var errDevices, errInterfaces, errAddresses error

	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		devices, errDevices = nb.fetchFlatDevices()
	}()
	go func() {
		defer wg.Done()
		interfaces, errInterfaces = nb.fetchFlatInterfaces()
	}()
	go func() {
		defer wg.Done()
		addresses, errAddresses = nb.fetchFlatAddresses()
	}()
	wg.Wait()

	if errDevices != nil {
		return nil, errDevices
	}
	if errInterfaces != nil {
		return nil, errInterfaces
	}
	if errAddresses != nil {
		return nil, errAddresses
	}

	return nb.parseDevices(stitchDevices(devices, interfaces, addresses), false)
}

// getVMsFlat is getDevicesFlat's virtual_machine_list/vm_interface_list
// equivalent. Note this re-fetches ip_address_list independently of any
// getDevicesFlat call - a known, accepted duplication (a full sync calls
// both back to back) rather than adding cross-call caching to
// NetboxClient, which cache.go's own doc comment says deliberately never
// caches anything itself.
func (nb *NetboxClient) getVMsFlat() ([]*NBDevice, error) {
	var vms []JSONDevice
	var interfaces []flatVMInterfaceRow
	var addresses []flatAddressRow
	var errVMs, errInterfaces, errAddresses error

	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		vms, errVMs = nb.fetchFlatVMs()
	}()
	go func() {
		defer wg.Done()
		interfaces, errInterfaces = nb.fetchFlatVMInterfaces()
	}()
	go func() {
		defer wg.Done()
		addresses, errAddresses = nb.fetchFlatAddresses()
	}()
	wg.Wait()

	if errVMs != nil {
		return nil, errVMs
	}
	if errInterfaces != nil {
		return nil, errInterfaces
	}
	if errAddresses != nil {
		return nil, errAddresses
	}

	return nb.parseDevices(stitchVMs(vms, interfaces, addresses), true)
}
