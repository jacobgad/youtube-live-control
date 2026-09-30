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
	PresetSelected      func(label string)
	DateEntered         func(raw string)
	TimeEntered         func(raw string)
	SchedulePressed     func()
	HomeAssistantOnline func()
}

// Subscriptions are the topics the controller listens on.
var Subscriptions = []string{
	BroadcastSet, TitleSet, PrivacySet, FastModeSet,
	GoLivePress, EndPress,
	PresetSet, DateSet, TimeSet, SchedulePress,
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
		case PresetSet:
			actions.PresetSelected(text)
		case DateSet:
			actions.DateEntered(text)
		case TimeSet:
			actions.TimeEntered(text)
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
