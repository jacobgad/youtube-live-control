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
	objects := []string{
		"broadcast", "title", "scheduled_start", "thumbnail",
		"fast_mode", "fast_mode_remaining",
		"stream_health", "broadcast_status", "viewers",
		"save", "create", "go_live", "end_stream", "authorization", "channel",
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

func TestRetiredConfigTopicsAreNumbers(t *testing.T) {
	if len(RetiredConfigTopics) != 5 {
		t.Fatalf("retired topics = %v", RetiredConfigTopics)
	}
	for _, topic := range RetiredConfigTopics {
		if !strings.HasPrefix(topic, HADiscoveryPrefix+"/number/"+NodeID+"/") || !strings.HasSuffix(topic, "/config") {
			t.Fatalf("retired topic %q", topic)
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
