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
