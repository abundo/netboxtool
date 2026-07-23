package main

//
// CLI for the netboxtool
//

import (
	"encoding/json"
	"fmt"

	"github.com/abundo/netboxtool"
	"github.com/GiGurra/boa/pkg/boa"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert/yaml"
)

type Params struct {
	ConfigFile string                `configfile:"true" optional:"true" default:"/etc/netboxtool/netboxtool.yaml"`
	Debug      bool                  `descr:"Enable verbose debug logging" short:"d"`
	Loglevel   string                `descr:"Set log level" alts:"error,warning,info,debug" default:"info"`
	Config     netboxtool.ConfigRoot `yaml:",inline"`
}

type ParamsDevice struct {
	Params
	Name string `descr:"Name" short:"n" optional:"true"`
	ID   int    `descr:"Device id" short:"i" optional:"true"`
}

type ParamsDeviceType struct {
	Params
	Manufacturer string
	Model        string
}
type ParamsManufacturer struct {
	Params
	Name string `descr:"Name" short:"n" optional:"true"`
	ID   int    `descr:"Manufacturer id" short:"i" optional:"true"`
}

func main() {
	boa.RegisterConfigFormat(".yaml", yaml.Unmarshal)

	boa.CmdT[struct{}]{
		Use:   "netboxtool",
		Short: "Manage netbox using REST API",
		SubCmds: boa.SubCmds(

			boa.CmdT[Params]{
				Use:   "show-config",
				Short: "Show the configuration",
				RunFuncE: func(p *Params, cmd *cobra.Command, args []string) error {
					Pprint(p.Config)
					return nil
				},
			},
			boa.CmdT[ParamsDevice]{
				Use:   "get-device",
				Short: "Get one device",
				RunFuncE: func(p *ParamsDevice, cmd *cobra.Command, args []string) error {
					if p.Name == "" && p.ID == 0 {
						fmt.Println("Error:Specify name or id")
						return nil
					}
					nbtool, err := netboxtool.NewNetboxClient(p.Config.Netbox)
					if err != nil {
						return err
					}
					data, err := nbtool.GetDevice(p.Name, p.ID)
					if err != nil {
						return err
					}
					Pprint(data)
					return nil
				},
			},

			boa.CmdT[Params]{
				Use:   "get-devices",
				Short: "Get all devices",
				RunFuncE: func(p *Params, cmd *cobra.Command, args []string) error {
					nbtool, err := netboxtool.NewNetboxClient(p.Config.Netbox)
					data, err := nbtool.GetDevices()
					if err != nil {
						return err
					}
					Pprint(data)
					return nil
				},
			},

			boa.CmdT[ParamsDevice]{
				Use:   "get-vm",
				Short: "Get one VM",
				RunFuncE: func(p *ParamsDevice, cmd *cobra.Command, args []string) error {
					if p.Name == "" && p.ID == 0 {
						fmt.Println("Error:Specify name or id")
						return nil
					}
					nbtool, err := netboxtool.NewNetboxClient(p.Config.Netbox)
					if err != nil {
						return err
					}
					data, err := nbtool.GetVM(p.Name, p.ID)
					if err != nil {
						return err
					}
					Pprint(data)
					return nil
				},
			},
			boa.CmdT[Params]{
				Use:   "get-vms",
				Short: "Get all VMs",
				RunFuncE: func(p *Params, cmd *cobra.Command, args []string) error {
					nbtool, err := netboxtool.NewNetboxClient(p.Config.Netbox)
					if err != nil {
						return err
					}
					data, err := nbtool.GetVMs()
					if err != nil {
						return err
					}
					Pprint(data)
					return nil
				},
			},
			boa.CmdT[ParamsDeviceType]{
				Use:   "get-device-type",
				Short: "Get device type",
				RunFuncE: func(p *ParamsDeviceType, cmd *cobra.Command, args []string) error {
					nbtool, err := netboxtool.NewNetboxClient(p.Config.Netbox)
					data, err := nbtool.GetDeviceType(p.Manufacturer, p.Model)
					if err != nil {
						return err
					}
					Pprint(data)
					return nil
				},
			},
			boa.CmdT[ParamsManufacturer]{
				Use:   "get-manufacturer",
				Short: "Get Manufacturer",
				RunFuncE: func(p *ParamsManufacturer, cmd *cobra.Command, args []string) error {
					nbtool, err := netboxtool.NewNetboxClient(p.Config.Netbox)
					data, err := nbtool.GetManufacturer(p.Name, p.ID)
					if err != nil {
						return err
					}
					Pprint(data)
					return nil
				},
			}),
	}.Run()
}

// print structures JSON formatted
func Pprint(data any) {
	s, _ := json.MarshalIndent(data, "", "  ")
	fmt.Println(string(s))
}
