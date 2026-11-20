package notifier

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"movistar-webdav-proxy/internal/logger"
)

// alertMarkup son los botones del bot, también bajo las alertas (vacío si no se escuchan comandos).
var alertMarkup atomic.Value // map[string]interface{}

// Button es un botón bajo un mensaje del bot: al pulsarlo se ejecuta el comando Data
// y se actualiza ese mismo mensaje.
type Button struct {
	Text, Data string
}

// Command es un comando que entiende el bot.
type Command struct {
	Name        string // sin «/»
	Description string
}

// PollCommands escucha los mensajes al bot (long polling de getUpdates) y responde
// con handle a los comandos. Solo atiende al chat configurado: lo demás se ignora y
// se anota. Mientras Telegram no esté configurado, espera.
func PollCommands(ctx context.Context, commands []Command, keyboard [][]Button, handle func(cmd, args string) string) {
	markup := inlineKeyboard(keyboard)
	alertMarkup.Store(markup)
	client := &http.Client{Timeout: 70 * time.Second}
	started := time.Now()
	offset := int64(0)
	menuFor := "" // token con el que ya se publicó el menú de comandos
	for ctx.Err() == nil {
		token, chat := Default.Credentials()
		if token == "" || chat == "" {
			sleepCtx(ctx, 30*time.Second)
			continue
		}
		if menuFor != token {
			if err := setMyCommands(client, token, commands); err == nil {
				menuFor = token
			}
		}
		updates, err := getUpdates(ctx, client, token, offset)
		if err != nil {
			if ctx.Err() == nil {
				logger.Debug("[Telegram] getUpdates: %s", sanitizeTransportError(err, token))
				sleepCtx(ctx, 15*time.Second)
			}
			continue
		}
		for _, u := range updates {
			offset = u.UpdateID + 1
			if q := u.Callback; q != nil {
				if q.Message == nil || strconv.FormatInt(q.Message.Chat.ID, 10) != chat {
					logger.Log("[Telegram] ⚠️ Botón ignorado de un chat no autorizado")
					continue
				}
				cmd, args := parseCommand("/" + q.Data)
				reply := handle(cmd, args)
				editMessage(client, token, q.Message.Chat.ID, q.Message.MessageID, reply, markup)
				answerCallback(client, token, q.ID)
				continue
			}
			m := u.Message
			if m == nil || m.Text == "" {
				continue
			}
			if strconv.FormatInt(m.Chat.ID, 10) != chat && "@"+m.Chat.Username != chat {
				logger.Log("[Telegram] ⚠️ Mensaje ignorado de un chat no autorizado (%d)", m.Chat.ID)
				continue
			}
			// Lo que se escribió con el proxy parado no se contesta al arrancar.
			if time.Unix(m.Date, 0).Before(started.Add(-time.Minute)) {
				continue
			}
			cmd, args := parseCommand(m.Text)
			if cmd == "" {
				cmd = "ayuda" // cualquier otro texto: el menú
			}
			if reply := handle(cmd, args); reply != "" {
				_ = Default.sendHTMLWith(reply, map[string]interface{}{"reply_markup": markup})
			}
		}
	}
}

// parseCommand separa «/progreso@MiBot algo» en («progreso», «algo»).
func parseCommand(text string) (string, string) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return "", ""
	}
	cmd, args, _ := strings.Cut(text[1:], " ")
	cmd, _, _ = strings.Cut(cmd, "@")
	return strings.ToLower(cmd), strings.TrimSpace(args)
}

type update struct {
	UpdateID int64 `json:"update_id"`
	Callback *struct {
		ID      string `json:"id"`
		Data    string `json:"data"`
		Message *struct {
			MessageID int64 `json:"message_id"`
			Chat      struct {
				ID int64 `json:"id"`
			} `json:"chat"`
		} `json:"message"`
	} `json:"callback_query"`
	Message *struct {
		Date int64  `json:"date"`
		Text string `json:"text"`
		Chat struct {
			ID       int64  `json:"id"`
			Username string `json:"username"`
		} `json:"chat"`
	} `json:"message"`
}

func getUpdates(ctx context.Context, client *http.Client, token string, offset int64) ([]update, error) {
	u := fmt.Sprintf("%s/bot%s/getUpdates?timeout=50&offset=%d&allowed_updates=%%5B%%22message%%22%%2C%%22callback_query%%22%%5D", apiBase, token, offset)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		OK          bool     `json:"ok"`
		Description string   `json:"description"`
		Result      []update `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if !out.OK {
		return nil, fmt.Errorf("Telegram: %s", out.Description)
	}
	return out.Result, nil
}

// setMyCommands publica el menú de comandos del bot (lo que sale al escribir «/»).
func setMyCommands(client *http.Client, token string, commands []Command) error {
	type cmd struct {
		Command     string `json:"command"`
		Description string `json:"description"`
	}
	var list []cmd
	for _, c := range commands {
		list = append(list, cmd{c.Name, c.Description})
	}
	b, _ := json.Marshal(map[string]interface{}{"commands": list})
	resp, err := client.Post(fmt.Sprintf("%s/bot%s/setMyCommands", apiBase, token), "application/json", bytes.NewReader(b))
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("setMyCommands: HTTP %d", resp.StatusCode)
	}
	return nil
}

func sleepCtx(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

func inlineKeyboard(rows [][]Button) map[string]interface{} {
	var kb [][]map[string]string
	for _, r := range rows {
		var row []map[string]string
		for _, b := range r {
			row = append(row, map[string]string{"text": b.Text, "callback_data": b.Data})
		}
		kb = append(kb, row)
	}
	return map[string]interface{}{"inline_keyboard": kb}
}

// editMessage sustituye el texto de un mensaje del bot (al pulsar un botón). Si no
// ha cambiado nada, Telegram responde «message is not modified»: no es un error.
func editMessage(client *http.Client, token string, chatID, messageID int64, text string, markup map[string]interface{}) {
	b, _ := json.Marshal(map[string]interface{}{"chat_id": chatID, "message_id": messageID, "text": text,
		"parse_mode": "HTML", "reply_markup": markup, "link_preview_options": map[string]bool{"is_disabled": true}})
	resp, err := client.Post(fmt.Sprintf("%s/bot%s/editMessageText", apiBase, token), "application/json", bytes.NewReader(b))
	if err != nil {
		logger.Debug("[Telegram] editMessageText: %s", sanitizeTransportError(err, token))
		return
	}
	resp.Body.Close()
}

// answerCallback quita el «cargando» del botón pulsado.
func answerCallback(client *http.Client, token, id string) {
	b, _ := json.Marshal(map[string]string{"callback_query_id": id})
	if resp, err := client.Post(fmt.Sprintf("%s/bot%s/answerCallbackQuery", apiBase, token), "application/json", bytes.NewReader(b)); err == nil {
		resp.Body.Close()
	}
}
