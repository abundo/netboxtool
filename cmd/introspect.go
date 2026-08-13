package main

//
// One-off GraphQL schema introspection, used to find the real type names
// behind IPAddress.assigned_object (a polymorphic union - could be an
// Interface, a VMInterface, or a handful of other object types) before
// writing a flat top-level ip_address_list query for it. Guessing the
// union's member type names risked a query that simply errors against a
// real schema, so this asks Netbox directly instead. Read-only: this only
// queries __type/__schema, never any real data.
//

import (
	"encoding/json"
	"fmt"

	"github.com/abundo/netboxtool"
)

const introspectIPAddressTypeQuery = `
{
  __type(name: "IPAddressType") {
    name
    fields {
      name
      type {
        kind
        name
        ofType {
          kind
          name
          ofType {
            kind
            name
            possibleTypes { name }
          }
        }
      }
    }
  }
}
`

// introspectAssignmentUnionQuery finds assigned_object's concrete member
// types (e.g. InterfaceType, VMInterfaceType) and confirms each exposes an
// "id" field, once introspectIPAddressTypeQuery has revealed the union's
// name (IPAddressAssignmentType, as of Netbox 4.5.9).
const introspectAssignmentUnionQuery = `
{
  __type(name: "IPAddressAssignmentType") {
    possibleTypes {
      name
      fields { name }
    }
  }
}
`

// printGraphQL runs query (an introspection query - no {{.filter}} needed)
// and pretty-prints the raw JSON response.
func printGraphQL(nb *netboxtool.NetboxClient, query string) error {
	respData, err := nb.NetboxAPICall(query, "", -1, 0, 0)
	if err != nil {
		return err
	}
	var pretty map[string]any
	if err := json.Unmarshal(respData, &pretty); err != nil {
		return fmt.Errorf("decoding introspection response: %w", err)
	}
	out, err := json.MarshalIndent(pretty, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(out))
	return nil
}

// RunIntrospectAddressAssignment prints IPAddressType's field list (with a
// focus on assigned_object's shape - kind/name, and if it's a union, its
// possibleTypes) followed by that union's concrete member types and their
// fields. If "IPAddressType" isn't the right root type name on this Netbox
// version, the first query prints a null __type and this stops there rather
// than guessing further.
func RunIntrospectAddressAssignment(cfg netboxtool.ConfigNetbox) error {
	nb, err := netboxtool.NewNetboxClient(cfg)
	if err != nil {
		return err
	}
	if err := printGraphQL(nb, introspectIPAddressTypeQuery); err != nil {
		return err
	}
	fmt.Println()
	return printGraphQL(nb, introspectAssignmentUnionQuery)
}
