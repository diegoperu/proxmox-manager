package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestClusterTaskAttribution(t *testing.T) {
	payload := `[{"upid":"UPID:known","user":"pmgr@pve!manager","extra":{"n":9007199254740993}},{"upid":"UPID:external","user":"root@pam"},{"user":"root@pam","unknown":true}]`
	h := New()
	for _, mode := range []string{"attributed", "no hook", "hook error", "malformed envelope", "wrong array shape", "null task"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			h.SetTaskAttribution(func(upids []string) (map[string]string, error) {
				calls++
				if !reflect.DeepEqual(upids, []string{"UPID:known", "UPID:external"}) {
					t.Errorf("upids: %v", upids)
				}
				if mode == "hook error" {
					return nil, errors.New("private lookup error")
				}
				return map[string]string{"UPID:known": "anna"}, nil
			})
			if mode == "no hook" {
				h.SetTaskAttribution(nil)
			}
			original := payload
			defer func() { payload = original }()
			switch mode {
			case "malformed envelope":
				payload = `[{`
			case "wrong array shape":
				payload = `{"upid":"UPID:known"}`
			case "null task":
				payload = `[null]`
			}
			var logs bytes.Buffer
			old := log.Writer()
			log.SetOutput(&logs)
			defer log.SetOutput(old)
			w := httptest.NewRecorder()
			h.writeClusterTasks(w, json.RawMessage(payload))
			if strings.Contains(mode, "malformed") || mode == "wrong array shape" || mode == "null task" {
				if w.Code != 502 || calls != 0 {
					t.Fatalf("got %d, calls %d", w.Code, calls)
				}
				return
			}
			if w.Code != 200 {
				t.Fatalf("%d: %s", w.Code, w.Body)
			}
			var got []map[string]json.RawMessage
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			var want []map[string]json.RawMessage
			if err := json.Unmarshal([]byte(original), &want); err != nil {
				t.Fatal(err)
			}
			if mode == "attributed" {
				want[0]["pmgr_user"] = json.RawMessage(`"anna"`)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("fields changed: %s", w.Body)
			}
			if mode == "hook error" && (strings.Count(logs.String(), "attribuzione") != 1 || strings.Contains(logs.String(), "private")) {
				t.Fatalf("unsafe or repeated log: %s", logs.String())
			}
		})
	}
}

func TestTaskAttributionBatchesAreBoundedAndAtomic(t *testing.T) {
	h := New()
	tasks := make([]map[string]json.RawMessage, 2001)
	for i := range tasks {
		u, _ := json.Marshal(fmt.Sprintf("UPID:%d", i))
		tasks[i] = map[string]json.RawMessage{"upid": u}
	}
	calls := 0
	h.SetTaskAttribution(func(upids []string) (map[string]string, error) {
		calls++
		if len(upids) > 1000 {
			t.Fatal("unbounded lookup")
		}
		out := map[string]string{}
		for _, u := range upids {
			out[u] = "anna"
		}
		return out, nil
	})
	users, err := h.taskUsers(tasks)
	if err != nil || len(users) != 2001 || calls != 3 {
		t.Fatalf("%d users, %d calls, %v", len(users), calls, err)
	}
	calls = 0
	h.SetTaskAttribution(func(upids []string) (map[string]string, error) {
		calls++
		if calls == 2 {
			return nil, errors.New("lookup failed")
		}
		return map[string]string{upids[0]: "anna"}, nil
	})
	if users, err := h.taskUsers(tasks); err == nil || users != nil {
		t.Fatalf("partial attribution leaked: %v %v", users, err)
	}
	for _, task := range tasks {
		if _, ok := task["pmgr_user"]; ok {
			t.Fatal("tasks mutated before successful lookup")
		}
	}
}

type attributionTaskStatus struct{ raw json.RawMessage }

func (s attributionTaskStatus) GetTaskStatus(node, upid string) (json.RawMessage, error) {
	return s.raw, nil
}

func TestBalanceTaskPreservesProxmoxUser(t *testing.T) {
	task, err := readTask(attributionTaskStatus{json.RawMessage(`{"status":"running","user":"pmgr@pve!local","future":true}`)}, "n1", "UPID:known")
	if err != nil || task.User != "pmgr@pve!local" || task.Status != "running" {
		t.Fatalf("%+v %v", task, err)
	}
}
