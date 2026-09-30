package mqtt

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

	NodeID               = "youtube_live_control"
	Identifier           = "ylc:controller"
	SchedulingNodeID     = "youtube_live_scheduling"
	SchedulingIdentifier = "ylc:scheduling"

	ControllerAvailability = Prefix + "/controller/availability"
	AuthState              = Prefix + "/auth/state"
	ChannelState           = Prefix + "/channel/state"

	BroadcastState      = Prefix + "/broadcast/state"
	BroadcastSet        = Prefix + "/broadcast/set"
	BroadcastAttributes = Prefix + "/broadcast/attributes"
	ThumbnailURLState   = Prefix + "/thumbnail/url"
	ThumbnailAvail      = Prefix + "/thumbnail/availability"
	TitleState          = Prefix + "/title/state"
	TitleSet            = Prefix + "/title/set"
	PrivacyState        = Prefix + "/privacy/state"
	PrivacySet          = Prefix + "/privacy/set"

	FastModeState      = Prefix + "/fast_mode/state"
	FastModeSet        = Prefix + "/fast_mode/set"
	FastRemainingState = Prefix + "/fast_mode_remaining/state"

	StageState          = Prefix + "/stage/state"
	ScheduledStartState = Prefix + "/scheduled_start/state"
	LiveState           = Prefix + "/live/state"
	EncoderState        = Prefix + "/encoder/state"
	HealthState         = Prefix + "/stream_health/state"
	StatusState         = Prefix + "/broadcast_status/state"

	GoLivePress = Prefix + "/go_live/press"
	EndPress    = Prefix + "/end_stream/press"

	GoLiveAvailability = Prefix + "/go_live/availability"
	EndAvailability    = Prefix + "/end_stream/availability"

	PresetState          = Prefix + "/preset/state"
	PresetSet            = Prefix + "/preset/set"
	DateState            = Prefix + "/date/state"
	DateSet              = Prefix + "/date/set"
	TimeState            = Prefix + "/time/state"
	TimeSet              = Prefix + "/time/set"
	SchedulePress        = Prefix + "/schedule/press"
	ScheduleAvailability = Prefix + "/schedule/availability"
)

// StageOptions are the Stage sensor's enum states, written for the person at the panel.
var StageOptions = []string{
	"no_broadcast", "no_stream_key", "waiting_for_encoder", "ready_to_go_live",
	"starting", "live", "stream_stopping", "ready_to_end", "ending", "ended",
}

// HADiscoveryTopic builds homeassistant/<component>/<node>/<object>/config.
func HADiscoveryTopic(component, nodeID, objectID string) string {
	return HADiscoveryPrefix + "/" + component + "/" + nodeID + "/" + objectID + "/config"
}

// RetiredConfigTopics are discovery configs published by earlier versions and cleared
// on every full republish so their entities do not linger in Home Assistant.
var RetiredConfigTopics = []string{
	HADiscoveryTopic("text", NodeID, "scheduled_start"),
	HADiscoveryTopic("select", NodeID, "thumbnail"),
	HADiscoveryTopic("button", NodeID, "save"),
	HADiscoveryTopic("button", NodeID, "create"),
	HADiscoveryTopic("sensor", NodeID, "viewers"),
	HADiscoveryTopic("select", SchedulingNodeID, "date"),
	HADiscoveryTopic("select", SchedulingNodeID, "time"),
	HADiscoveryTopic("datetime", SchedulingNodeID, "start"),
	HADiscoveryTopic("number", NodeID, "list_poll_minutes"),
	HADiscoveryTopic("number", NodeID, "fast_poll_seconds"),
	HADiscoveryTopic("number", NodeID, "fast_mode_minutes"),
	HADiscoveryTopic("number", NodeID, "live_poll_seconds"),
	HADiscoveryTopic("number", NodeID, "idle_poll_minutes"),
}
