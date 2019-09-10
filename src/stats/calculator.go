package stats

import (
	"math"

	"github.com/docker/docker/api/types"
)

type Cooked types.Stats

func Cook(t types.Stats) Cooked {
	return Cooked(t)
}

type CPU struct {
	CPU    float64
	Kernel float64
	User   float64
}

// this formula is only valid for Linux. TODO: provide windows version
func (c *Cooked) CPU() CPU {
	cpu := CPU{}
	// calculate the change for the cpu usage of the container in between readings
	duration := float64(c.Read.Sub(c.PreRead).Nanoseconds())
	if duration > 0 {
		maxVal := float64(len(c.CPUStats.CPUUsage.PercpuUsage) * 100)

		cpuDelta := float64(c.CPUStats.CPUUsage.TotalUsage - c.PreCPUStats.CPUUsage.TotalUsage)
		cpu.CPU = math.Min(maxVal, cpuDelta*100/duration)

		userDelta := float64(c.CPUStats.CPUUsage.UsageInUsermode - c.PreCPUStats.CPUUsage.UsageInUsermode)
		cpu.User = math.Min(maxVal, userDelta*100/duration)

		kernelDelta := float64(c.CPUStats.CPUUsage.UsageInKernelmode - c.PreCPUStats.CPUUsage.UsageInKernelmode)
		cpu.Kernel = math.Min(maxVal, kernelDelta*100/duration)
	}
	return cpu
}

type Memory struct {
	UsageBytes      float64
	CacheUsageBytes float64
	RSSUsageBytes   float64
	MemLimitBytes   float64
}

// TODO: add other metrics such as swap and memsw limit
func (c *Cooked) Memory() Memory {
	var cache, rss float64
	if icache, ok := c.MemoryStats.Stats["cache"]; ok {
		cache = float64(icache)
	}
	if irss, ok := c.MemoryStats.Stats["rss"]; ok {
		rss = float64(irss)
	}
	return Memory{
		UsageBytes:      float64(c.MemoryStats.Usage),
		CacheUsageBytes: cache,
		RSSUsageBytes:   rss,
		MemLimitBytes:   float64(c.MemoryStats.Limit),
	}
}
