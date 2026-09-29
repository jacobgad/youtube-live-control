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

// NumberSpec describes one runtime-tunable number entity; the range doubles as
// command validation on the controller side.
type NumberSpec struct {
	Object string
	Name   string
	Unit   string
	Icon   string
	Min    int
	Max    int
}

// TunableSpecs are the runtime settings exposed as configuration number entities,
// adjustable from Home Assistant without a restart; the add-on options keep only
// infrastructure that genuinely needs one (OAuth client, URLs, directories).
var TunableSpecs = []NumberSpec{
	{Object: "list_poll_minutes", Name: "List poll interval", Unit: "min", Icon: "mdi:update", Min: 1, Max: 60},
	{Object: "fast_poll_seconds", Name: "Fast poll interval", Unit: "s", Icon: "mdi:speedometer", Min: 1, Max: 30},
	{Object: "fast_mode_minutes", Name: "Fast refresh window", Unit: "min", Icon: "mdi:timer-cog-outline", Min: 1, Max: 60},
	{Object: "live_poll_seconds", Name: "Live poll interval", Unit: "s", Icon: "mdi:pulse", Min: 15, Max: 600},
	{Object: "idle_poll_minutes", Name: "Idle poll interval", Unit: "min", Icon: "mdi:sleep", Min: 1, Max: 60},
}

// NumberState builds the retained state topic for a tunable.
func NumberState(object string) string { return Prefix + "/" + object + "/state" }

// NumberSet builds the command topic for a tunable.
func NumberSet(object string) string { return Prefix + "/" + object + "/set" }
