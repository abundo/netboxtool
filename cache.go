package netboxtool

//
// Optional in-memory caching for device and device-type lookups. NetboxClient
// itself never caches - wrap it (or any other CacheSource, e.g. a test fake)
// with NewCache to get caching, for callers (such as factum2's
// internal/device-sync) that make many repeated lookups within one run and
// don't want to re-fetch or re-implement this themselves.
//

// CacheSource is the subset of NetboxClient that Cache needs.
type CacheSource interface {
	GetDevices() ([]*NBDevice, error)
	GetDevice(name string, id int) (*NBDevice, error)
	GetDeviceType(manufacturer, model string) (*NetboxDeviceTypeDetail, error)
}

// Cache wraps a CacheSource with an in-memory, per-instance cache of
// devices (keyed by name) and device-type interface templates (keyed by
// "manufacturer/model"). It is not safe for concurrent use.
type Cache struct {
	src     CacheSource
	devices map[string]*NBDevice
	// deviceTypeInterfaces caches TemplateInterfaceTypes results, keyed by
	// "manufacturer/model" - many devices in one run typically share a
	// handful of models, so this avoids one lookup per device.
	deviceTypeInterfaces map[string]map[string]string
}

func NewCache(src CacheSource) *Cache {
	return &Cache{
		src:                  src,
		devices:              make(map[string]*NBDevice),
		deviceTypeInterfaces: make(map[string]map[string]string),
	}
}

// GetDevices fetches every device and (re)primes the cache.
func (c *Cache) GetDevices() ([]*NBDevice, error) {
	devices, err := c.src.GetDevices()
	if err != nil {
		return nil, err
	}
	for _, d := range devices {
		c.devices[d.Name] = d
	}
	return devices, nil
}

// GetDevice returns one device, from cache unless refresh is set. Returns
// nil, nil (not an error) for an unknown name, matching the underlying
// source's GetDevice.
func (c *Cache) GetDevice(name string, refresh bool) (*NBDevice, error) {
	if !refresh {
		if d, ok := c.devices[name]; ok {
			return d, nil
		}
	}
	device, err := c.src.GetDevice(name, -1)
	if err != nil {
		return nil, err
	}
	if device == nil {
		delete(c.devices, name)
		return nil, nil
	}
	c.devices[name] = device
	return device, nil
}

// RefreshDevice reloads device and all its related data from the source -
// callers use this after a mutation so later cached lookups see the new
// state.
func (c *Cache) RefreshDevice(device *NBDevice) (*NBDevice, error) {
	return c.GetDevice(device.Name, true)
}

// TemplateInterfaceTypes returns the Netbox interface type (e.g.
// "1000base-t") of every interface defined by a device type's template
// (manufacturer+model), keyed by interface name. Returns nil, nil (not an
// error) when manufacturer or model is empty, since there's no template to
// look up. Results are cached per manufacturer/model for the life of the
// Cache.
func (c *Cache) TemplateInterfaceTypes(manufacturer, model string) (map[string]string, error) {
	if manufacturer == "" || model == "" {
		return nil, nil
	}
	key := manufacturer + "/" + model
	if types, ok := c.deviceTypeInterfaces[key]; ok {
		return types, nil
	}
	deviceType, err := c.src.GetDeviceType(manufacturer, model)
	if err != nil {
		return nil, err
	}
	types := make(map[string]string, len(deviceType.Interfaces))
	for _, tmpl := range deviceType.Interfaces {
		types[tmpl.Name] = tmpl.Type
	}
	c.deviceTypeInterfaces[key] = types
	return types, nil
}
