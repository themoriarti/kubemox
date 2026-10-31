package proxmox

import (
	"strings"

	"github.com/luthermonson/go-proxmox"

	"github.com/alperencelik/kubemox/pkg/utils"
)

type TaskError struct {
	ExitStatus string
}

func (e *TaskError) Error() string {
	if e.ExitStatus == "" {
		return "the task did not finish in time"
	}
	return e.ExitStatus
}

// Transient reports whether the same request can work on another try: the task
// lost the race for the machine's lock, or was still running when the wait for
// it ran out, which leaves ExitStatus empty.
func (e *TaskError) Transient() bool {
	return e.ExitStatus == "" || utils.IsTimeoutError(e) || strings.Contains(e.ExitStatus, "is locked")
}

func (pc *ProxmoxClient) GetTask(taskUPID string) *proxmox.Task {
	taskID := proxmox.UPID(
		taskUPID,
	)
	return proxmox.NewTask(taskID, pc.Client)
}
