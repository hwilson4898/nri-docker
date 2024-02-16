package integration

import (
	"context"
	"testing"

	"github.com/newrelic/nri-docker/src/raw/dockerapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// It currently it shows how to get container stats data and can be useful for development purposes.
func TestDockerAPIFetcher(t *testing.T) {
	dockerClient := newDocker(t)

	info, err := dockerClient.Info(context.Background())
	require.NoError(t, err)
	if info.CgroupVersion != "2" {
		t.Skip("DockerAPIFetcher only supports cgroups v2 version")
	}

	fetcher := dockerapi.NewFetcher(dockerClient)

	// run a container for testing purposes
	containerID, dockerRM := stress(t, "stress-ng", "-c", "0", "-l", "0", "-t", "5m")
	defer dockerRM()

	// show container inspect data (it's done in biz/metrics)
	inspectData, err := dockerClient.ContainerInspect(context.Background(), containerID)
	require.NoError(t, err)

	assert.EventuallyWithT(t, func(t *assert.CollectT) {
		statsData, err := fetcher.Fetch(inspectData)
		require.NoError(t, err)

		// Network metrics
		// Only RxBytes and RxPackets are generated
		assert.NotZero(t, statsData.Network.RxBytes)
		assert.NotZero(t, statsData.Network.RxPackets)
	}, eventuallyTimeout, eventuallyTick)
}
