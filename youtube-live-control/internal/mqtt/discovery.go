package mqtt

import (
	"encoding/json"

	"github.com/jacobgad/youtube-live-control/internal/youtube"
)

// Origin identifies this add-on in discovery payloads.
type Origin struct {
	Version    string
	SupportURL string
}

// Message is one retained discovery config.
type Message struct {
	Topic   string
	Payload map[string]any
}

// JSON renders the payload.
func (m Message) JSON() string {
	data, _ := json.Marshal(m.Payload)
	return string(data)
}

// Options are the option lists embedded in the selects; discovery is republished when they change.
type Options struct {
	Broadcasts []string
	Presets    []string
}

const (
	controllerName = "YouTube Live"
	schedulingName = "YouTube Live Scheduling"

	iconBroadcast = "mdi:youtube"
	iconTitle     = "mdi:format-title"
	iconPrivacy   = "mdi:eye-lock-outline"
	iconFastMode  = "mdi:speedometer"
	iconFastLeft  = "mdi:timer-outline"
	iconStage     = "mdi:progress-check"
	iconStart     = "mdi:calendar-clock"
	iconHealth    = "mdi:pulse"
	iconStatus    = "mdi:broadcast"
	iconGoLive    = "mdi:play-circle"
	iconEnd       = "mdi:stop-circle"
	iconAuth      = "mdi:shield-account"
	iconPreset    = "mdi:playlist-star"
	iconDate      = "mdi:calendar"
	iconTime      = "mdi:clock-outline"
	iconSchedule  = "mdi:calendar-plus"

	// MaxTitleLength is YouTube's limit.
	MaxTitleLength = 100
)

// Messages lists every discovery config for both devices.
func Messages(o Origin, opts Options) []Message {
	return []Message{
		commandEntity(NodeID, "select", "broadcast", "Broadcast", iconBroadcast, map[string]any{
			"state_topic":           BroadcastState,
			"command_topic":         BroadcastSet,
			"json_attributes_topic": BroadcastAttributes,
			"options":               orEmpty(opts.Broadcasts),
		}, o),
		commandEntity(NodeID, "text", "title", "Title", iconTitle, map[string]any{
			"state_topic":   TitleState,
			"command_topic": TitleSet,
			"min":           0,
			"max":           MaxTitleLength,
			"mode":          "text",
		}, o),
		commandEntity(NodeID, "select", "privacy", "Privacy", iconPrivacy, map[string]any{
			"state_topic":   PrivacyState,
			"command_topic": PrivacySet,
			"options":       youtube.PrivacyOptions,
		}, o),
		thumbnailImage(o),
		sensor("stage", "Stage", iconStage, StageState, map[string]any{"device_class": "enum", "options": StageOptions}, "", o),
		sensor("scheduled_start", "Scheduled start", iconStart, ScheduledStartState, map[string]any{"device_class": "timestamp"}, "", o),
		binarySensor(NodeID, "live", "Live", LiveState, "running", o),
		binarySensor(NodeID, "encoder", "Encoder connected", EncoderState, "connectivity", o),
		button(NodeID, "go_live", "Go Live", iconGoLive, GoLivePress, GoLiveAvailability, o),
		button(NodeID, "end_stream", "End Stream", iconEnd, EndPress, EndAvailability, o),
		commandEntity(NodeID, "switch", "fast_mode", "Fast refresh", iconFastMode, map[string]any{
			"state_topic":   FastModeState,
			"command_topic": FastModeSet,
			"payload_on":    PayloadOn,
			"payload_off":   PayloadOff,
		}, o),
		sensor("fast_mode_remaining", "Fast refresh remaining", iconFastLeft, FastRemainingState, map[string]any{"unit_of_measurement": "min"}, "", o),
		sensor("broadcast_status", "Broadcast status", iconStatus, StatusState, nil, "diagnostic", o),
		sensor("stream_health", "Stream health", iconHealth, HealthState, nil, "diagnostic", o),
		sensor("channel", "Channel", iconBroadcast, ChannelState, nil, "diagnostic", o),
		authSensor(o),

		commandEntity(SchedulingNodeID, "select", "preset", "Preset", iconPreset, map[string]any{
			"state_topic":   PresetState,
			"command_topic": PresetSet,
			"options":       orEmpty(opts.Presets),
		}, o),
		commandEntity(SchedulingNodeID, "date", "date", "Date", iconDate, map[string]any{
			"state_topic":   DateState,
			"command_topic": DateSet,
		}, o),
		commandEntity(SchedulingNodeID, "time", "time", "Time", iconTime, map[string]any{
			"state_topic":   TimeState,
			"command_topic": TimeSet,
		}, o),
		button(SchedulingNodeID, "schedule", "Schedule", iconSchedule, SchedulePress, ScheduleAvailability, o),
	}
}

func orEmpty(list []string) []string {
	if list == nil {
		return []string{}
	}
	return list
}

func commandEntity(node, component, object, name, icon string, fields map[string]any, o Origin) Message {
	fields["optimistic"] = false
	fields["retain"] = false
	fields["qos"] = 1
	m := base(node, component, object, name, icon, fields, o)
	m.Payload["availability"] = []map[string]any{controllerAvailability(), authAvailability()}
	m.Payload["availability_mode"] = "all"
	return m
}

func sensor(object, name, icon, stateTopic string, extra map[string]any, category string, o Origin) Message {
	fields := map[string]any{"state_topic": stateTopic}
	for k, v := range extra {
		fields[k] = v
	}
	m := base(NodeID, "sensor", object, name, icon, fields, o)
	m.Payload["availability"] = []map[string]any{controllerAvailability()}
	if category != "" {
		m.Payload["entity_category"] = category
	}
	return m
}

// MQTT drops entity_picture from json attributes, so the thumbnail needs its own entity.
func thumbnailImage(o Origin) Message {
	m := base(NodeID, "image", "thumbnail", "Thumbnail", "", map[string]any{"url_topic": ThumbnailURLState}, o)
	delete(m.Payload, "icon")
	m.Payload["availability"] = []map[string]any{
		controllerAvailability(),
		{"topic": ThumbnailAvail, "payload_available": PayloadOnline, "payload_not_available": PayloadOffline},
	}
	m.Payload["availability_mode"] = "all"
	return m
}

func binarySensor(node, object, name, stateTopic, deviceClass string, o Origin) Message {
	m := base(node, "binary_sensor", object, name, "", map[string]any{
		"state_topic":  stateTopic,
		"payload_on":   PayloadOn,
		"payload_off":  PayloadOff,
		"device_class": deviceClass,
	}, o)
	delete(m.Payload, "icon")
	m.Payload["availability"] = []map[string]any{controllerAvailability()}
	return m
}

func button(node, object, name, icon, pressTopic, availabilityTopic string, o Origin) Message {
	m := base(node, "button", object, name, icon, map[string]any{
		"command_topic": pressTopic,
		"payload_press": PayloadPress,
		"retain":        false,
		"qos":           1,
	}, o)
	m.Payload["availability"] = []map[string]any{
		controllerAvailability(),
		authAvailability(),
		{"topic": availabilityTopic, "payload_available": PayloadOnline, "payload_not_available": PayloadOffline},
	}
	m.Payload["availability_mode"] = "all"
	return m
}

func authSensor(o Origin) Message {
	m := base(NodeID, "sensor", "authorization", "Authorization", iconAuth, map[string]any{"state_topic": AuthState}, o)
	m.Payload["entity_category"] = "diagnostic"
	m.Payload["availability"] = []map[string]any{controllerAvailability()}
	return m
}

func base(node, component, object, name, icon string, fields map[string]any, o Origin) Message {
	payload := make(map[string]any, len(fields)+8)
	for k, v := range fields {
		payload[k] = v
	}
	payload["name"] = name
	payload["unique_id"] = node + "_" + object
	payload["object_id"] = node + "_" + object
	payload["icon"] = icon
	payload["origin"] = origin(o)
	if node == SchedulingNodeID {
		payload["device"] = schedulingDevice(o)
	} else {
		payload["device"] = controllerDevice(o)
	}
	return Message{Topic: HADiscoveryTopic(component, node, object), Payload: payload}
}

func origin(o Origin) map[string]any {
	return map[string]any{"name": "YouTube Live Control", "sw_version": o.Version, "support_url": o.SupportURL}
}

func controllerAvailability() map[string]any {
	return map[string]any{"topic": ControllerAvailability, "payload_available": PayloadOnline, "payload_not_available": PayloadOffline}
}

func authAvailability() map[string]any {
	return map[string]any{"topic": AuthState, "payload_available": PayloadAuthorized, "payload_not_available": PayloadUnauthorized}
}

func controllerDevice(o Origin) map[string]any {
	return map[string]any{
		"identifiers":  []string{Identifier},
		"name":         controllerName,
		"manufacturer": "youtube-live-control add-on",
		"model":        "Selected broadcast",
		"sw_version":   o.Version,
	}
}

func schedulingDevice(o Origin) map[string]any {
	return map[string]any{
		"identifiers":  []string{SchedulingIdentifier},
		"name":         schedulingName,
		"manufacturer": "youtube-live-control add-on",
		"model":        "Scheduling from presets",
		"sw_version":   o.Version,
		"via_device":   Identifier,
	}
}
