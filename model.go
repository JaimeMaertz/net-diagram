package netdiagram

import "time"

// Switch is everything read from one switch, normalized across vendors.
type Switch struct {
	Host     string   `json:"host"`
	Vendor   string   `json:"vendor"`
	Name     string   `json:"name,omitempty"`    // system name, if set; LLDP neighbors are matched on it
	DBName   string   `json:"db_name,omitempty"` // the device database's name for one of its MACs
	Model    string   `json:"model,omitempty"`
	Firmware string   `json:"firmware,omitempty"`
	Serial   string   `json:"serial,omitempty"`
	MACs     []string `json:"macs"` // the switch's own MACs (burned-in, management, ports)
	Ports    []Port   `json:"ports"`
	ARP      []ARP    `json:"arp,omitempty"`
}

// Title is what a switch is called on the map: the database's name, else its system name, else
// its model.
func (s Switch) Title() string {
	switch {
	case s.DBName != "":
		return s.DBName
	case s.Name != "":
		return s.Name
	}
	return s.Model
}

// Port is one physical port.
type Port struct {
	Name        string     `json:"name"` // as the switch names it: "0/5", "GigabitEthernet1/0/5"
	Description string     `json:"description,omitempty"`
	Up          bool       `json:"up"`
	Speed       string     `json:"speed,omitempty"` // "1000 Full"
	VLAN        string     `json:"vlan,omitempty"`
	PoEmW       int        `json:"poe_mw,omitempty"` // power delivered now
	PoEStatus   string     `json:"poe_status,omitempty"`
	Neighbors   []Neighbor `json:"neighbors,omitempty"` // LLDP / CDP / ISDP
	MACs        []MACEntry `json:"macs,omitempty"`      // learned on this port
}

// Neighbor is one LLDP (or CDP/ISDP) neighbor seen on a port.
type Neighbor struct {
	ChassisID   string `json:"chassis_id"`
	PortID      string `json:"port_id,omitempty"`
	SystemName  string `json:"system_name,omitempty"`
	SystemDesc  string `json:"system_desc,omitempty"`
	PortDesc    string `json:"port_desc,omitempty"`
	MgmtIP      string `json:"mgmt_ip,omitempty"`
	CapsEnabled string `json:"caps_enabled,omitempty"` // "bridge, router", "station only"
}

// MACEntry is one forwarding-table entry.
type MACEntry struct {
	MAC  string `json:"mac"`
	VLAN string `json:"vlan,omitempty"`
}

// ARP maps a MAC to an IP as some table saw it.
type ARP struct {
	MAC string `json:"mac"`
	IP  string `json:"ip"`
}

// Snapshot is one discovery: what was read, and the map worked out from it.
type Snapshot struct {
	Taken    time.Time `json:"taken"`
	Switches []Switch  `json:"switches"`
	Devices  []Device  `json:"devices"`
	Links    []Link    `json:"links"`
}

// Device is one box on the network, with one or more MACs.
type Device struct {
	Name         string      `json:"name"`
	Model        string      `json:"model,omitempty"`
	Jobsite      string      `json:"jobsite,omitempty"`
	Manufacturer string      `json:"manufacturer,omitempty"`
	Interfaces   []Interface `json:"interfaces"`
	Switch       string      `json:"switch"` // the host of the switch it's plugged into
	Port         string      `json:"port"`
	// Grouping says why these MACs are one device: "single", "database", "lldp", "self" (this
	// PC's adapters), "likely" (same manufacturer, adjacent MACs; unconfirmed).
	Grouping string `json:"grouping"`
	// Beyond is true when the device was only seen on a switch-to-switch port, so it sits behind
	// a switch that wasn't read.
	Beyond  bool   `json:"beyond,omitempty"`
	Virtual bool   `json:"virtual,omitempty"` // a virtual machine's MAC (VirtualBox, VMware, Hyper-V)
	Self    bool   `json:"self,omitempty"`    // the PC the scan ran from ("Your Computer")
	Note    string `json:"note,omitempty"`
}

// Interface is one MAC of a device.
type Interface struct {
	MAC    string `json:"mac"`
	Name   string `json:"name,omitempty"` // the database's name for this MAC, when it has one
	IP     string `json:"ip,omitempty"`
	Source string `json:"source,omitempty"` // where the name came from: "database", "lldp"
}

// Link is a cable between two switch ports. To is empty for a switch that wasn't read.
type Link struct {
	FromSwitch string `json:"from_switch"`
	FromPort   string `json:"from_port"`
	ToSwitch   string `json:"to_switch,omitempty"`
	ToPort     string `json:"to_port,omitempty"`
	ToName     string `json:"to_name,omitempty"` // the unread switch's LLDP name or chassis ID
}
