package stats

import (
	"bufio"
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/containerd/cgroups"
	"github.com/docker/docker/api/types"
	"github.com/newrelic/infra-integrations-sdk/log"
	"github.com/newrelic/infra-integrations-sdk/persist"
)

const (
	cgroupPath = "/sys/fs/cgroup"
)

type CGroupsProvider struct {
	store persist.Storer
}

func NewCGroupsProvider() (*CGroupsProvider, error) {
	store, err := persist.NewFileStore( // TODO: make the following options configurable
		persist.DefaultPath("container_cpus"),
		log.NewStdErr(true),
		60*time.Second)

	return &CGroupsProvider{store: store}, err
}

func (cg *CGroupsProvider) PersistStats() error {
	return cg.store.Save()
}

// returns a path that is located on the root folder of the host and the `/host` folder
// on the integrations. If they existed in both root and /host, returns the /host path,
// assuming the integration is running in a container
func hostFolder(folders ...string) string {
	insideHostFile := path.Join(folders...)
	insideContainerFile := path.Join("/host", insideHostFile) // TODO: make the /host configurable
	var err error
	if _, err = os.Stat(insideContainerFile); err == nil {
		return insideContainerFile
	}
	return insideHostFile
}

func parseUintFile(file string) (value uint64, err error) {
	f, err := os.Open(file)
	if err != nil {
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Scan()
	if err = scanner.Err(); err != nil {
		return
	}

	return strconv.ParseUint(scanner.Text(), 10, 64)
}

func (cg *CGroupsProvider) Fetch(containerID string) (Cooked, error) {
	stats := types.Stats{}

	stats.Read = time.Now()

	control, err := cgroups.Load(cgroups.V1, cgroups.StaticPath(path.Join("docker", containerID)))
	if err != nil {
		return Cooked{}, err
	}
	metrics, err := control.Stat()
	if err != nil {
		return Cooked{}, err
	}

	if err := pidStats(metrics, containerID, &stats.PidsStats); err != nil {
		log.Error("couldn't read pids stats: %v", err)
	}

	if err := readBlkioStats(containerID, &stats.BlkioStats); err != nil {
		log.Error("couldn't read blkio stats: %v", err)
	}

	if err := cpuStats(metrics, &stats.CPUStats); err != nil {
		log.Error("couldn't read cpu stats: %v", err)
	}

	if err := memoryStats(metrics, &stats.MemoryStats); err != nil {
		log.Error("couldn't read memory stats: %v", err)
	}

	var preStats struct {
		UnixTime int64
		CPUStats types.CPUStats
	}
	// Reading previous CPU stats
	if _, err := cg.store.Get(containerID, &preStats); err == nil {
		stats.PreRead = time.Unix(preStats.UnixTime, 0)
		stats.PreCPUStats = preStats.CPUStats
	}
	// Storing current CPU stats for the next execution
	preStats.UnixTime = stats.Read.Unix()
	preStats.CPUStats = stats.CPUStats
	_ = cg.store.Set(containerID, preStats)

	return Cooked(stats), nil
}

func pidStats(metrics *cgroups.Metrics, containerID string, stats *types.PidsStats) (err error) {
	stats.Current = metrics.Pids.Current
	stats.Limit = metrics.Pids.Limit
	cpath := hostFolder(cgroupPath, "pids", "docker", containerID)

	stats.Current, err = parseUintFile(path.Join(cpath, "pids.current"))
	if err != nil {
		return err
	}

	body, err := ioutil.ReadFile(path.Join(cpath, "/pids.max"))
	if err != nil {
		return err
	}
	value := string(body)
	if value == "max\n" {
		stats.Limit = 0
	} else {
		stats.Limit, err = strconv.ParseUint(value, 10, 64)
		if err != nil {
			return err
		}
	}

	return nil
}

// TODO: use cgroups library (as for readPidStats)
func readBlkio(blkioPath string, ioStat string) ([]types.BlkioStatEntry, error) {
	entries := []types.BlkioStatEntry{}

	f, err := os.Open(path.Join(blkioPath, ioStat))
	if err != nil {
		return nil, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.FieldsFunc(sc.Text(), func(r rune) bool {
			return r == ' ' || r == ':'
		})
		if len(fields) < 3 {
			if len(fields) == 2 && fields[0] == "Total" {
				// skip total line
				continue
			} else {
				return nil, fmt.Errorf("invalid line found while parsing %s: %s", blkioPath, sc.Text())
			}
		}

		v, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			return nil, err
		}
		major := v

		v, err = strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return nil, err
		}
		minor := v

		op := ""
		valueField := 2
		if len(fields) == 4 {
			op = fields[2]
			valueField = 3
		}
		v, err = strconv.ParseUint(fields[valueField], 10, 64)
		if err != nil {
			return nil, err
		}
		entries = append(entries, types.BlkioStatEntry{Major: major, Minor: minor, Op: op, Value: v})
	}

	return entries, nil
}

// TODO: use cgroups library (as for readPidStats)
func readBlkioStats(containerID string, stats *types.BlkioStats) (err error) {
	cpath := hostFolder(cgroupPath, "blkio", "docker", containerID)

	if stats.IoServiceBytesRecursive, err = readBlkio(cpath, "blkio.throttle.io_service_bytes"); err != nil {
		return err
	}

	if stats.IoServicedRecursive, err = readBlkio(cpath, "blkio.throttle.io_serviced"); err != nil {
		return err
	}
	return nil
}

const nanoSecondsPerSecond = 1e9

// readSystemCPUUsage returns the host system's cpu usage in
// nanoseconds. An error is returned if the format of the underlying
// file does not match.
//
// Uses /proc/stat defined by POSIX. Looks for the cpu
// statistics line and then sums up the first seven fields
// provided. See `man 5 proc` for details on specific field
// information.
func readSystemCPUUsage() (uint64, error) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return 0, err
	}

	bufReader := bufio.NewReaderSize(nil, 128)
	defer func() {
		bufReader.Reset(nil)
		f.Close()
	}()
	bufReader.Reset(f)

	for {
		line, err := bufReader.ReadString('\n')
		if err != nil {
			break
		}
		parts := strings.Fields(line)
		switch parts[0] {
		case "cpu":
			if len(parts) < 8 {
				return 0, fmt.Errorf("invalid number of cpu fields")
			}
			var totalClockTicks uint64
			for _, i := range parts[1:8] {
				v, err := strconv.ParseUint(i, 10, 64)
				if err != nil {
					return 0, fmt.Errorf("Unable to convert value %s to int: %s", i, err)
				}
				totalClockTicks += v
			}
			return (totalClockTicks * nanoSecondsPerSecond) / 100, nil
		}
	}
	return 0, fmt.Errorf("invalid stat format. Error trying to parse the '/proc/stat' file")
}

func cpuStats(metric *cgroups.Metrics, stats *types.CPUStats) error {
	stats.CPUUsage.TotalUsage = metric.CPU.Usage.Total
	stats.CPUUsage.UsageInUsermode = metric.CPU.Usage.User
	stats.CPUUsage.UsageInKernelmode = metric.CPU.Usage.Kernel
	stats.CPUUsage.PercpuUsage = metric.CPU.Usage.PerCPU

	var err error
	if stats.SystemUsage, err = readSystemCPUUsage(); err != nil {
		return err
	}

	return nil
}

func memoryStats(metric *cgroups.Metrics, stats *types.MemoryStats) error {

	stats.MaxUsage = metric.Memory.Usage.Max
	stats.Limit = metric.Memory.Usage.Limit
	stats.Stats = map[string]uint64{
		"cache": metric.Memory.Cache,
		"rss":   metric.Memory.RSS,
	}

	/* Calculating usage instead of `memory.usage_in_bytes` file contents.
	https://www.kernel.org/doc/Documentation/cgroup-v1/memory.txt
	For efficiency, as other kernel components, memory cgroup uses some optimization
	to avoid unnecessary cacheline false sharing. usage_in_bytes is affected by the
	method and doesn't show 'exact' value of memory (and swap) usage, it's a fuzz
	value for efficient access. (Of course, when necessary, it's synchronized.)
	If you want to know more exact memory usage, you should use RSS+CACHE(+SWAP)
	value in memory.stat(see 5.2).
	However, as the `docker stats` cli tool does, page cache is intentionally
	excluded to avoid misinterpretation of the output.
	Also the Swap usage is parsed from memory.memsw.usage_in_bytes, which
	according to the documentation reports the sum of current memory usage
	plus swap space used by processes in the cgroup (in bytes). That's why
	Usage is subtracted from the Swap: to get the actual swap.
	*/
	stats.Usage = metric.Memory.RSS + metric.Memory.Swap.Usage - metric.Memory.Usage.Usage

	return nil
}
