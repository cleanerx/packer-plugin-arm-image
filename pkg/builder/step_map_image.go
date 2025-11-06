package builder

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/packer-plugin-sdk/multistep"
	"github.com/hashicorp/packer-plugin-sdk/packer"
)

var (
	loopRe = regexp.MustCompile("/dev/loop[0-9]+")
)

type stepMapImage struct {
	ImageKey  string
	ResultKey string
}

func (s *stepMapImage) Run(_ context.Context, state multistep.StateBag) multistep.StepAction {
	// Read our value and assert that it is the type we want
	image := state.Get(s.ImageKey).(string)
	ui := state.Get("ui").(packer.Ui)

	ui.Message(fmt.Sprintf("mapping %s", image))

	// Create loopback device
	//   -P (--partscan) creates a partitioned loop device
	//   -f (--find) finds first unused loop device
	//   --show outputs used loop device path
	// Output example:
	//   /dev/loop10
	out, err := exec.Command("losetup", "--show", "-f", "-P", image).CombinedOutput()
	ui.Say(fmt.Sprintf("losetup --show -f -P %s", image))
	if err != nil {
		ui.Error(fmt.Sprintf("error losetup --show -f -P %v: %s", err, string(out)))
		s.Cleanup(state)
		return multistep.ActionHalt
	}
	path := strings.TrimSpace(string(out))
	loop := strings.Split(path, "/")[2]
	partPrefix := loop + "p"


    // Wait for udev to settle
    _ = exec.Command("udevadm", "settle").Run()


    var partitions []string
    found := false

    // Scan with lsblk for partitions and LVM volumes
    for retries := 0; retries < 30; retries++ {
        lsblkOut, err := exec.Command("lsblk", "-ln", "-o", "NAME,TYPE", path).Output()
        if err != nil {
            ui.Error(fmt.Sprintf("lsblk failed: %v", err))
            break
        }

        lines := strings.Split(string(lsblkOut), "\n")
        for _, line := range lines {
            fields := strings.Fields(line)
            if len(fields) != 2 {
                continue
            }
            name, typ := fields[0], fields[1]
            if strings.HasPrefix(name, partPrefix) && typ == "part" {
                partitions = append(partitions, "/dev/"+name)
            }
        }

        if len(partitions) > 0 {
            found = true
            break
        }
        time.Sleep(1 * time.Second)
    }

    // Always scan /dev/mapper for LVM volumes
    mapperFiles, _ := os.ReadDir("/dev/mapper/")
    for _, file := range mapperFiles {
        if strings.HasPrefix(file.Name(), loop) || strings.Contains(file.Name(), "ubuntu--vg") {
            partitions = append(partitions, "/dev/mapper/"+file.Name())
            found = true
        }
    }

    if !found || len(partitions) == 0 {
        ui.Error("No partitions or LVM volumes found. GPT or LVM layout may not be detected.")
        s.Cleanup(state)
        return multistep.ActionHalt
    }

    // Sort loopXpN partitions numerically
    sort.SliceStable(partitions, func(i, j int) bool {
        pi := partitions[i]
        pj := partitions[j]
        if strings.HasPrefix(pi, "/dev/"+partPrefix) && strings.HasPrefix(pj, "/dev/"+partPrefix) {
            n_i, _ := strconv.Atoi(strings.TrimPrefix(pi, "/dev/"+partPrefix))
            n_j, _ := strconv.Atoi(strings.TrimPrefix(pj, "/dev/"+partPrefix))
            return n_i < n_j
        }
        return pi < pj // fallback alphabetical
    })

    state.Put(s.ResultKey, partitions)
    ui.Message(fmt.Sprintf("Mapped partitions and volumes: %v", partitions))

    return multistep.ActionContinue


}

func (s *stepMapImage) Cleanup(state multistep.StateBag) {
	switch partitions := state.Get(s.ResultKey).(type) {
	case nil:
		return
	case []string:
		if len(partitions) > 0 {
			// Convert /dev/loop10p1 into /dev/loop10
			loop := loopRe.Find([]byte(partitions[0]))
			if loop != nil {
				run(context.TODO(), state, fmt.Sprintf("losetup -d %s", string(loop)))
			}
		}
	}
}
