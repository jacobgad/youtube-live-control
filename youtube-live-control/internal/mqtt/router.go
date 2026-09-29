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
	ScheduledEntered    func(raw string)
	ThumbnailSelected   func(label string)
	FastModeSwitched    func(on bool)
	NumberEntered       func(object, raw string)
	SavePressed         func()
	CreatePressed       func()
	GoLivePressed       func()
	EndPressed          func()
	HomeAssistantOnline func()
}

// Subscriptions are the topics the controller listens on.
var Subscriptions = func() []string {
	topics := make([]string, 0, 10+len(TunableSpecs))
	topics = append(topics,
		BroadcastSet, TitleSet, ScheduledSet, ThumbnailSet, FastModeSet,
		SavePress, CreatePress, GoLivePress, EndPress,
		HAStatusTopic,
	)
	for _, spec := range TunableSpecs {
		topics = append(topics, NumberSet(spec.Object))
	}
	return topics
}()

// NewRouter maps inbound topics to Actions.
func NewRouter(actions Actions, log *slog.Logger) MessageHandler {
	numberSets := map[string]string{}
	for _, spec := range TunableSpecs {
		numberSets[NumberSet(spec.Object)] = spec.Object
	}
	return func(topic string, payload []byte) {
		if object, ok := numberSets[topic]; ok {
			actions.NumberEntered(object, strings.TrimSpace(string(payload)))
			return
		}
		switch topic {
		case BroadcastSet:
			actions.BroadcastSelected(strings.TrimSpace(string(payload)))
		case TitleSet:
			actions.TitleEntered(string(payload))
		case ScheduledSet:
			actions.ScheduledEntered(strings.TrimSpace(string(payload)))
		case ThumbnailSet:
			actions.ThumbnailSelected(strings.TrimSpace(string(payload)))
		case FastModeSet:
			switch strings.ToUpper(strings.TrimSpace(string(payload))) {
			case PayloadOn:
				actions.FastModeSwitched(true)
			case PayloadOff:
				actions.FastModeSwitched(false)
			default:
				log.Warn("fast_mode_command_invalid", "payload", string(payload))
			}
		case SavePress:
			actions.SavePressed()
		case CreatePress:
			actions.CreatePressed()
		case GoLivePress:
			actions.GoLivePressed()
		case EndPress:
			actions.EndPressed()
		case HAStatusTopic:
			if strings.TrimSpace(string(payload)) == PayloadOnline {
				log.Info("home_assistant_online")
				actions.HomeAssistantOnline()
			}
		default:
			log.Debug("mqtt_message_ignored", "topic", topic)
		}
	}
}
