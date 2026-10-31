package httpapi

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/AcSunday/cocogem/internal/workflow"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestTrashLifecycleUsesConfiguredRetention(t *testing.T) {
	s, db, cookie, csrf := pagingServer(t, workflow.DefaultLimits())
	purgeWindow := func(purgeAt string, days int) {
		t.Helper()
		at, err := time.Parse(time.RFC3339Nano, purgeAt)
		if err != nil {
			t.Fatalf("purge_at %q is not RFC3339Nano: %v", purgeAt, err)
		}
		got := time.Until(at)
		want := time.Duration(days) * 24 * time.Hour
		if got < want-time.Minute || got > want+time.Minute {
			t.Fatalf("purge window=%v, want about %d days", got, days)
		}
	}
	deleteThread := func(thread w.Thread) DeleteReply {
		t.Helper()
		rw := apiCall(s, "DELETE", "/api/v1/threads/"+thread.ID, nil, cookie, csrf, nil)
		if rw.Code != 200 {
			t.Fatalf("delete got %d: %s", rw.Code, rw.Body.String())
		}
		var reply DeleteReply
		if err := json.Unmarshal(rw.Body.Bytes(), &reply); err != nil {
			t.Fatal(err)
		}
		return reply
	}
	settings := func() w.Settings {
		t.Helper()
		rw := apiCall(s, "GET", "/api/v1/settings", nil, cookie, csrf, nil)
		var value w.Settings
		if err := json.Unmarshal(rw.Body.Bytes(), &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	setRetention := func(days, revision int) w.Settings {
		t.Helper()
		rw := apiCall(s, "PATCH", "/api/v1/settings", SettingsInput{RetentionDays: &days}, cookie, csrf, map[string]string{"If-Match": strconv.Itoa(revision)})
		if rw.Code != 200 {
			t.Fatalf("retention update got %d: %s", rw.Code, rw.Body.String())
		}
		var value w.Settings
		if err := json.Unmarshal(rw.Body.Bytes(), &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	trash := func() TrashList {
		t.Helper()
		rw := apiCall(s, "GET", "/api/v1/trash", nil, cookie, csrf, nil)
		var value TrashList
		if err := json.Unmarshal(rw.Body.Bytes(), &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	find := func(list TrashList, id string) w.Thread {
		t.Helper()
		for _, item := range list.Items {
			if item.Thread.ID == id {
				return item.Thread
			}
		}
		t.Fatalf("trash has no thread %s: %+v", id, list.Items)
		return w.Thread{}
	}

	// No configured value keeps the 7-day default.
	first, _ := seedThread(t, db)
	purgeWindow(deleteThread(first).PurgeAt, w.DefaultRetentionDays)

	// A changed retention also reschedules conversations already in the trash.
	current := settings()
	extended := setRetention(45, current.Revision)
	item := find(trash(), first.ID)
	purgeWindow(item.PurgeAt, 45)
	shortened := setRetention(7, extended.Revision)
	item = find(trash(), first.ID)
	purgeWindow(item.PurgeAt, 7)

	// A new delete immediately uses the current period.
	second, _ := seedThread(t, db)
	purgeWindow(deleteThread(second).PurgeAt, 7)

	list := trash()
	if len(list.Items) != 2 || list.Items[0].WorkspaceName == "" {
		t.Fatalf("trash projection=%+v", list.Items)
	}

	rw := apiCall(s, "POST", "/api/v1/threads/"+first.ID+"/restore", map[string]any{}, cookie, csrf, map[string]string{"If-Match": "2"})
	if rw.Code != 200 {
		t.Fatalf("restore got %d: %s", rw.Code, rw.Body.String())
	}
	var restored w.Thread
	if err := json.Unmarshal(rw.Body.Bytes(), &restored); err != nil {
		t.Fatal(err)
	}
	if restored.DeletedAt != "" || restored.PurgeAt != "" {
		t.Fatalf("restore kept trash fields: %+v", restored)
	}
	// Only trashed threads can be permanently deleted; restoring makes it live.
	rw = apiCall(s, "DELETE", "/api/v1/threads/"+first.ID+"/permanent", nil, cookie, csrf, map[string]string{"If-Match": strconv.Itoa(restored.Revision)})
	if rw.Code != 404 {
		t.Fatalf("live thread permanently deleted: %d %s", rw.Code, rw.Body.String())
	}
	rw = apiCall(s, "DELETE", "/api/v1/threads/"+second.ID+"/permanent", nil, cookie, csrf, map[string]string{"If-Match": "2"})
	if rw.Code != 200 {
		t.Fatalf("permanent delete got %d: %s", rw.Code, rw.Body.String())
	}
	if len(trash().Items) != 0 {
		t.Fatalf("trash still lists deleted threads")
	}

	for _, days := range []int{0, w.MaxRetentionDays + 1} {
		value := days
		rw = apiCall(s, "PATCH", "/api/v1/settings", SettingsInput{RetentionDays: &value}, cookie, csrf, map[string]string{"If-Match": strconv.Itoa(shortened.Revision)})
		if rw.Code != 400 {
			t.Fatalf("retention %d got %d: %s", days, rw.Code, rw.Body.String())
		}
	}
}
