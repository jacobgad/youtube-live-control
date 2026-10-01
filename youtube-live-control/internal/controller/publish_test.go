package controller

import (
	"slices"
	"testing"

	"github.com/jacobgad/youtube-live-control/internal/mqtt"
)

func TestRetireWipesExactlyWhatRenderDevicePublishes(t *testing.T) {
	var rendered []string
	for _, m := range renderDevice(deviceSnap{deviceID: "s1"}) {
		rendered = append(rendered, m.topic)
	}
	listed := append([]string(nil), mqtt.StreamTopics{Device: "s1"}.StateTopics()...)
	slices.Sort(rendered)
	slices.Sort(listed)
	if !slices.Equal(rendered, listed) {
		t.Fatalf("renderDevice and StateTopics out of sync:\nrendered %v\nlisted %v", rendered, listed)
	}
}
