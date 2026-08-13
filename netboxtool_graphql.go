package netboxtool

//
// Library to manage Netbox
//
// Each query below fetches a device/VM together with its related objects
// (device_type/role/site/platform, interfaces, and each interface's ip
// addresses) in a single nested GraphQL query, instead of fetching each
// table flat and stitching the results together in Go.
//

var deviceListGraphQLbody = `
{
    device_list{{.filter}} {
        id
        name
        comments
        status
        latitude
        longitude
        device_type {
            id
            model
            manufacturer {
                id
                name
            }
        }
        role {
            id
            name
        }
        site {
            id
            name
            latitude
            longitude
        }
        primary_ip4 {
            id
            address
        }
        primary_ip6 {
            id
            address
        }
        platform {
        	id
        	name
        }
        tags {
        	id
         	name
        }
        custom_fields
        interfaces {
            id
            name
            description
            type
            mode
            label
            parent {
                id
            }
            vrf {
                id
                name
            }
            cable {
                id
            }
            untagged_vlan {
                id
                vid
            }
            tagged_vlans {
                id
                vid
            }
            qinq_svlan {
                id
                vid
            }
            tags {
                id
                name
            }
            custom_fields
            ip_addresses {
                id
                address
                role
                vrf {
                    id
                    name
                }
            }
        }
    }
}
`

// deviceTypeGraphQLbody fetches a device type together with its interface
// templates in a single nested query (interfacetemplates is resolved
// server-side, same as the interfaces field on device_list/
// virtual_machine_list above).
var deviceTypeGraphQLbody = `
{
    device_type_list{{.filter}} {
        id
        model
        manufacturer {
            id
            name
        }
        default_platform {
            id
            name
        }
        custom_fields
        interfacetemplates {
            id
            name
            description
            type
        }
    }
}
`

// manufacturerListGraphQLbody fetches manufacturers, filterable by name or
// id (see GetManufacturer).
// tenantListGraphQLbody fetches tenants, filterable by name or id (see
// GetTenants).
var tenantListGraphQLbody = `
{
    tenant_list{{.filter}} {
        id
        name
        slug
        custom_fields
    }
}
`

// siteListGraphQLbody fetches every dcim.Site, for callers that need the
// full site inventory rather than just the sites referenced by a device
// (see GetSites).
var siteListGraphQLbody = `
{
    site_list{{.filter}} {
        id
        name
        latitude
        longitude
    }
}
`

var manufacturerListGraphQLbody = `
{
    manufacturer_list{{.filter}} {
        id
        name
        description
        comments
    }
}
`

var virtualMachineListGraphQLbody = `
{
  	virtual_machine_list{{.filter}} {
      	id
       	name
        comments
        status
        site {
        	id
        	name
        }
        primary_ip4 {
	        id
	        address
        }
        primary_ip6 {
	        id
	        address
        }
      tags {
          id
	      name
      }
      custom_fields
      interfaces {
          id
          name
          description
          tags {
              id
              name
          }
          custom_fields
          ip_addresses {
              id
              address
          }
      }
    }
}
`

//
// Flat query variants, used by GetDevices/GetVMs (via
// getDevicesFlat/getVMsFlat in netboxtool_flat.go) instead of the nested
// bodies above. Netbox's GraphQL resolver appears to do per-parent-row work
// when resolving a nested one-to-many field - deviceListGraphQLbody's
// interfaces{ip_addresses{...}} in particular, benchmarked (see
// cmd/benchmark.go) at costing roughly as much as the rest of the query
// combined once an inventory reaches a few thousand interfaces. Splitting
// device_list/interface_list/ip_address_list into separate top-level
// queries - stitched together in Go, then fed through the same
// parseDevices used for the nested variant - measured ~8x faster on a
// 1400-device/37000-interface instance, with no change in output shape.
//

// flatDeviceListGraphQLbody is deviceListGraphQLbody without the nested
// interfaces selection - see getDevicesFlat.
var flatDeviceListGraphQLbody = `
{
    device_list{{.filter}} {
        id
        name
        comments
        status
        latitude
        longitude
        device_type {
            id
            model
            manufacturer {
                id
                name
            }
        }
        role {
            id
            name
        }
        site {
            id
            name
            latitude
            longitude
        }
        primary_ip4 {
            id
            address
        }
        primary_ip6 {
            id
            address
        }
        platform {
        	id
        	name
        }
        tags {
        	id
         	name
        }
        custom_fields
    }
}
`

// interfaceListGraphQLbody is deviceListGraphQLbody's interfaces selection,
// promoted to a top-level query - same fields, minus ip_addresses (fetched
// separately via ipAddressListGraphQLbody), plus a device back-reference
// (implicit before, from nesting) used to stitch interfaces back onto their
// device.
var interfaceListGraphQLbody = `
{
    interface_list{{.filter}} {
        id
        name
        description
        type
        mode
        label
        device {
            id
        }
        parent {
            id
        }
        vrf {
            id
            name
        }
        cable {
            id
        }
        untagged_vlan {
            id
            vid
        }
        tagged_vlans {
            id
            vid
        }
        qinq_svlan {
            id
            vid
        }
        tags {
            id
            name
        }
        custom_fields
    }
}
`

// flatVirtualMachineListGraphQLbody is virtualMachineListGraphQLbody
// without the nested interfaces selection - see getVMsFlat.
var flatVirtualMachineListGraphQLbody = `
{
  	virtual_machine_list{{.filter}} {
      	id
       	name
        comments
        status
        site {
        	id
        	name
        }
        primary_ip4 {
	        id
	        address
        }
        primary_ip6 {
	        id
	        address
        }
      tags {
          id
	      name
      }
      custom_fields
    }
}
`

// vmInterfaceListGraphQLbody is virtualMachineListGraphQLbody's interfaces
// selection, promoted to a top-level query - same fields, minus
// ip_addresses (fetched separately), plus a virtual_machine back-reference.
// The query field is vm_interface_list (underscore) - confirmed the hard
// way, Netbox 4.5.9 rejected a first guess of "vminterface_list".
var vmInterfaceListGraphQLbody = `
{
    vm_interface_list{{.filter}} {
        id
        name
        description
        virtual_machine {
            id
        }
        tags {
            id
            name
        }
        custom_fields
    }
}
`

// ipAddressListGraphQLbody fetches every ipam.IPAddress, resolving its
// owning object through assigned_object - a polymorphic union
// (IPAddressAssignmentType, confirmed via GraphQL schema introspection
// against a real Netbox 4.5.9 instance) that needs one inline fragment per
// concrete type it can be. Only InterfaceType (dcim, physical devices) and
// VMInterfaceType (virtualization) are selected; the union's third member,
// FHRPGroupType (virtual HSRP/VRRP addresses with no real owning
// interface), is out of scope for GetDevices/GetVMs and left unmatched by
// stitchDevices/stitchVMs. Fetches role/vrf for every address, including
// virtual-machine ones - virtualMachineListGraphQLbody's own nested
// ip_addresses above never requested those, so this is a small accuracy
// improvement for VM addresses as a side effect of the two device/VM paths
// now sharing one address query.
var ipAddressListGraphQLbody = `
{
    ip_address_list{{.filter}} {
        id
        address
        role
        vrf {
            id
            name
        }
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
