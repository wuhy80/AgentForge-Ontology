package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"
)

func ValidateIsolationMode(mode string) error {
	if mode != "" && mode != "sandbox" && mode != "trusted" {
		return errors.New("执行隔离模式仅支持sandbox或trusted")
	}
	return nil
}

// Public deployments require both a rootless daemon and gVisor. Missing
// infrastructure makes the runner unavailable; it never falls back to runc.
func (d *DockerRunner) CheckIsolation(ctx context.Context) error {
	if ValidateIsolationMode(d.Isolation) != nil {
		return errors.New("invalid isolation mode")
	}
	if d.Isolation == "trusted" {
		return nil
	}
	probe, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := d.exec(probe, "info", "--format", `{"security":{{json .SecurityOptions}},"runtimes":{{json .Runtimes}},"cgroupVersion":{{json .CgroupVersion}},"memory":{{.MemoryLimit}},"pids":{{.PidsLimit}},"cpu":{{.CPUCfsQuota}}}`)
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, io.Discard
	if cmd.Run() != nil || output.Len() > 65536 {
		return errors.New("sandbox daemon unavailable")
	}
	decoder := json.NewDecoder(&output)
	var info struct {
		Security          []string                   `json:"security"`
		Runtimes          map[string]json.RawMessage `json:"runtimes"`
		CgroupVersion     string                     `json:"cgroupVersion"`
		Memory, Pids, CPU bool
	}
	if decoder.Decode(&info) != nil || decoder.Decode(new(any)) != io.EOF {
		return errors.New("invalid sandbox daemon configuration")
	}
	rootless := false
	for _, option := range info.Security {
		if option == "name=rootless" {
			rootless = true
		}
	}
	if !rootless || len(info.Runtimes["runsc"]) == 0 || info.CgroupVersion != "2" || !info.Memory || !info.Pids || !info.CPU {
		return errors.New("rootless Docker, runsc and enforced cgroup v2 budgets are required")
	}
	return nil
}
