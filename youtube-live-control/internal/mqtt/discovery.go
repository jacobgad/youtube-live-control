package mqtt

import "encoding/json"

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

const (
	controllerName = "YouTube Live"

	iconBroadcast = "mdi:youtube"
	iconTitle     = "mdi:format-title"
	iconScheduled = "mdi:calendar-clock"
	iconThumbnail = "mdi:image"
	iconFastMode  = "mdi:speedometer"
	iconFastLeft  = "mdi:timer-outline"
	iconHealth    = "mdi:pulse"
	iconStatus    = "mdi:broadcast"
	iconViewers   = "mdi:account-eye"
	iconSave      = "mdi:content-save"
	iconCreate    = "mdi:plus-box"
	iconGoLive    = "mdi:play-circle"
	iconEnd       = "mdi:stop-circle"
	iconAuth      = "mdi:shield-account"

	// MaxTitleLength is YouTube's limit for a broadcast title.
	MaxTitleLength = 100

	// scheduledPattern accepts "2006-01-02 15:04" (as published), the same with a
	// T separator, and full RFC 3339; empty clears the draft.
	scheduledPattern = `^(\d{4}-\d{2}-\d{2}[ T]\d{2}:\d{2}(:\d{2}(Z|[+-]\d{2}:?\d{2})?)?)?$`
)

// Messages lists every discovery config for the single YouTube Live device. The two
// selects embed their option lists, so this is republished whenever options change;
// retained configs make that idempotent.
func Messages(o Origin, broadcastOptions, thumbnailOptions []string) []Message {
	if broadcastOptions == nil {
		broadcastOptions = []string{}
	}
	if thumbnailOptions == nil {
		thumbnailOptions = []string{}
	}
	msgs := make([]Message, 0, 14+len(TunableSpecs))
	msgs = append(msgs,
		commandEntity("select", "broadcast", "Broadcast", iconBroadcast, map[string]any{
			"state_topic":   BroadcastState,
			"command_topic": BroadcastSet,
			"options":       broadcastOptions,
		}, o),
		commandEntity("text", "title", "Title", iconTitle, map[string]any{
			"state_topic":   TitleState,
			"command_topic": TitleSet,
			"min":           0,
			"max":           MaxTitleLength,
			"mode":          "text",
		}, o),
		commandEntity("text", "scheduled_start", "Scheduled start", iconScheduled, map[string]any{
			"state_topic":   ScheduledState,
			"command_topic": ScheduledSet,
			"min":           0,
			"max":           25,
			"mode":          "text",
			"pattern":       scheduledPattern,
		}, o),
		commandEntity("select", "thumbnail", "Thumbnail", iconThumbnail, map[string]any{
			"state_topic":   ThumbnailState,
			"command_topic": ThumbnailSet,
			"options":       thumbnailOptions,
		}, o),
		commandEntity("switch", "fast_mode", "Fast refresh", iconFastMode, map[string]any{
			"state_topic":   FastModeState,
			"command_topic": FastModeSet,
			"payload_on":    PayloadOn,
			"payload_off":   PayloadOff,
		}, o),
		sensor("fast_mode_remaining", "Fast refresh remaining", iconFastLeft, FastRemainingState, map[string]any{"unit_of_measurement": "min"}, o),
		sensor("stream_health", "Stream health", iconHealth, HealthState, nil, o),
		sensor("broadcast_status", "Broadcast status", iconStatus, StatusState, nil, o),
		sensor("viewers", "Viewers", iconViewers, ViewersState, map[string]any{"state_class": "measurement"}, o),
		button("save", "Save", iconSave, SavePress, SaveAvailability, o),
		button("create", "Create", iconCreate, CreatePress, CreateAvailability, o),
		button("go_live", "Go Live", iconGoLive, GoLivePress, GoLiveAvailability, o),
		button("end_stream", "End Stream", iconEnd, EndPress, EndAvailability, o),
		authSensor(o),
	)
	for _, spec := range TunableSpecs {
		msgs = append(msgs, numberEntity(spec, o))
	}
	return msgs
}

func numberEntity(spec NumberSpec, o Origin) Message {
	m := base("number", spec.Object, spec.Name, spec.Icon, map[string]any{
		"state_topic":         NumberState(spec.Object),
		"command_topic":       NumberSet(spec.Object),
		"min":                 spec.Min,
		"max":                 spec.Max,
		"step":                1,
		"mode":                "box",
		"unit_of_measurement": spec.Unit,
		"optimistic":          false,
		"retain":              false,
		"qos":                 1,
	}, o)
	m.Payload["entity_category"] = "config"
	m.Payload["availability"] = []map[string]any{controllerAvailability()}
	return m
}

func commandEntity(component, object, name, icon string, fields map[string]any, o Origin) Message {
	fields["optimistic"] = false
	fields["retain"] = false
	fields["qos"] = 1
	m := base(component, object, name, icon, fields, o)
	m.Payload["availability"] = []map[string]any{controllerAvailability(), authAvailability()}
	m.Payload["availability_mode"] = "all"
	return m
}

func sensor(object, name, icon, stateTopic string, extra map[string]any, o Origin) Message {
	fields := map[string]any{"state_topic": stateTopic}
	for k, v := range extra {
		fields[k] = v
	}
	m := base("sensor", object, name, icon, fields, o)
	m.Payload["availability"] = []map[string]any{controllerAvailability()}
	return m
}

func button(object, name, icon, pressTopic, availabilityTopic string, o Origin) Message {
	m := base("button", object, name, icon, map[string]any{
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
	m := base("sensor", "authorization", "Authorization", iconAuth, map[string]any{"state_topic": AuthState}, o)
	m.Payload["entity_category"] = "diagnostic"
	m.Payload["availability"] = []map[string]any{controllerAvailability()}
	return m
}

func base(component, object, name, icon string, fields map[string]any, o Origin) Message {
	payload := map[string]any{}
	for k, v := range fields {
		payload[k] = v
	}
	payload["name"] = name
	payload["unique_id"] = NodeID + "_" + object
	payload["object_id"] = NodeID + "_" + object
	payload["icon"] = icon
	payload["device"] = device(o)
	payload["origin"] = origin(o)
	return Message{Topic: HADiscoveryTopic(component, object), Payload: payload}
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

func device(o Origin) map[string]any {
	return map[string]any{
		"identifiers":  []string{Identifier},
		"name":         controllerName,
		"manufacturer": "youtube-live-control add-on",
		"model":        "YouTube Live MQTT bridge",
		"sw_version":   o.Version,
	}
}
