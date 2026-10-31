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
	refuse  bool
	exit    string   // how a task ends; empty means OK
	deleted []string // options deleted, in order
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
		writeCTData(w, machine)
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
