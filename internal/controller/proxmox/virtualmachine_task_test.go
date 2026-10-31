package proxmox

import (
	"errors"
	"testing"

	"github.com/alperencelik/kube-external-watcher/watcher"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	proxmoxv1alpha1 "github.com/alperencelik/kubemox/api/proxmox/v1alpha1"
	"github.com/alperencelik/kubemox/pkg/proxmox"
)

func TestTaskFailure(t *testing.T) {
	cases := []struct {
		name     string
		exit     string
		terminal bool
	}{
		{"lost the lock", "can't lock file '/var/lock/qemu-server/lock-124.conf' - got timeout", false},
		{"locked by a backup", "VM is locked (backup)", false},
		{"still running", "", false},
		{"storage is gone", "storage 'fast' does not exist", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			vm := &proxmoxv1alpha1.VirtualMachine{}
			vm.Name = "vm"
			key := types.NamespacedName{Name: vm.Name}
			w := watcher.NewExternalWatcher(&proxmox.VirtualMachineFetcher{})
			w.Register(key, watcher.ResourceConfig{})
			r := &VirtualMachineReconciler{Watcher: w}

			err := r.taskFailure(vm, &proxmox.TaskError{ExitStatus: tc.exit})

			if got := errors.Is(err, reconcile.TerminalError(nil)); got != tc.terminal {
				t.Errorf("terminal = %v, want %v", got, tc.terminal)
			}
			if got := w.IsRegistered(key); got == tc.terminal {
				t.Errorf("watcher registered = %v after a failure with terminal = %v", got, tc.terminal)
			}
		})
	}
}
