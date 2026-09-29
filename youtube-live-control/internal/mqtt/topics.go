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

	NodeID     = "youtube_live_control"
	Identifier = "ylc:controller"

	ControllerAvailability = Prefix + "/controller/availability"
	AuthState              = Prefix + "/auth/state"
	ChannelState           = Prefix + "/channel/state"

	BroadcastState = Prefix + "/broadcast/state"
	BroadcastSet   = Prefix + "/broadcast/set"
	TitleState     = Prefix + "/title/state"
	TitleSet       = Prefix + "/title/set"
	ScheduledState = Prefix + "/scheduled_start/state"
	ScheduledSet   = Prefix + "/scheduled_start/set"
	ThumbnailState = Prefix + "/thumbnail/state"
	ThumbnailSet   = Prefix + "/thumbnail/set"

	FastModeState      = Prefix + "/fast_mode/state"
	FastModeSet        = Prefix + "/fast_mode/set"
	FastRemainingState = Prefix + "/fast_mode_remaining/state"

	HealthState  = Prefix + "/stream_health/state"
	StatusState  = Prefix + "/broadcast_status/state"
	ViewersState = Prefix + "/viewers/state"

	SavePress   = Prefix + "/save/press"
	CreatePress = Prefix + "/create/press"
	GoLivePress = Prefix + "/go_live/press"
	EndPress    = Prefix + "/end_stream/press"

	SaveAvailability   = Prefix + "/save/availability"
	CreateAvailability = Prefix + "/create/availability"
	GoLiveAvailability = Prefix + "/go_live/availability"
	EndAvailability    = Prefix + "/end_stream/availability"
)

// HADiscoveryTopic builds homeassistant/<component>/<node>/<object>/config.
func HADiscoveryTopic(component, objectID string) string {
	return HADiscoveryPrefix + "/" + component + "/" + NodeID + "/" + objectID + "/config"
}

// RetiredConfigTopics are discovery configs published by earlier versions and cleared
// on every full republish so their entities do not linger in Home Assistant.
var RetiredConfigTopics = []string{
	HADiscoveryTopic("number", "list_poll_minutes"),
	HADiscoveryTopic("number", "fast_poll_seconds"),
	HADiscoveryTopic("number", "fast_mode_minutes"),
	HADiscoveryTopic("number", "live_poll_seconds"),
	HADiscoveryTopic("number", "idle_poll_minutes"),
}
