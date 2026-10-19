package proxmox

import (
	"strings"
	"testing"
)

func TestRenderNetworkConfig(t *testing.T) {
	ports := []PortAddress{
		{MACAddress: "BC:24:11:0F:69:C2", Address: "10.208.64.10/24", Gateway: "10.208.64.1"},
		{MACAddress: "BC:24:11:0F:69:C3"},
		{Address: "10.0.0.5/24"}, // no MAC yet: left out
	}
	got := RenderNetworkConfig(ports, []string{"10.208.64.1", "1.1.1.1"})
	for _, want := range []string{
		"version: 2", "eth0:", "macaddress: bc:24:11:0f:69:c2", "set-name: eth0", "dhcp4: false",
		"- 10.208.64.10/24", "to: default", "via: 10.208.64.1", "- 1.1.1.1\n        - 10.208.64.1",
		"eth1:", "macaddress: bc:24:11:0f:69:c3", "dhcp4: true",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("network-config lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "eth2") {
		t.Errorf("a port without a MAC was rendered:\n%s", got)
	}
	if RenderNetworkConfig([]PortAddress{{MACAddress: "aa"}}, nil) != "" {
		t.Error("ports without addresses rendered a network-config")
	}
}

func TestNoCloudHashAndMetaData(t *testing.T) {
	a := NoCloudData{UserData: "#cloud-config\n", NetworkConfig: "version: 2\n"}
	b := NoCloudData{UserData: "#cloud-config\n", NetworkConfig: "version: 2\n"}
	if a.Hash() != b.Hash() {
		t.Fatal("equal content hashed differently")
	}
	b.VendorData = "x"
	if a.Hash() == b.Hash() {
		t.Fatal("different content hashed the same")
	}
	// The separator keeps the fields apart: moving bytes across them changes the hash.
	c := NoCloudData{UserData: "#cloud-config\nversion: 2\n"}
	if a.Hash() == c.Hash() {
		t.Fatal("field boundary is not part of the hash")
	}
	if got := RenderMetaData("web-abc123", "web"); got != "instance-id: web-abc123\nlocal-hostname: web\n" {
		t.Fatalf("RenderMetaData = %q", got)
	}
}
