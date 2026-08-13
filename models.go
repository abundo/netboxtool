package netboxtool

import "time"

type NBModel struct {
	ID        uint      `json:"id" gorm:"primarykey"`
	CreatedAt time.Time `json:"-"`
	UpdatedAt time.Time `json:"-"`
}

// --------------------------------------------------------------------------
//	Netbox
// --------------------------------------------------------------------------

type NBDevice struct {
	NBModel
	VM             bool
	NetboxID       uint   `json:"netbox_id"`
	Name           string `json:"name" gorm:"type:varchar(255)"`
	Comments       string `json:"comments" gorm:"type:varchar(255)"`
	Enabled        bool   `json:"enabled"`
	Manufacturer   string `json:"manufacturer" gorm:"type:varchar(255)"`
	ManufacturerID uint   `json:"manufacturer_id"`
	ModelName      string `json:"model_name" gorm:"type:varchar(255)"`
	ModelID        uint   `json:"model_id"`
	Platform       string `json:"platform" gorm:"type:varchar(255)"`
	PlatformID     uint   `json:"platform_id"`
	PrimaryIPv4    string `json:"primary_ipv4" gorm:"type:varchar(255)"`
	PrimaryIPv4ID  uint   `json:"primary_ipv4_id"`
	PrimaryIPv6    string `json:"primary_ipv6" gorm:"type:varchar(255)"`
	PrimaryIPv6ID  uint   `json:"primary_ipv6_id"`
	Role           string `json:"role" gorm:"type:varchar(255)"`
	RoleID         uint   `json:"role_id"`
	Site           string `json:"site" gorm:"type:varchar(255)"`
	SiteID         uint   `json:"site_id"`
	Status         string `json:"status" gorm:"type:varchar(255)"`
	// Latitude/Longitude are the device's own GPS coordinates if set in
	// Netbox, else inherited from its site (resolved in parseDevices) -
	// nil if neither the device nor its site has coordinates.
	Latitude           *float64 `json:"latitude"`
	Longitude          *float64 `json:"longitude"`
	CfAlarmTimeperiod  string   `json:"cf_alarm_timeperiod" gorm:"type:varchar(255)"`
	CfAlarmDestination string   `json:"cf_alarm_destination" gorm:"type:varchar(255)"`
	CfAlarmInterfaces  bool     `json:"cf_alarm_interfaces"`
	CfBackupOxidized   bool     `json:"cf_backup_oxidized"`
	CfConnectionMethod string   `json:"cf_connection_method" gorm:"type:varchar(255)"`
	CfLocation         string   `json:"cf_location" gorm:"type:varchar(255)"`
	CfMonitorGrafana   bool     `json:"cf_monitor_grafana"`
	CfMonitorIcinga    bool     `json:"cf_monitor_icinga"`
	CfMonitorLibrenms  bool     `json:"cf_monitor_librenms"`
	CfSource           string   `json:"cf_source" gorm:"type:varchar(255)"`
	CfSourceID         uint     `json:"cf_source_id"`
	// CfParents is the comma-separated "parents" custom field.
	CfParents string `json:"cf_parents"`
	// CustomFields is the raw custom_fields object from Netbox, including
	// keys this package also copies onto typed Cf* fields. Callers that
	// need an instance-specific field not modelled here read it from here.
	CustomFields map[string]any `json:"custom_fields"`
	LibrenmsID   uint           `json:"librenms_id"`
	Interfaces   []NBInterface  `json:"interfaces"` // gorm:"constraint:OnUpdate:CASCADE,OnDelete:SET NULL;"`
	Tags         []NBTag        `json:"tags"`
}

type NBInterface struct {
	NBModel
	NBDeviceID  uint   `json:"nbdevice_id"`
	NetboxID    uint   `json:"netbox_id"`
	Name        string `json:"name" gorm:"type:varchar(255)"`
	Description string `json:"description" gorm:"type:varchar(255)"`
	Enabled     bool   `json:"enabled"`
	Type        string `json:"type" gorm:"type:varchar(255)"`
	VRF         string `json:"vrf" gorm:"type:varchar(255)"`
	CfRole      string `json:"cf_role" gorm:"type:varchar(255)"`
	// runtime data
	LineProtocolStatus string `gorm:"-"`
	InterfaceStatus    string `gorm:"-"`
	// CableID is the netbox ID of the cable terminated on this interface, 0
	// if none - populated by GraphQL reads (deviceListGraphQLbody's
	// interfaces{cable{id}}), not stored.
	CableID uint `gorm:"-"`
	// Label is dcim.Interface.label, Netbox's free-text interface label
	// (distinct from Name) - populated by GraphQL reads
	// (deviceListGraphQLbody's interfaces{label}), not stored.
	Label string `gorm:"-"`
	// ParentID is the netbox ID of this interface's parent interface, 0 if
	// none - populated by GraphQL reads (deviceListGraphQLbody's
	// interfaces{parent{id}}), not stored.
	ParentID uint `gorm:"-"`
	// UntaggedVLAN/TaggedVLANs are VIDs (not Netbox IDs), populated by
	// GraphQL reads (deviceListGraphQLbody's interfaces{untagged_vlan,
	// tagged_vlans}), not stored. UntaggedVLAN is 0 if unset.
	UntaggedVLAN int   `gorm:"-"`
	TaggedVLANs  []int `gorm:"-"`
	// Mode is dcim.Interface.mode ("access", "tagged", "tagged-all" or
	// "q-in-q"), populated by GraphQL reads (deviceListGraphQLbody's
	// interfaces{mode}), not stored. "" if the interface isn't a switchport.
	Mode string `gorm:"-"`
	// CustomFields is the raw custom_fields object from Netbox. Typed
	// fields this package knows about (currently CfRole) are also copied
	// out separately.
	CustomFields map[string]any `json:"custom_fields"`

	Addresses []NBAddress `json:"addresses"` // gorm:"constraint:OnUpdate:CASCADE,OnDelete:SET NULL;"`
	Tags      []NBTag     `json:"tags"`
}

type NBAddress struct {
	NBModel
	NBAddressID   uint   `json:"address_id"`
	NBInterfaceID uint   `json:"interface_id"`
	NetboxID      uint   `json:"netbox_id"`
	Address       string `gorm:"type:varchar(80)"`
	// Role is ipam.IPAddress.role (e.g. "anycast"), "" if unset.
	Role string `gorm:"type:varchar(80)"`
	// VRF is the name of the VRF this address belongs to, "" for the
	// global/default VRF.
	VRF string `gorm:"type:varchar(255)"`
}

// NBCable is a dcim.Cable connecting exactly two interfaces (the only shape
// internal/device-sync creates/inspects - Netbox cables can in principle
// terminate on more than two endpoints, e.g. distribution cables, which
// this type doesn't attempt to represent).
type NBCable struct {
	NetboxID   uint
	AInterface uint // interface ID on the A side
	BInterface uint // interface ID on the B side
	Label      string
}

// NBPrefix is an ipam.Prefix row.
type NBPrefix struct {
	NetboxID uint
	Prefix   string
	Status   string
}

// NBVlanGroup is an ipam.VLANGroup row - always global (no scope) for the
// callers in this codebase, which use a single VLAN group for every device.
type NBVlanGroup struct {
	NetboxID uint
	Name     string
	Slug     string
}

// NBVlan is an ipam.VLAN row, scoped to a VLAN group.
type NBVlan struct {
	NetboxID uint
	VID      int
	Name     string
	GroupID  uint
}

// NBL2VPN is an ipam.L2VPN row (e.g. an EVPL/ELINE service).
type NBL2VPN struct {
	NetboxID   uint
	Name       string
	Slug       string
	Type       string // e.g. "evpl"
	Identifier int
}

// NBL2VPNTermination is an ipam.L2VPNTermination row, binding an L2VPN to
// one assigned interface (dcim.interface).
type NBL2VPNTermination struct {
	NetboxID    uint
	L2VPNID     uint
	InterfaceID uint
}

// NBCustomField is extras.CustomField. ObjectTypes are Netbox object-type
// strings such as "dcim.device" or "dcim.interface".
type NBCustomField struct {
	NetboxID    uint
	Name        string
	Type        string // e.g. "integer", "text"
	ObjectTypes []string
}

// AssignedTo reports whether this custom field is assigned to objectType
// (e.g. "dcim.device").
func (f *NBCustomField) AssignedTo(objectType string) bool {
	if f == nil {
		return false
	}
	for _, t := range f.ObjectTypes {
		if t == objectType {
			return true
		}
	}
	return false
}

type NBTag struct {
	NBModel
	NBDeviceID    uint   `json:"device_id"`
	NBInterfaceID uint   `json:"interface_id"`
	NetboxID      uint   `json:"netbox_id"`
	Name          string `json:"name" gorm:"type:varchar(255)"`
}

type NBParent struct {
	NBModel
	NBDeviceID uint `json:"device_id"`
}

type NBTenant struct {
	NBModel
	NetboxID   uint   `json:"netbox_id"`
	Name       string `json:"name" gorm:"type:varchar(255)"`
	Slug       string `json:"slug" gorm:"type:varchar(255)"`
	CfSource   string `json:"cf_source" gorm:"type:varchar(255)"`
	CfSourceID string `json:"cf_source_id" gorm:"type:varchar(255)"`
}

// IsTag reports whether the device has a tag with the given name.
func (d *NBDevice) IsTag(name string) bool {
	return hasTag(d.Tags, name)
}

// IsTag reports whether the interface has a tag with the given name.
func (i *NBInterface) IsTag(name string) bool {
	return hasTag(i.Tags, name)
}

func hasTag(tags []NBTag, name string) bool {
	for _, t := range tags {
		if t.Name == name {
			return true
		}
	}
	return false
}
