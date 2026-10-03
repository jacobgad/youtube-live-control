package mqtt

import "strings"

// Topic layout and payload constants shared with Home Assistant.
const (
	Prefix            = "ylc"
	HADiscoveryPrefix = "homeassistant"
	HAStatusTopic     = HADiscoveryPrefix + "/status"

	PayloadOnline  = "online"
	PayloadOffline = "offline"
	PayloadPress   = "PRESS"
	PayloadOn      = "ON"
	PayloadOff     = "OFF"

	PayloadAuthorized   = "authorized"
	PayloadUnauthorized = "unauthorized"

	HubNodeID            = "youtube_live_hub"
	HubIdentifier        = "ylc:hub"
	SchedulingNodeID     = "youtube_live_scheduling"
	SchedulingIdentifier = "ylc:scheduling"

	StreamNodePrefix       = "ylc_stream_"
	StreamIdentifierPrefix = "ylc:stream:"

	ControllerAvailability = Prefix + "/controller/availability"
	AuthState              = Prefix + "/auth/state"
	ChannelState           = Prefix + "/channel/state"

	FastModeState      = Prefix + "/fast_mode/state"
	FastModeSet        = Prefix + "/fast_mode/set"
	FastRemainingState = Prefix + "/fast_mode_remaining/state"

	SchedulingLock       = Prefix + "/scheduling/lock"
	PresetState          = Prefix + "/preset/state"
	PresetSet            = Prefix + "/preset/set"
	StartState           = Prefix + "/start/state"
	StartSet             = Prefix + "/start/set"
	SchedulePrivacyState = Prefix + "/schedule_privacy/state"
	SchedulePrivacySet   = Prefix + "/schedule_privacy/set"
	SchedulePress        = Prefix + "/schedule/press"
	ScheduleAvailability = Prefix + "/schedule/availability"
)

const streamTopicPrefix = Prefix + "/stream/"

// DeviceID maps a YouTube stream ID onto the charset Home Assistant allows in
// discovery node IDs; MQTT topic levels use the same form so one ID addresses both.
func DeviceID(streamID string) string {
	var b strings.Builder
	for _, r := range streamID {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// StreamTopics addresses one stream-key device's topics; Device is DeviceID(streamID).
type StreamTopics struct{ Device string }

func (t StreamTopics) root() string { return streamTopicPrefix + t.Device }

// Node is the device's Home Assistant discovery node ID.
func (t StreamTopics) Node() string { return StreamNodePrefix + t.Device }

// Identifier is the device's Home Assistant device identifier.
func (t StreamTopics) Identifier() string { return StreamIdentifierPrefix + t.Device }

func (t StreamTopics) Lock() string                { return t.root() + "/lock" }
func (t StreamTopics) BroadcastState() string      { return t.root() + "/broadcast/state" }
func (t StreamTopics) BroadcastSet() string        { return t.root() + "/broadcast/set" }
func (t StreamTopics) BroadcastAttributes() string { return t.root() + "/broadcast/attributes" }
func (t StreamTopics) TitleState() string          { return t.root() + "/title/state" }
func (t StreamTopics) TitleSet() string            { return t.root() + "/title/set" }
func (t StreamTopics) PrivacyState() string        { return t.root() + "/privacy/state" }
func (t StreamTopics) PrivacySet() string          { return t.root() + "/privacy/set" }
func (t StreamTopics) ThumbnailURLState() string   { return t.root() + "/thumbnail/url" }
func (t StreamTopics) ThumbnailAvail() string      { return t.root() + "/thumbnail/availability" }
func (t StreamTopics) StageState() string          { return t.root() + "/stage/state" }
func (t StreamTopics) ScheduledStartState() string { return t.root() + "/scheduled_start/state" }
func (t StreamTopics) LiveState() string           { return t.root() + "/live/state" }
func (t StreamTopics) EncoderState() string        { return t.root() + "/encoder/state" }
func (t StreamTopics) HealthState() string         { return t.root() + "/stream_health/state" }
func (t StreamTopics) StatusState() string         { return t.root() + "/broadcast_status/state" }
func (t StreamTopics) StreamKeyState() string      { return t.root() + "/stream_key/state" }
func (t StreamTopics) GoLivePress() string         { return t.root() + "/go_live/press" }
func (t StreamTopics) GoLiveAvailability() string  { return t.root() + "/go_live/availability" }
func (t StreamTopics) EndPress() string            { return t.root() + "/end_stream/press" }
func (t StreamTopics) EndAvailability() string     { return t.root() + "/end_stream/availability" }
func (t StreamTopics) DeletePress() string         { return t.root() + "/delete/press" }
func (t StreamTopics) DeleteAvailability() string  { return t.root() + "/delete/availability" }

// StateTopics lists every retained non-config topic the device publishes, so a
// retired device can be wiped from the broker.
func (t StreamTopics) StateTopics() []string {
	return []string{
		t.Lock(), t.BroadcastState(), t.BroadcastAttributes(), t.TitleState(), t.PrivacyState(),
		t.ThumbnailURLState(), t.ThumbnailAvail(), t.StageState(), t.ScheduledStartState(),
		t.LiveState(), t.EncoderState(), t.HealthState(), t.StatusState(), t.StreamKeyState(),
		t.GoLiveAvailability(), t.EndAvailability(), t.DeleteAvailability(),
	}
}

// parseStreamTopic splits ylc/stream/<device>/<object>/<leaf>.
func parseStreamTopic(topic string) (deviceID, object, leaf string, ok bool) {
	rest, found := strings.CutPrefix(topic, streamTopicPrefix)
	if !found {
		return "", "", "", false
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", "", false
	}
	return parts[0], parts[1], parts[2], true
}

func streamWildcard(object, leaf string) string {
	return streamTopicPrefix + "+/" + object + "/" + leaf
}

// Subscriptions are the topics the controller listens on.
var Subscriptions = []string{
	streamWildcard("broadcast", "set"),
	streamWildcard("title", "set"),
	streamWildcard("privacy", "set"),
	streamWildcard("go_live", "press"),
	streamWildcard("end_stream", "press"),
	streamWildcard("delete", "press"),
	FastModeSet,
	PresetSet, StartSet, SchedulePrivacySet, SchedulePress,
	HAStatusTopic,
}

// StageOptions are the Stage sensor's enum states.
var StageOptions = []string{
	"No broadcast", "No stream key", "Waiting for encoder", "Ready to go live",
	"Starting", "Live", "Stream stopping", "Ready to end", "Ending", "Ended",
}

// HADiscoveryTopic builds homeassistant/<component>/<node>/<object>/config.
func HADiscoveryTopic(component, nodeID, objectID string) string {
	return HADiscoveryPrefix + "/" + component + "/" + nodeID + "/" + objectID + "/config"
}

// legacyNodeID is 1.x's selected-broadcast device, replaced by per-stream devices.
const legacyNodeID = "youtube_live_control"

// RetiredConfigTopics are earlier versions' configs, cleared so their entities do not linger.
var RetiredConfigTopics = []string{
	HADiscoveryTopic("select", legacyNodeID, "broadcast"),
	HADiscoveryTopic("text", legacyNodeID, "title"),
	HADiscoveryTopic("select", legacyNodeID, "privacy"),
	HADiscoveryTopic("image", legacyNodeID, "thumbnail"),
	HADiscoveryTopic("sensor", legacyNodeID, "stage"),
	HADiscoveryTopic("sensor", legacyNodeID, "scheduled_start"),
	HADiscoveryTopic("binary_sensor", legacyNodeID, "live"),
	HADiscoveryTopic("binary_sensor", legacyNodeID, "encoder"),
	HADiscoveryTopic("button", legacyNodeID, "go_live"),
	HADiscoveryTopic("button", legacyNodeID, "end_stream"),
	HADiscoveryTopic("button", legacyNodeID, "delete"),
	HADiscoveryTopic("switch", legacyNodeID, "fast_mode"),
	HADiscoveryTopic("sensor", legacyNodeID, "fast_mode_remaining"),
	HADiscoveryTopic("sensor", legacyNodeID, "broadcast_status"),
	HADiscoveryTopic("sensor", legacyNodeID, "stream_health"),
	HADiscoveryTopic("sensor", legacyNodeID, "channel"),
	HADiscoveryTopic("sensor", legacyNodeID, "authorization"),
	HADiscoveryTopic("sensor", legacyNodeID, "viewers"),
	HADiscoveryTopic("text", legacyNodeID, "scheduled_start"),
	HADiscoveryTopic("select", legacyNodeID, "thumbnail"),
	HADiscoveryTopic("button", legacyNodeID, "save"),
	HADiscoveryTopic("button", legacyNodeID, "create"),
	HADiscoveryTopic("select", SchedulingNodeID, "date"),
	HADiscoveryTopic("select", SchedulingNodeID, "time"),
	HADiscoveryTopic("date", SchedulingNodeID, "date"),
	HADiscoveryTopic("time", SchedulingNodeID, "time"),
	HADiscoveryTopic("number", legacyNodeID, "list_poll_minutes"),
	HADiscoveryTopic("number", legacyNodeID, "fast_poll_seconds"),
	HADiscoveryTopic("number", legacyNodeID, "fast_mode_minutes"),
	HADiscoveryTopic("number", legacyNodeID, "live_poll_seconds"),
	HADiscoveryTopic("number", legacyNodeID, "idle_poll_minutes"),
}

// RetiredStateTopics are 1.x's retained state topics, cleared alongside the configs.
var RetiredStateTopics = []string{
	Prefix + "/lock",
	Prefix + "/broadcast/state",
	Prefix + "/broadcast/attributes",
	Prefix + "/title/state",
	Prefix + "/privacy/state",
	Prefix + "/thumbnail/url",
	Prefix + "/thumbnail/availability",
	Prefix + "/stage/state",
	Prefix + "/scheduled_start/state",
	Prefix + "/live/state",
	Prefix + "/encoder/state",
	Prefix + "/stream_health/state",
	Prefix + "/broadcast_status/state",
	Prefix + "/go_live/availability",
	Prefix + "/end_stream/availability",
	Prefix + "/delete/availability",
}
