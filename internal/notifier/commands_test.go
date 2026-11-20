package notifier

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// El bot solo contesta al chat configurado, con botones; un botón edita el mismo mensaje.
func TestPollCommandsOnlyAuthorizedChat(t *testing.T) {
	var mu sync.Mutex
	calls := map[string][]map[string]interface{}{}
	served := false
	now := time.Now().Unix()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		if method == "getUpdates" {
			mu.Lock()
			first := !served
			served = true
			mu.Unlock()
			if !first {
				<-ctx.Done()
				return
			}
			fmt.Fprintf(w, `{"ok":true,"result":[
				{"update_id":1,"message":{"date":%d,"text":"/estado","chat":{"id":42}}},
				{"update_id":2,"message":{"date":%d,"text":"/estado","chat":{"id":666}}},
				{"update_id":3,"callback_query":{"id":"cb","data":"progreso","message":{"message_id":9,"chat":{"id":42}}}},
				{"update_id":4,"callback_query":{"id":"x","data":"estado","message":{"message_id":5,"chat":{"id":666}}}}]}`, now, now)
			return
		}
		var m map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&m)
		mu.Lock()
		calls[method] = append(calls[method], m)
		mu.Unlock()
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	old, oldDefault := apiBase, Default
	apiBase = srv.URL
	Default = &TelegramNotifier{client: srv.Client(), lastAlerts: map[string]time.Time{}}
	t.Cleanup(func() { apiBase, Default = old, oldDefault })
	Default.UpdateCredentials("123:abc", "42")

	done := make(chan struct{})
	go func() {
		PollCommands(ctx, []Command{{Name: "estado", Description: "x"}}, [][]Button{{{Text: "Estado", Data: "estado"}}},
			func(cmd, _ string) string { return "respuesta " + cmd })
		close(done)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(calls["sendMessage"]) + len(calls["editMessageText"])
		mu.Unlock()
		if n >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done
	mu.Lock()
	defer mu.Unlock()
	if len(calls["sendMessage"]) != 1 || calls["sendMessage"][0]["chat_id"] != "42" || calls["sendMessage"][0]["text"] != "respuesta estado" {
		t.Fatalf("sendMessage: %v", calls["sendMessage"])
	}
	if calls["sendMessage"][0]["reply_markup"] == nil {
		t.Fatal("la respuesta debe llevar botones")
	}
	if len(calls["editMessageText"]) != 1 || calls["editMessageText"][0]["text"] != "respuesta progreso" {
		t.Fatalf("editMessageText: %v", calls["editMessageText"])
	}
	if len(calls["answerCallbackQuery"]) != 1 || len(calls["setMyCommands"]) != 1 {
		t.Fatalf("answerCallbackQuery %d, setMyCommands %d", len(calls["answerCallbackQuery"]), len(calls["setMyCommands"]))
	}
}

func TestParseCommand(t *testing.T) {
	for in, want := range map[string]string{"/Estado": "estado", "/progreso@MiBot algo": "progreso", "hola": ""} {
		if got, _ := parseCommand(in); got != want {
			t.Errorf("%q → %q, esperado %q", in, got, want)
		}
	}
}
