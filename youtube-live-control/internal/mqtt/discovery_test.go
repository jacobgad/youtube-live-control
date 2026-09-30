package mqtt

import (
	"strings"
	"testing"
)

var testOrigin = Origin{Version: "test", SupportURL: "https://example.invalid"}

var testOptions = Options{
	Broadcasts: []string{"Sun 5 Jan 09:30 · Service"},
	Presets:    []string{"Sunday"},
}

func messagesByObject(t *testing.T) map[string]Message {
	t.Helper()
	out := map[string]Message{}
	for _, m := range Messages(testOrigin, testOptions) {
		out[m.Payload["unique_id"].(string)] = m
	}
	return out
}

func TestMessagesCoverEveryEntityOnBothDevices(t *testing.T) {
	byID := messagesByObject(t)
	controller := []string{
		"broadcast", "title", "privacy", "thumbnail", "stage", "scheduled_start", "live", "encoder",
		"go_live", "end_stream", "fast_mode", "fast_mode_remaining",
		"broadcast_status", "stream_health", "channel", "authorization",
	}
	scheduling := []string{"preset", "date", "time", "schedule"}
	for _, object := range controller {
		m, ok := byID[NodeID+"_"+object]
		if !ok {
			t.Fatalf("missing discovery config for %s", object)
		}
		if m.Payload["device"].(map[string]any)["identifiers"].([]string)[0] != Identifier {
			t.Fatalf("%s: wrong device", object)
		}
	}
	for _, object := range scheduling {
		m, ok := byID[SchedulingNodeID+"_"+object]
		if !ok {
			t.Fatalf("missing discovery config for %s", object)
		}
		device := m.Payload["device"].(map[string]any)
		if device["identifiers"].([]string)[0] != SchedulingIdentifier || device["via_device"] != Identifier {
			t.Fatalf("%s: wrong device %v", object, device)
		}
	}
	if len(byID) != len(controller)+len(scheduling) {
		t.Fatalf("entity count %d, want %d", len(byID), len(controller)+len(scheduling))
	}
}

func TestCommandEntitiesAreNeverOptimisticOrRetained(t *testing.T) {
	for id, m := range messagesByObject(t) {
		if _, hasCommand := m.Payload["command_topic"]; !hasCommand {
			continue
		}
		if m.Payload["retain"] != false {
			t.Fatalf("%s: commands must not be retained", id)
		}
		if _, isButton := m.Payload["payload_press"]; isButton {
			continue
		}
		if m.Payload["optimistic"] != false {
			t.Fatalf("%s: entities must not be optimistic", id)
		}
		if _, hasState := m.Payload["state_topic"]; !hasState {
			t.Fatalf("%s: writable entity without state topic", id)
		}
	}
}

func TestButtonsGateOnOwnAvailabilityTopic(t *testing.T) {
	byID := messagesByObject(t)
	wantTopic := map[string]string{
		NodeID + "_go_live":            GoLiveAvailability,
		NodeID + "_end_stream":         EndAvailability,
		SchedulingNodeID + "_schedule": ScheduleAvailability,
	}
	for id, topic := range wantTopic {
		m := byID[id]
		if m.Payload["availability_mode"] != "all" {
			t.Fatalf("%s: availability_mode = %v", id, m.Payload["availability_mode"])
		}
		found := false
		for _, a := range m.Payload["availability"].([]map[string]any) {
			if a["topic"] == topic {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s: availability misses %s", id, topic)
		}
		if !strings.HasSuffix(m.Payload["command_topic"].(string), "/press") {
			t.Fatalf("%s: command topic = %v", id, m.Payload["command_topic"])
		}
	}
}

func TestPlatformNativeClasses(t *testing.T) {
	byID := messagesByObject(t)
	stage := byID[NodeID+"_stage"]
	if stage.Payload["device_class"] != "enum" || len(stage.Payload["options"].([]string)) != 10 {
		t.Fatalf("stage = %v", stage.Payload)
	}
	if byID[NodeID+"_scheduled_start"].Payload["device_class"] != "timestamp" {
		t.Fatal("scheduled_start is not a timestamp sensor")
	}
	if byID[NodeID+"_live"].Payload["device_class"] != "running" || byID[NodeID+"_encoder"].Payload["device_class"] != "connectivity" {
		t.Fatal("binary sensor device classes")
	}
	if byID[NodeID+"_broadcast"].Payload["json_attributes_topic"] != BroadcastAttributes {
		t.Fatal("broadcast select lacks attributes topic")
	}
	if byID[SchedulingNodeID+"_date"].Topic != HADiscoveryTopic("date", SchedulingNodeID, "date") || byID[SchedulingNodeID+"_time"].Topic != HADiscoveryTopic("time", SchedulingNodeID, "time") {
		t.Fatal("date/time are not core date and time entities")
	}
	if byID[NodeID+"_thumbnail"].Payload["url_topic"] != ThumbnailURLState || byID[NodeID+"_thumbnail"].Topic != HADiscoveryTopic("image", NodeID, "thumbnail") {
		t.Fatal("thumbnail is not a core image entity")
	}
	for _, diag := range []string{"broadcast_status", "stream_health", "channel", "authorization"} {
		if byID[NodeID+"_"+diag].Payload["entity_category"] != "diagnostic" {
			t.Fatalf("%s should be diagnostic", diag)
		}
	}
}

func TestSelectsEmbedOptions(t *testing.T) {
	byID := messagesByObject(t)
	for id, want := range map[string]string{
		NodeID + "_broadcast":        "Sun 5 Jan 09:30 · Service",
		SchedulingNodeID + "_preset": "Sunday",
	} {
		if opts := byID[id].Payload["options"].([]string); len(opts) != 1 || opts[0] != want {
			t.Fatalf("%s options = %v", id, opts)
		}
	}
	if opts := byID[NodeID+"_privacy"].Payload["options"].([]string); len(opts) != 3 || opts[0] != "public" {
		t.Fatalf("privacy options = %v", opts)
	}
}

func TestRetiredConfigTopicsAreNotPublished(t *testing.T) {
	live := map[string]bool{}
	for _, m := range Messages(testOrigin, Options{}) {
		live[m.Topic] = true
	}
	for _, topic := range RetiredConfigTopics {
		if !strings.HasPrefix(topic, HADiscoveryPrefix+"/") || !strings.HasSuffix(topic, "/config") {
			t.Fatalf("retired topic %q", topic)
		}
		if live[topic] {
			t.Fatalf("retired topic %q is still published", topic)
		}
	}
}

func TestDiscoveryTopicLayout(t *testing.T) {
	if got := HADiscoveryTopic("select", NodeID, "broadcast"); got != "homeassistant/select/youtube_live_control/broadcast/config" {
		t.Fatalf("HADiscoveryTopic = %q", got)
	}
}
