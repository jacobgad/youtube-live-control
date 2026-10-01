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

// StreamDevice is one custom stream key as a Home Assistant device.
type StreamDevice struct {
	DeviceID   string
	Name       string
	Broadcasts []string
}

// Options are the device roster and option lists embedded in the discovery configs;
// discovery is republished when they change.
type Options struct {
	Streams []StreamDevice
	Presets []string
}

const (
	hubName        = "YouTube Live Control"
	schedulingName = "YouTube Live Scheduling"

	iconBroadcast = "mdi:youtube"
	iconTitle     = "mdi:format-title"
	iconPrivacy   = "mdi:eye-lock-outline"
	iconFastMode  = "mdi:speedometer"
	iconFastLeft  = "mdi:timer-outline"
	iconStage     = "mdi:progress-check"
	iconHealth    = "mdi:pulse"
	iconStreamKey = "mdi:key-variant"
	iconStatus    = "mdi:broadcast"
	iconGoLive    = "mdi:play-circle"
	iconEnd       = "mdi:stop-circle"
	iconDelete    = "mdi:delete-outline"
	iconAuth      = "mdi:shield-account"
	iconPreset    = "mdi:playlist-star"
	iconStart     = "mdi:calendar-clock"
	iconSchedule  = "mdi:calendar-plus"

	// MaxTitleLength is YouTube's limit.
	MaxTitleLength = 100
)

// Messages lists every discovery config: the hub, one device per custom stream key,
// and the scheduling device.
func Messages(o Origin, opts Options) []Message {
	out := hubMessages(o)
	for _, s := range opts.Streams {
		out = append(out, streamMessages(s, o)...)
	}
	return append(out, schedulingMessages(opts.Presets, o)...)
}

func hubMessages(o Origin) []Message {
	device := hubDevice(o)
	fastMode := base(HubNodeID, "switch", "fast_mode", "Fast refresh", iconFastMode, map[string]any{
		"state_topic":   FastModeState,
		"command_topic": FastModeSet,
		"payload_on":    PayloadOn,
		"payload_off":   PayloadOff,
		"optimistic":    false,
		"retain":        false,
		"qos":           1,
	}, device, o)
	fastMode.Payload["availability"] = []map[string]any{controllerAvailability(), authAvailability()}
	fastMode.Payload["availability_mode"] = "all"
	return []Message{
		diagnosticSensor(HubNodeID, "authorization", "Authorization", iconAuth, AuthState, device, o),
		diagnosticSensor(HubNodeID, "channel", "Channel", iconBroadcast, ChannelState, device, o),
		fastMode,
		sensor(HubNodeID, "fast_mode_remaining", "Fast refresh remaining", iconFastLeft, FastRemainingState, map[string]any{"unit_of_measurement": "min"}, "", device, o),
	}
}

func streamMessages(s StreamDevice, o Origin) []Message {
	t := StreamTopics{Device: s.DeviceID}
	node := t.Node()
	device := streamDevice(s, o)
	return []Message{
		commandEntity(node, "select", "broadcast", "Broadcast", iconBroadcast, t.Lock(), map[string]any{
			"state_topic":           t.BroadcastState(),
			"command_topic":         t.BroadcastSet(),
			"json_attributes_topic": t.BroadcastAttributes(),
			"options":               orEmpty(s.Broadcasts),
		}, device, o),
		commandEntity(node, "text", "title", "Title", iconTitle, t.Lock(), map[string]any{
			"state_topic":   t.TitleState(),
			"command_topic": t.TitleSet(),
			"min":           0,
			"max":           MaxTitleLength,
			"mode":          "text",
		}, device, o),
		commandEntity(node, "select", "privacy", "Privacy", iconPrivacy, t.Lock(), map[string]any{
			"state_topic":   t.PrivacyState(),
			"command_topic": t.PrivacySet(),
			"options":       youtube.PrivacyOptions,
		}, device, o),
		thumbnailImage(node, t, device, o),
		sensor(node, "stage", "Stage", iconStage, t.StageState(), map[string]any{"device_class": "enum", "options": StageOptions}, "", device, o),
		sensor(node, "scheduled_start", "Scheduled start", iconStart, t.ScheduledStartState(), map[string]any{"device_class": "timestamp"}, "", device, o),
		binarySensor(node, "live", "Live", t.LiveState(), "running", device, o),
		binarySensor(node, "encoder", "Encoder connected", t.EncoderState(), "connectivity", device, o),
		button(node, "go_live", "Go Live", iconGoLive, t.GoLivePress(), t.Lock(), t.GoLiveAvailability(), device, o),
		button(node, "end_stream", "End Stream", iconEnd, t.EndPress(), t.Lock(), t.EndAvailability(), device, o),
		button(node, "delete", "Delete", iconDelete, t.DeletePress(), t.Lock(), t.DeleteAvailability(), device, o),
		sensor(node, "broadcast_status", "Broadcast status", iconStatus, t.StatusState(), nil, "diagnostic", device, o),
		sensor(node, "stream_health", "Stream health", iconHealth, t.HealthState(), nil, "diagnostic", device, o),
		sensor(node, "stream_key", "Stream key", iconStreamKey, t.StreamKeyState(), nil, "diagnostic", device, o),
	}
}

func schedulingMessages(presets []string, o Origin) []Message {
	device := schedulingDevice(o)
	return []Message{
		commandEntity(SchedulingNodeID, "select", "preset", "Preset", iconPreset, SchedulingLock, map[string]any{
			"state_topic":   PresetState,
			"command_topic": PresetSet,
			"options":       orEmpty(presets),
		}, device, o),
		commandEntity(SchedulingNodeID, "datetime", "start", "Start", iconStart, SchedulingLock, map[string]any{
			"state_topic":   StartState,
			"command_topic": StartSet,
		}, device, o),
		commandEntity(SchedulingNodeID, "select", "privacy", "Privacy", iconPrivacy, SchedulingLock, map[string]any{
			"state_topic":   SchedulePrivacyState,
			"command_topic": SchedulePrivacySet,
			"options":       youtube.PrivacyOptions,
		}, device, o),
		button(SchedulingNodeID, "schedule", "Schedule", iconSchedule, SchedulePress, SchedulingLock, ScheduleAvailability, device, o),
	}
}

// StreamConfigTopics lists a stream device's discovery config topics, so a retired
// device can be removed from Home Assistant. Must cover streamMessages exactly.
func StreamConfigTopics(deviceID string) []string {
	node := StreamNodePrefix + deviceID
	return []string{
		HADiscoveryTopic("select", node, "broadcast"),
		HADiscoveryTopic("text", node, "title"),
		HADiscoveryTopic("select", node, "privacy"),
		HADiscoveryTopic("image", node, "thumbnail"),
		HADiscoveryTopic("sensor", node, "stage"),
		HADiscoveryTopic("sensor", node, "scheduled_start"),
		HADiscoveryTopic("binary_sensor", node, "live"),
		HADiscoveryTopic("binary_sensor", node, "encoder"),
		HADiscoveryTopic("button", node, "go_live"),
		HADiscoveryTopic("button", node, "end_stream"),
		HADiscoveryTopic("button", node, "delete"),
		HADiscoveryTopic("sensor", node, "broadcast_status"),
		HADiscoveryTopic("sensor", node, "stream_health"),
		HADiscoveryTopic("sensor", node, "stream_key"),
	}
}

func orEmpty(list []string) []string {
	if list == nil {
		return []string{}
	}
	return list
}

// Each device's inputs share that device's lock: while a change for it is in flight
// to YouTube nothing on it can be acted on, and everything returns together on
// readback. Other devices stay available throughout.
func commandEntity(node, component, object, name, icon, lockTopic string, fields map[string]any, device map[string]any, o Origin) Message {
	fields["optimistic"] = false
	fields["retain"] = false
	fields["qos"] = 1
	m := base(node, component, object, name, icon, fields, device, o)
	m.Payload["availability"] = []map[string]any{controllerAvailability(), authAvailability(), lockAvailability(lockTopic)}
	m.Payload["availability_mode"] = "all"
	return m
}

func lockAvailability(lockTopic string) map[string]any {
	return map[string]any{"topic": lockTopic, "payload_available": PayloadOnline, "payload_not_available": PayloadOffline}
}

func sensor(node, object, name, icon, stateTopic string, extra map[string]any, category string, device map[string]any, o Origin) Message {
	fields := map[string]any{"state_topic": stateTopic}
	for k, v := range extra {
		fields[k] = v
	}
	m := base(node, "sensor", object, name, icon, fields, device, o)
	m.Payload["availability"] = []map[string]any{controllerAvailability()}
	if category != "" {
		m.Payload["entity_category"] = category
	}
	return m
}

func diagnosticSensor(node, object, name, icon, stateTopic string, device map[string]any, o Origin) Message {
	return sensor(node, object, name, icon, stateTopic, nil, "diagnostic", device, o)
}

// MQTT drops entity_picture from json attributes, so the thumbnail needs its own entity.
func thumbnailImage(node string, t StreamTopics, device map[string]any, o Origin) Message {
	m := base(node, "image", "thumbnail", "Thumbnail", "", map[string]any{"url_topic": t.ThumbnailURLState()}, device, o)
	delete(m.Payload, "icon")
	m.Payload["availability"] = []map[string]any{
		controllerAvailability(),
		{"topic": t.ThumbnailAvail(), "payload_available": PayloadOnline, "payload_not_available": PayloadOffline},
	}
	m.Payload["availability_mode"] = "all"
	return m
}

func binarySensor(node, object, name, stateTopic, deviceClass string, device map[string]any, o Origin) Message {
	m := base(node, "binary_sensor", object, name, "", map[string]any{
		"state_topic":  stateTopic,
		"payload_on":   PayloadOn,
		"payload_off":  PayloadOff,
		"device_class": deviceClass,
	}, device, o)
	delete(m.Payload, "icon")
	m.Payload["availability"] = []map[string]any{controllerAvailability()}
	return m
}

func button(node, object, name, icon, pressTopic, lockTopic, availabilityTopic string, device map[string]any, o Origin) Message {
	m := base(node, "button", object, name, icon, map[string]any{
		"command_topic": pressTopic,
		"payload_press": PayloadPress,
		"retain":        false,
		"qos":           1,
	}, device, o)
	m.Payload["availability"] = []map[string]any{
		controllerAvailability(),
		authAvailability(),
		lockAvailability(lockTopic),
		{"topic": availabilityTopic, "payload_available": PayloadOnline, "payload_not_available": PayloadOffline},
	}
	m.Payload["availability_mode"] = "all"
	return m
}

func base(node, component, object, name, icon string, fields map[string]any, device map[string]any, o Origin) Message {
	payload := make(map[string]any, len(fields)+8)
	for k, v := range fields {
		payload[k] = v
	}
	payload["name"] = name
	payload["unique_id"] = node + "_" + object
	payload["object_id"] = node + "_" + object
	payload["icon"] = icon
	payload["origin"] = origin(o)
	payload["device"] = device
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

func hubDevice(o Origin) map[string]any {
	return map[string]any{
		"identifiers":  []string{HubIdentifier},
		"name":         hubName,
		"manufacturer": "youtube-live-control add-on",
		"model":        "Channel hub",
		"sw_version":   o.Version,
	}
}

func streamDevice(s StreamDevice, o Origin) map[string]any {
	name := s.Name
	if name == "" {
		name = "Stream " + s.DeviceID
	}
	return map[string]any{
		"identifiers":  []string{StreamIdentifierPrefix + s.DeviceID},
		"name":         name,
		"manufacturer": "youtube-live-control add-on",
		"model":        "Stream key",
		"sw_version":   o.Version,
		"via_device":   HubIdentifier,
	}
}

func schedulingDevice(o Origin) map[string]any {
	return map[string]any{
		"identifiers":  []string{SchedulingIdentifier},
		"name":         schedulingName,
		"manufacturer": "youtube-live-control add-on",
		"model":        "Scheduling from presets",
		"sw_version":   o.Version,
		"via_device":   HubIdentifier,
	}
}
