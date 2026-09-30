package youtube

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const studioBroadcast = `{
  "id": "abc",
  "snippet": {
    "publishedAt": "2025-01-01T00:00:00Z",
    "channelId": "UC1",
    "title": "Sunday Service",
    "description": "Join us",
    "scheduledStartTime": "2025-01-05T09:30:00Z",
    "isDefaultBroadcast": false,
    "liveChatId": "chat1"
  },
  "status": {
    "lifeCycleStatus": "ready",
    "privacyStatus": "unlisted",
    "recordingStatus": "notRecording",
    "madeForKids": false,
    "selfDeclaredMadeForKids": false
  },
  "contentDetails": {
    "boundStreamId": "s1",
    "boundStreamLastUpdateTimeMs": "1700000000000",
    "monitorStream": {"enableMonitorStream": true, "broadcastStreamDelayMs": 0},
    "enableEmbed": true,
    "enableDvr": true,
    "recordFromStart": true,
    "enableClosedCaptions": false,
    "closedCaptionsType": "closedCaptionsDisabled",
    "enableAutoStart": true,
    "enableAutoStop": true,
    "latencyPreference": "low"
  }
}`

func parseStudio(t *testing.T) Broadcast {
	t.Helper()
	var item apiBroadcastItem
	if err := json.Unmarshal([]byte(studioBroadcast), &item); err != nil {
		t.Fatal(err)
	}
	return item.broadcast()
}

func TestBroadcastFromAPI(t *testing.T) {
	b := parseStudio(t)
	if b.ID != "abc" || b.Title != "Sunday Service" || b.Description != "Join us" || b.PrivacyStatus != "unlisted" || b.LifeCycleStatus != LifeReady || b.BoundStreamID != "s1" {
		t.Fatalf("broadcast = %+v", b)
	}
	if !b.MonitorEnabled {
		t.Fatal("monitor stream flag not read")
	}
	if want := time.Date(2025, 1, 5, 9, 30, 0, 0, time.UTC); !b.ScheduledStart.Equal(want) {
		t.Fatalf("scheduled start = %v", b.ScheduledStart)
	}
}

func TestUpdateBodyPreservesUnrelatedSettingsAndForcesAutoFlagsOff(t *testing.T) {
	b := parseStudio(t)
	b.Title = "Renamed"
	body := updateBody(b)
	content := body["contentDetails"].(map[string]any)
	snippet := body["snippet"].(map[string]any)
	status := body["status"].(map[string]any)

	for key, want := range map[string]any{"enableDvr": true, "enableEmbed": true, "recordFromStart": true, "latencyPreference": "low"} {
		if content[key] != want {
			t.Fatalf("contentDetails.%s = %v, want %v (clobbered)", key, content[key], want)
		}
	}
	if content["enableAutoStart"] != false || content["enableAutoStop"] != false {
		t.Fatalf("auto flags not forced off: %v / %v", content["enableAutoStart"], content["enableAutoStop"])
	}
	if snippet["title"] != "Renamed" || snippet["description"] != "Join us" {
		t.Fatalf("snippet = %v", snippet)
	}
	if status["privacyStatus"] != "unlisted" || status["selfDeclaredMadeForKids"] != false {
		t.Fatalf("status = %v", status)
	}
	for part, keys := range readOnlyFields {
		for _, key := range keys {
			if _, present := body[part].(map[string]any)[key]; present {
				t.Fatalf("read-only %s.%s sent in update", part, key)
			}
		}
	}
	if body["id"] != "abc" {
		t.Fatalf("id = %v", body["id"])
	}
}

func TestValidPrivacy(t *testing.T) {
	for _, p := range PrivacyOptions {
		if !ValidPrivacy(p) {
			t.Fatalf("%s should be valid", p)
		}
	}
	if ValidPrivacy("secret") || ValidPrivacy("") {
		t.Fatal("invalid privacy accepted")
	}
}

func TestVideoUpdateBodyPreservesSnippetAndStripsReadOnly(t *testing.T) {
	snippet := map[string]any{"title": "T", "description": "D", "tags": []any{"a"}, "defaultLanguage": "en", "categoryId": "22", "publishedAt": "x", "thumbnails": map[string]any{}, "channelId": "UC"}
	body := videoUpdateBody("v1", snippet, "29")
	got := body["snippet"].(map[string]any)
	if got["categoryId"] != "29" || got["defaultLanguage"] != "en" || got["title"] != "T" {
		t.Fatalf("snippet = %v", got)
	}
	for _, key := range videoSnippetReadOnly {
		if _, present := got[key]; present {
			t.Fatalf("read-only %s sent", key)
		}
	}
	if snippet["categoryId"] != "22" {
		t.Fatal("input snippet must not be mutated")
	}
	if body["id"] != "v1" {
		t.Fatalf("id = %v", body["id"])
	}
}

func TestUpdateBodyWithoutFetchedPartsStillValid(t *testing.T) {
	body := updateBody(Broadcast{ID: "x", Title: "T", ScheduledStart: time.Now(), PrivacyStatus: "public"})
	content := body["contentDetails"].(map[string]any)
	if content["enableAutoStart"] != false || content["enableAutoStop"] != false {
		t.Fatal("auto flags missing when no parts were fetched")
	}
}

func TestInsertBodySerialisesAutoFlagsExplicitlyFalse(t *testing.T) {
	data, err := json.Marshal(newInsertBody(NewBroadcast{Title: "T", Description: "Join us", Start: time.Date(2025, 1, 5, 9, 30, 0, 0, time.UTC), Privacy: "public"}))
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, want := range []string{`"enableAutoStart":false`, `"enableAutoStop":false`, `"enableMonitorStream":false`, `"scheduledStartTime":"2025-01-05T09:30:00Z"`, `"privacyStatus":"public"`, `"description":"Join us"`} {
		if !strings.Contains(s, want) {
			t.Fatalf("insert body %s lacks %s", s, want)
		}
	}
	dataUpdate, _ := json.Marshal(updateBody(parseStudio(t)))
	for _, want := range []string{`"enableAutoStart":false`, `"enableAutoStop":false`} {
		if !strings.Contains(string(dataUpdate), want) {
			t.Fatalf("update body lacks %s", want)
		}
	}
}
