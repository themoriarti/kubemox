package proxmox

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/luthermonson/go-proxmox"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	proxmoxv1alpha1 "github.com/alperencelik/kubemox/api/proxmox/v1alpha1"
)

const (
	refusedVMName = "vm-refused"
	refusedVMID   = 100
	refusedNet1   = "net1"
	refusedNet2   = "net2"
)

// refusingHost is a node with one machine. refuse makes it answer every request
// that would start a task the way a busy Proxmox does, with no task at all.
type refusingHost struct {
	refuse bool
	// refuseReads makes the machine's status unreadable, though it is listed.
	refuseReads bool
	exit        string   // how a task ends; empty means OK
	deleted     []string // options deleted, in order
}

// refusedVMStatus is the machine as /qemu and status/current list it.
type refusedVMStatus struct {
	VMID   int    `json:"vmid"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

// refusingTask is a finished task as /tasks/{upid}/status reports it.
type refusingTask struct {
	Status     string `json:"status"`
	ExitStatus string `json:"exitstatus"`
}

// refusedNode is the node as /nodes lists it.
type refusedNode struct {
	Node   string `json:"node"`
	Status string `json:"status"`
}

func newRefusingHost(t *testing.T, h *refusingHost) *ProxmoxClient {
	t.Helper()
	base := "/api2/json/nodes/" + lxcTestNode
	machine := refusedVMStatus{refusedVMID, refusedVMName, VirtualMachineRunningState}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+base+"/status", func(w http.ResponseWriter, _ *http.Request) {
		writeCTData(w, map[string]any{"uptime": 1})
	})
	mux.HandleFunc("GET "+base+"/qemu", func(w http.ResponseWriter, _ *http.Request) {
		writeCTData(w, []refusedVMStatus{machine})
	})
	mux.HandleFunc("GET "+base+"/qemu/100/status/current", func(w http.ResponseWriter, _ *http.Request) {
		if h.refuseReads {
			writePVEError(w, "got no worker upid - start worker failed")
			return
		}
		writeCTData(w, machine)
	})
	mux.HandleFunc("GET /api2/json/nodes", func(w http.ResponseWriter, _ *http.Request) {
		writeCTData(w, []refusedNode{{lxcTestNode, "online"}})
	})
	mux.HandleFunc("GET "+base+"/qemu/100/config", func(w http.ResponseWriter, _ *http.Request) {
		writeCTData(w, map[string]any{virtualMachineCPUOption: 1})
	})
	mux.HandleFunc("GET "+base+"/tasks/", func(w http.ResponseWriter, _ *http.Request) {
		exit := h.exit
		if exit == "" {
			exit = "OK"
		}
		writeCTData(w, refusingTask{"stopped", exit})
	})
	mux.HandleFunc("POST "+base+"/qemu/100/", func(w http.ResponseWriter, r *http.Request) {
		if h.refuse {
			writePVEError(w, "got no worker upid - start worker failed")
			return
		}
		var options map[string]string
		if err := json.NewDecoder(r.Body).Decode(&options); err != nil {
			t.Errorf("decoding the config request: %v", err)
		}
		h.deleted = append(h.deleted, options["delete"])
		writeCTData(w, fakeCTUPID("qmconfig", refusedVMID))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	client := proxmox.NewClient(srv.URL+"/api2/json",
		proxmox.WithAPIToken("test@pve!t", "secret"),
		proxmox.WithHTTPClient(srv.Client()))
	return &ProxmoxClient{Client: client, nodesCache: make(map[string]NodeCache)}
}

func refusedVM() *proxmoxv1alpha1.VirtualMachine {
	return &proxmoxv1alpha1.VirtualMachine{
		TypeMeta:   metav1.TypeMeta{Kind: "VirtualMachine"},
		ObjectMeta: metav1.ObjectMeta{Name: refusedVMName},
		Spec: proxmoxv1alpha1.VirtualMachineSpec{
			Name: refusedVMName, NodeName: lxcTestNode,
			AdditionalConfig: map[string]string{"balloon": "0"},
		},
	}
}

func deleteOptions(pc *ProxmoxClient, options ...string) error {
	return applyChanges(pc, ctx, refusedVM(), nil, nil, options,
		func(option string) string { return option }, nil, nil, "option")
}

// A Proxmox that refuses to start a task answers with no task to wait for. It
// has to come back as an error: these used to dereference the missing task.
func TestRefusedTaskIsAnError(t *testing.T) {
	cases := map[string]func(pc *ProxmoxClient) error{
		"additional config": func(pc *ProxmoxClient) error { return pc.ApplyAdditionalConfiguration(refusedVM()) },
		"reboot":            func(pc *ProxmoxClient) error { return pc.RebootVM(refusedVMName, lxcTestNode) },
		"stop before delete": func(pc *ProxmoxClient) error {
			return pc.DeleteVM(refusedVMName, lxcTestNode)
		},
		"delete option": func(pc *ProxmoxClient) error { return deleteOptions(pc, refusedNet1) },
	}
	for name, call := range cases {
		t.Run(name, func(t *testing.T) {
			if err := call(newRefusingHost(t, &refusingHost{refuse: true})); err == nil {
				t.Fatal("no error from a refused task")
			}
		})
	}
}

func TestRebootOfAMissingMachineIsAnError(t *testing.T) {
	pc := newRefusingHost(t, &refusingHost{})
	if err := pc.RebootVM("no-such-machine", lxcTestNode); err == nil {
		t.Fatal("no error for a machine the node does not have")
	}
}

func TestDeleteOptionsGoesThroughAllOfThem(t *testing.T) {
	h := &refusingHost{}
	if err := deleteOptions(newRefusingHost(t, h), refusedNet1, refusedNet2); err != nil {
		t.Fatal(err)
	}
	if len(h.deleted) != 2 || h.deleted[0] != refusedNet1 || h.deleted[1] != refusedNet2 {
		t.Fatalf("deleted %v", h.deleted)
	}
}

func TestDeleteOptionReportsAFailedTask(t *testing.T) {
	err := deleteOptions(newRefusingHost(t, &refusingHost{exit: "unable to delete"}), refusedNet1, refusedNet2)
	var taskErr *TaskError
	if !errors.As(err, &taskErr) || taskErr.ExitStatus != "unable to delete" {
		t.Fatalf("err = %v, want the task's exit status", err)
	}
}

// A machine that is listed but cannot be read has to come back as an error, and
// nothing may be changed on it: an unread cloud-init config used to compare as
// different, which empties the real one.
func TestUnreadableMachineIsAnError(t *testing.T) {
	ciConfig := &proxmoxv1alpha1.CloudInitConfig{User: "user"}
	template := &proxmoxv1alpha1.VirtualMachineTemplate{
		Spec: proxmoxv1alpha1.VirtualMachineTemplateSpec{
			Name: refusedVMName, NodeName: lxcTestNode, CloudInitConfig: ciConfig,
		},
	}
	cases := map[string]func(t *testing.T, pc *ProxmoxClient) error{
		"snapshots": func(_ *testing.T, pc *ProxmoxClient) error {
			_, err := pc.GetVMSnapshots(refusedVMName)
			return err
		},
		"remove tag": func(_ *testing.T, pc *ProxmoxClient) error {
			return pc.RemoveVirtualMachineTag(refusedVMName, lxcTestNode, "tag")
		},
		"network": func(_ *testing.T, pc *ProxmoxClient) error {
			nets := []proxmoxv1alpha1.VirtualMachineNetwork{{Model: defaultNetworkModel, Bridge: testBridge0}}
			return pc.updateNetworkConfig(ctx, refusedVM(), 0, nets)
		},
		"convert to template": func(_ *testing.T, pc *ProxmoxClient) error {
			return pc.ConvertVMToTemplate(refusedVMName, lxcTestNode)
		},
		"set cloud-init": func(_ *testing.T, pc *ProxmoxClient) error {
			return pc.SetCloudInitConfig(refusedVMName, lxcTestNode, ciConfig)
		},
		"empty cloud-init": func(_ *testing.T, pc *ProxmoxClient) error {
			return pc.EmptyCloudInitConfig(refusedVMName, lxcTestNode)
		},
		"get cloud-init": func(_ *testing.T, pc *ProxmoxClient) error {
			_, err := pc.GetCloudInitConfig(refusedVMName, lxcTestNode)
			return err
		},
		"compare cloud-init": func(t *testing.T, pc *ProxmoxClient) error {
			differs, err := pc.CheckVirtualMachineTemplateCIConfig(template)
			if differs {
				t.Error("an unread config compares as different")
			}
			return err
		},
		"cloud-init drive": func(_ *testing.T, pc *ProxmoxClient) error {
			return pc.AddCloudInitDrive(refusedVMName, lxcTestNode)
		},
		"boot order": func(_ *testing.T, pc *ProxmoxClient) error {
			return pc.SetBootOrder(refusedVMName, lxcTestNode)
		},
	}
	for name, call := range cases {
		t.Run(name, func(t *testing.T) {
			h := &refusingHost{refuseReads: true}
			if err := call(t, newRefusingHost(t, h)); err == nil {
				t.Error("no error from an unreadable machine")
			}
			if len(h.deleted) != 0 {
				t.Errorf("the machine was changed: %v", h.deleted)
			}
		})
	}
}
