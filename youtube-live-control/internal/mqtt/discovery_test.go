package mqtt

import (
	"strings"
	"testing"
)

var testOrigin = Origin{Version: "test", SupportURL: "https://example.invalid"}

func messagesByObject(t *testing.T) map[string]Message {
	t.Helper()
	out := map[string]Message{}
	for _, m := range Messages(testOrigin, []string{"New stream…"}, []string{"Keep current"}) {
		out[m.Payload["unique_id"].(string)] = m
	}
	return out
}

func TestMessagesCoverEveryEntity(t *testing.T) {
	byID := messagesByObject(t)
	objects := make([]string, 0, 14+len(TunableSpecs))
	objects = append(objects,
		"broadcast", "title", "scheduled_start", "thumbnail",
		"fast_mode", "fast_mode_remaining",
		"stream_health", "broadcast_status", "viewers",
		"save", "create", "go_live", "end_stream", "authorization",
	)
	for _, spec := range TunableSpecs {
		objects = append(objects, spec.Object)
	}
	for _, object := range objects {
		if _, ok := byID[NodeID+"_"+object]; !ok {
			t.Fatalf("missing discovery config for %s", object)
		}
	}
	if len(byID) != len(objects) {
		t.Fatalf("entity count %d, want %d", len(byID), len(objects))
	}
}

func TestTunableNumbersAreConfigEntities(t *testing.T) {
	byID := messagesByObject(t)
	for _, spec := range TunableSpecs {
		m := byID[NodeID+"_"+spec.Object]
		if m.Payload["entity_category"] != "config" {
			t.Fatalf("%s: entity_category = %v", spec.Object, m.Payload["entity_category"])
		}
		if m.Payload["min"] != spec.Min || m.Payload["max"] != spec.Max {
			t.Fatalf("%s: range = %v..%v", spec.Object, m.Payload["min"], m.Payload["max"])
		}
		if m.Payload["unit_of_measurement"] != spec.Unit || m.Payload["mode"] != "box" {
			t.Fatalf("%s: unit/mode = %v/%v", spec.Object, m.Payload["unit_of_measurement"], m.Payload["mode"])
		}
		if m.Payload["command_topic"] != NumberSet(spec.Object) || m.Payload["state_topic"] != NumberState(spec.Object) {
			t.Fatalf("%s: topics = %v/%v", spec.Object, m.Payload["command_topic"], m.Payload["state_topic"])
		}
	}
}

func TestSubscriptionsIncludeTunables(t *testing.T) {
	for _, spec := range TunableSpecs {
		found := false
		for _, topic := range Subscriptions {
			if topic == NumberSet(spec.Object) {
				found = true
			}
		}
		if !found {
			t.Fatalf("subscriptions miss %s", NumberSet(spec.Object))
		}
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
		"save":       SaveAvailability,
		"create":     CreateAvailability,
		"go_live":    GoLiveAvailability,
		"end_stream": EndAvailability,
	}
	for object, topic := range wantTopic {
		m := byID[NodeID+"_"+object]
		if m.Payload["availability_mode"] != "all" {
			t.Fatalf("%s: availability_mode = %v", object, m.Payload["availability_mode"])
		}
		avail := m.Payload["availability"].([]map[string]any)
		found := false
		for _, a := range avail {
			if a["topic"] == topic {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s: availability misses %s", object, topic)
		}
		if !strings.HasSuffix(m.Payload["command_topic"].(string), "/press") {
			t.Fatalf("%s: command topic = %v", object, m.Payload["command_topic"])
		}
	}
}

func TestSelectsEmbedOptions(t *testing.T) {
	byID := messagesByObject(t)
	if opts := byID[NodeID+"_broadcast"].Payload["options"].([]string); len(opts) != 1 || opts[0] != "New stream…" {
		t.Fatalf("broadcast options = %v", opts)
	}
	if opts := byID[NodeID+"_thumbnail"].Payload["options"].([]string); len(opts) != 1 || opts[0] != "Keep current" {
		t.Fatalf("thumbnail options = %v", opts)
	}
}

func TestDiscoveryTopicLayout(t *testing.T) {
	if got := HADiscoveryTopic("select", "broadcast"); got != "homeassistant/select/youtube_live_control/broadcast/config" {
		t.Fatalf("HADiscoveryTopic = %q", got)
	}
}
