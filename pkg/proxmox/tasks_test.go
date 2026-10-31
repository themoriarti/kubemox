package proxmox

import "testing"

func TestTaskErrorTransient(t *testing.T) {
	cases := []struct {
		exit string
		want bool
	}{
		{"", true}, // still running when the wait ran out
		{"can't lock file '/var/lock/qemu-server/lock-124.conf' - got timeout", true},
		{"VM quit/powerdown failed - got timeout", true},
		{"VM is locked (backup)", true},
		{"CT is locked (snapshot)", true},
		{"storage 'fast' does not exist", false},
		{"Permission check failed (/vms/100, VM.PowerMgmt)", false},
		{"unable to create VM 100 - no such logical volume", false},
	}
	for _, tc := range cases {
		e := &TaskError{ExitStatus: tc.exit}
		if got := e.Transient(); got != tc.want {
			t.Errorf("Transient(%q) = %v, want %v", tc.exit, got, tc.want)
		}
	}
}

func TestTaskErrorStillRunningSaysSo(t *testing.T) {
	if got := (&TaskError{}).Error(); got == "" {
		t.Fatal("a task that ran out of time has no message")
	}
}
