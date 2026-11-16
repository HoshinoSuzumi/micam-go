package cloud

import (
	"net/netip"
	"strconv"
	"strings"
)

// Device is one entry from the account's device list.
type Device struct {
	DID     string
	Name    string
	Model   string
	LocalIP string
	Online  bool
}

// IsCamera reports whether this device is something this tool can stream.
func (d Device) IsCamera() bool {
	return strings.Contains(d.Model, ".camera.")
}

// Vendor identifiers returned by miss_get_vendor.
const (
	VendorTUTK  = 1
	VendorAgora = 3
	VendorCS2   = 4
	VendorMTP   = 6
)

// Provisioning is everything the cloud hands over so a client can reach one
// camera peer to peer.
type Provisioning struct {
	VendorID     int
	P2PID        string
	License      string
	CRCKey       string
	InitString   string
	Servers      []netip.Addr
	DevicePublic string // camera X25519 public key, hex
	Sign         string // signature presented during MISS authentication
	MissVersion  string
}

// VendorName renders the transport family for logs.
func (p *Provisioning) VendorName() string {
	switch p.VendorID {
	case VendorTUTK:
		return "tutk"
	case VendorAgora:
		return "agora"
	case VendorCS2:
		return "cs2"
	case VendorMTP:
		return "mtp"
	}
	return "vendor-" + strconv.Itoa(p.VendorID)
}

// IsCS2 reports whether the camera uses the only transport implemented here.
func (p *Provisioning) IsCS2() bool {
	return p.VendorID == VendorCS2
}
