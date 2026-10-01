package mqtt

import (
	"slices"
	"strings"
	"testing"
)

var testOrigin = Origin{Version: "test", SupportURL: "https://example.invalid"}

var testOptions = Options{
	Streams: []StreamDevice{
		{DeviceID: "s1", Name: "Main Auditorium", Broadcasts: []string{"Sun 5 Jan 09:30 · Service"}},
		{DeviceID: "s2", Name: "Youth Hall", Broadcasts: nil},
	},
	Presets: []string{"Sunday"},
}

func messagesByObject(t *testing.T) map[string]Message {
	t.Helper()
	out := map[string]Message{}
	for _, m := range Messages(testOrigin, testOptions) {
		out[m.Payload["unique_id"].(string)] = m
	}
	return out
}

var streamObjects = []string{
	"broadcast", "title", "privacy", "thumbnail", "stage", "scheduled_start", "live", "encoder",
	"go_live", "end_stream", "delete", "broadcast_status", "stream_health", "stream_key",
}

func TestMessagesCoverEveryEntityOnEveryDevice(t *testing.T) {
	byID := messagesByObject(t)
	hub := []string{"authorization", "channel", "fast_mode", "fast_mode_remaining"}
	scheduling := []string{"preset", "start", "privacy", "schedule"}
	for _, object := range hub {
		m, ok := byID[HubNodeID+"_"+object]
		if !ok {
			t.Fatalf("missing discovery config for hub %s", object)
		}
		if m.Payload["device"].(map[string]any)["identifiers"].([]string)[0] != HubIdentifier {
			t.Fatalf("%s: wrong device", object)
		}
	}
	for _, s := range testOptions.Streams {
		node := StreamNodePrefix + s.DeviceID
		for _, object := range streamObjects {
			m, ok := byID[node+"_"+object]
			if !ok {
				t.Fatalf("missing discovery config for %s %s", s.DeviceID, object)
			}
			device := m.Payload["device"].(map[string]any)
			if device["identifiers"].([]string)[0] != StreamIdentifierPrefix+s.DeviceID {
				t.Fatalf("%s %s: wrong device", s.DeviceID, object)
			}
			if device["via_device"] != HubIdentifier || device["name"] != s.Name {
				t.Fatalf("%s %s: device = %v", s.DeviceID, object, device)
			}
		}
	}
	for _, object := range scheduling {
		m, ok := byID[SchedulingNodeID+"_"+object]
		if !ok {
			t.Fatalf("missing discovery config for %s", object)
		}
		device := m.Payload["device"].(map[string]any)
		if device["identifiers"].([]string)[0] != SchedulingIdentifier || device["via_device"] != HubIdentifier {
			t.Fatalf("%s: wrong device %v", object, device)
		}
	}
	want := len(hub) + len(streamObjects)*len(testOptions.Streams) + len(scheduling)
	if len(byID) != want {
		t.Fatalf("entity count %d, want %d", len(byID), want)
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
	s1 := StreamTopics{Device: "s1"}
	wantTopic := map[string]string{
		s1.Node() + "_go_live":         s1.GoLiveAvailability(),
		s1.Node() + "_end_stream":      s1.EndAvailability(),
		s1.Node() + "_delete":          s1.DeleteAvailability(),
		SchedulingNodeID + "_schedule": ScheduleAvailability,
	}
	for id, topic := range wantTopic {
		m := byID[id]
		if m.Payload["availability_mode"] != "all" {
			t.Fatalf("%s: availability_mode = %v", id, m.Payload["availability_mode"])
		}
		if !hasAvailabilityTopic(m, topic) {
			t.Fatalf("%s: availability misses %s", id, topic)
		}
		if !strings.HasSuffix(m.Payload["command_topic"].(string), "/press") {
			t.Fatalf("%s: command topic = %v", id, m.Payload["command_topic"])
		}
	}
}

func hasAvailabilityTopic(m Message, topic string) bool {
	avail, ok := m.Payload["availability"].([]map[string]any)
	if !ok {
		return false
	}
	for _, a := range avail {
		if a["topic"] == topic {
			return true
		}
	}
	return false
}

// Each stream device's writable entities carry that device's lock, scheduling carries
// its own, and no lock crosses devices: that is what keeps bystander devices usable
// while another device's change is in flight.
func TestCommandEntitiesCarryTheirOwnDeviceLock(t *testing.T) {
	byID := messagesByObject(t)
	for _, s := range testOptions.Streams {
		st := StreamTopics{Device: s.DeviceID}
		for _, object := range []string{"broadcast", "title", "privacy", "go_live", "end_stream", "delete"} {
			m := byID[st.Node()+"_"+object]
			if !hasAvailabilityTopic(m, st.Lock()) {
				t.Fatalf("%s %s lacks its device lock", s.DeviceID, object)
			}
			other := StreamTopics{Device: "s1"}
			if s.DeviceID == "s1" {
				other = StreamTopics{Device: "s2"}
			}
			if hasAvailabilityTopic(m, other.Lock()) || hasAvailabilityTopic(m, SchedulingLock) {
				t.Fatalf("%s %s carries a foreign lock", s.DeviceID, object)
			}
		}
	}
	for _, object := range []string{"preset", "start", "privacy", "schedule"} {
		if !hasAvailabilityTopic(byID[SchedulingNodeID+"_"+object], SchedulingLock) {
			t.Fatalf("scheduling %s lacks the scheduling lock", object)
		}
	}
	if hasAvailabilityTopic(byID[HubNodeID+"_fast_mode"], SchedulingLock) {
		t.Fatal("fast mode must not carry any lock")
	}
}

func TestPlatformNativeClasses(t *testing.T) {
	byID := messagesByObject(t)
	node := StreamNodePrefix + "s1"
	stage := byID[node+"_stage"]
	if stage.Payload["device_class"] != "enum" || len(stage.Payload["options"].([]string)) != 10 {
		t.Fatalf("stage = %v", stage.Payload)
	}
	if byID[node+"_scheduled_start"].Payload["device_class"] != "timestamp" {
		t.Fatal("scheduled_start is not a timestamp sensor")
	}
	if byID[node+"_live"].Payload["device_class"] != "running" || byID[node+"_encoder"].Payload["device_class"] != "connectivity" {
		t.Fatal("binary sensor device classes")
	}
	s1 := StreamTopics{Device: "s1"}
	if byID[node+"_broadcast"].Payload["json_attributes_topic"] != s1.BroadcastAttributes() {
		t.Fatal("broadcast select lacks attributes topic")
	}
	if byID[SchedulingNodeID+"_start"].Topic != HADiscoveryTopic("datetime", SchedulingNodeID, "start") {
		t.Fatal("start is not a core datetime entity")
	}
	if byID[node+"_thumbnail"].Payload["url_topic"] != s1.ThumbnailURLState() || byID[node+"_thumbnail"].Topic != HADiscoveryTopic("image", node, "thumbnail") {
		t.Fatal("thumbnail is not a core image entity")
	}
	for _, diag := range []string{node + "_broadcast_status", node + "_stream_health", node + "_stream_key", HubNodeID + "_channel", HubNodeID + "_authorization"} {
		if byID[diag].Payload["entity_category"] != "diagnostic" {
			t.Fatalf("%s should be diagnostic", diag)
		}
	}
}

func TestSelectsEmbedOptions(t *testing.T) {
	byID := messagesByObject(t)
	if opts := byID[StreamNodePrefix+"s1_broadcast"].Payload["options"].([]string); len(opts) != 1 || opts[0] != "Sun 5 Jan 09:30 · Service" {
		t.Fatalf("s1 broadcast options = %v", opts)
	}
	if opts := byID[StreamNodePrefix+"s2_broadcast"].Payload["options"].([]string); len(opts) != 0 {
		t.Fatalf("s2 broadcast options = %v", opts)
	}
	if opts := byID[SchedulingNodeID+"_preset"].Payload["options"].([]string); len(opts) != 1 || opts[0] != "Sunday" {
		t.Fatalf("preset options = %v", opts)
	}
	if opts := byID[StreamNodePrefix+"s1_privacy"].Payload["options"].([]string); len(opts) != 3 || opts[0] != "public" {
		t.Fatalf("privacy options = %v", opts)
	}
}

func TestStreamConfigTopicsMatchMessages(t *testing.T) {
	var published []string
	for _, m := range streamMessages(testOptions.Streams[0], testOrigin) {
		published = append(published, m.Topic)
	}
	listed := StreamConfigTopics("s1")
	slices.Sort(published)
	slices.Sort(listed)
	if !slices.Equal(published, listed) {
		t.Fatalf("StreamConfigTopics out of sync with streamMessages:\npublished %v\nlisted %v", published, listed)
	}
}

func TestRetiredTopicsAreNotPublished(t *testing.T) {
	live := map[string]bool{}
	for _, m := range Messages(testOrigin, testOptions) {
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
	for _, topic := range RetiredStateTopics {
		if !strings.HasPrefix(topic, Prefix+"/") {
			t.Fatalf("retired state topic %q", topic)
		}
		if strings.HasPrefix(topic, streamTopicPrefix) {
			t.Fatalf("retired state topic %q collides with the stream namespace", topic)
		}
	}
}

func TestDeviceID(t *testing.T) {
	if got := DeviceID("abcd-1234_XY"); got != "abcd-1234_XY" {
		t.Fatalf("DeviceID = %q", got)
	}
	if got := DeviceID("a.b/c+d#e"); got != "a_b_c_d_e" {
		t.Fatalf("DeviceID = %q", got)
	}
}

func TestDiscoveryTopicLayout(t *testing.T) {
	if got := HADiscoveryTopic("select", StreamNodePrefix+"s1", "broadcast"); got != "homeassistant/select/ylc_stream_s1/broadcast/config" {
		t.Fatalf("HADiscoveryTopic = %q", got)
	}
}
