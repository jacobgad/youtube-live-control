package mqtt

import (
	"log/slog"
	"strings"
)

// Actions are invoked synchronously from the broker's delivery goroutine, so each
// must return quickly; the controller mutates its session inline and does the API
// work in the background, which is what preserves command order. Stream-device
// actions receive the DeviceID from the topic.
type Actions struct {
	BroadcastSelected   func(deviceID, label string)
	TitleEntered        func(deviceID, raw string)
	PrivacySelected     func(deviceID, privacy string)
	GoLivePressed       func(deviceID string)
	EndPressed          func(deviceID string)
	DeletePressed       func(deviceID string)
	FastModeSwitched    func(on bool)
	PresetSelected      func(label string)
	StartEntered        func(raw string)
	SchedulePrivacy     func(privacy string)
	SchedulePressed     func()
	HomeAssistantOnline func()
}

// NewRouter maps inbound topics to Actions.
func NewRouter(actions Actions, log *slog.Logger) MessageHandler {
	return func(topic string, payload []byte) {
		text := strings.TrimSpace(string(payload))
		if deviceID, object, leaf, ok := parseStreamTopic(topic); ok {
			switch {
			case object == "broadcast" && leaf == "set":
				actions.BroadcastSelected(deviceID, text)
			case object == "title" && leaf == "set":
				actions.TitleEntered(deviceID, string(payload))
			case object == "privacy" && leaf == "set":
				actions.PrivacySelected(deviceID, text)
			case object == "go_live" && leaf == "press":
				actions.GoLivePressed(deviceID)
			case object == "end_stream" && leaf == "press":
				actions.EndPressed(deviceID)
			case object == "delete" && leaf == "press":
				actions.DeletePressed(deviceID)
			default:
				log.Debug("mqtt_message_ignored", "topic", topic)
			}
			return
		}
		switch topic {
		case FastModeSet:
			switch strings.ToUpper(text) {
			case PayloadOn:
				actions.FastModeSwitched(true)
			case PayloadOff:
				actions.FastModeSwitched(false)
			default:
				log.Warn("fast_mode_command_invalid", "payload", text)
			}
		case SchedulePrivacySet:
			actions.SchedulePrivacy(text)
		case PresetSet:
			actions.PresetSelected(text)
		case StartSet:
			actions.StartEntered(text)
		case SchedulePress:
			actions.SchedulePressed()
		case HAStatusTopic:
			if text == PayloadOnline {
				log.Info("home_assistant_online")
				actions.HomeAssistantOnline()
			}
		default:
			log.Debug("mqtt_message_ignored", "topic", topic)
		}
	}
}
