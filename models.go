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
	VM                 bool
	NetboxID           uint          `json:"netbox_id"`
	Name               string        `json:"name" gorm:"type:varchar(255)"`
	Comments           string        `json:"comments" gorm:"type:varchar(255)"`
	Enabled            bool          `json:"enabled"`
	Manufacturer       string        `json:"manufacturer" gorm:"type:varchar(255)"`
	ManufacturerID     uint          `json:"manufacturer_id"`
	ModelName          string        `json:"model_name" gorm:"type:varchar(255)"`
	ModelID            uint          `json:"model_id"`
	Platform           string        `json:"platform" gorm:"type:varchar(255)"`
	PlatformID         uint          `json:"platform_id"`
	PrimaryIPv4        string        `json:"primary_ipv4" gorm:"type:varchar(255)"`
	PrimaryIPv4ID      uint          `json:"primary_ipv4_id"`
	PrimaryIPv6        string        `json:"primary_ipv6" gorm:"type:varchar(255)"`
	PrimaryIPv6ID      uint          `json:"primary_ipv6_id"`
	Role               string        `json:"role" gorm:"type:varchar(255)"`
	RoleID             uint          `json:"role_id"`
	Site               string        `json:"site" gorm:"type:varchar(255)"`
	SiteID             uint          `json:"site_id"`
	Status             string        `json:"status" gorm:"type:varchar(255)"`
	CfAlarmTimeperiod  string        `json:"cf_alarm_timeperiod" gorm:"type:varchar(255)"`
	CfAlarmDestination string        `json:"cf_alarm_destination" gorm:"type:varchar(255)"`
	CfAlarmInterfaces  bool          `json:"cf_alarm_interfaces"`
	CfBackupOxidized   bool          `json:"cf_backup_oxidized"`
	CfConnectionMethod string        `json:"cf_connection_method" gorm:"type:varchar(255)"`
	CfLocation         string        `json:"cf_location" gorm:"type:varchar(255)"`
	CfMonitorGrafana   bool          `json:"cf_monitor_grafana"`
	CfMonitorIcinga    bool          `json:"cf_monitor_icinga"`
	CfMonitorLibrenms  bool          `json:"cf_monitor_librenms"`
	CfSource           string        `json:"cf_source" gorm:"type:varchar(255)"`
	CfSourceID         uint          `json:"cf_source_id"`
	Interfaces         []NBInterface `json:"interfaces"` // gorm:"constraint:OnUpdate:CASCADE,OnDelete:SET NULL;"`
	Tags               []NBTag       `json:"tags"`
}

type NBInterface struct {
	NBModel
	NBDeviceID  uint   `json:"nbdevice_id"`
	NetboxID    uint   `json:"netbox_id"`
	Name        string `json:"name" gorm:"type:varchar(255)"`
	Description string `json:"description" gorm:"type:varchar(255)"`
	Enabled     bool   `json:"enabled"`
	VRF         string `json:"vrf" gorm:"type:varchar(255)"`
	CfRole      string `json:"cf_role" gorm:"type:varchar(255)"`
	// runtime data
	LineProtocolStatus string `gorm:"-"`
	InterfaceStatus    string `gorm:"-"`

	Addresses []NBAddress `json:"addresses"` // gorm:"constraint:OnUpdate:CASCADE,OnDelete:SET NULL;"`
	Tags      []NBTag     `json:"tags"`
}

type NBAddress struct {
	NBModel
	NBAddressID   uint   `json:"address_id"`
	NBInterfaceID uint   `json:"interface_id"`
	NetboxID      uint   `json:"netbox_id"`
	Address       string `gorm:"type:varchar(80)"`
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
