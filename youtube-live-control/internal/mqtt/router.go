package mqtt

import (
	"log/slog"
	"strings"
)

// Actions are invoked synchronously from the broker's delivery goroutine, so each
// must return quickly; the controller mutates its session inline and does the API
// work in the background, which is what preserves command order.
type Actions struct {
	BroadcastSelected   func(label string)
	TitleEntered        func(raw string)
	PrivacySelected     func(privacy string)
	FastModeSwitched    func(on bool)
	GoLivePressed       func()
	EndPressed          func()
	DeletePressed       func()
	PresetSelected      func(label string)
	StartEntered        func(raw string)
	SchedulePrivacy     func(privacy string)
	SchedulePressed     func()
	HomeAssistantOnline func()
}

// Subscriptions are the topics the controller listens on.
var Subscriptions = []string{
	BroadcastSet, TitleSet, PrivacySet, FastModeSet,
	GoLivePress, EndPress, DeletePress,
	PresetSet, StartSet, SchedulePrivacySet, SchedulePress,
	HAStatusTopic,
}

// NewRouter maps inbound topics to Actions.
func NewRouter(actions Actions, log *slog.Logger) MessageHandler {
	return func(topic string, payload []byte) {
		text := strings.TrimSpace(string(payload))
		switch topic {
		case BroadcastSet:
			actions.BroadcastSelected(text)
		case TitleSet:
			actions.TitleEntered(string(payload))
		case PrivacySet:
			actions.PrivacySelected(text)
		case FastModeSet:
			switch strings.ToUpper(text) {
			case PayloadOn:
				actions.FastModeSwitched(true)
			case PayloadOff:
				actions.FastModeSwitched(false)
			default:
				log.Warn("fast_mode_command_invalid", "payload", text)
			}
		case GoLivePress:
			actions.GoLivePressed()
		case EndPress:
			actions.EndPressed()
		case DeletePress:
			actions.DeletePressed()
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
