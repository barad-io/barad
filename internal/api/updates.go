package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/coder/websocket"

	"github.com/barad-io/barad/wire"
)

// Dirty is a live-update hint: these sessions changed (sync them), and these
// sessions' list entries changed (reload the list). The hint carries no data;
// the API is the only source of state.
type Dirty struct {
	Sessions  []string
	Summaries []string
}

// Updates connects to the update socket and sends each hint to hints until
// ctx ends or the connection fails, which it returns. The caller reconnects,
// then reloads what it shows, because hints sent while it was away are lost.
// connected is called once the socket is open.
func (c *Client) Updates(ctx context.Context, connected func(), hints chan<- Dirty) error {
	var ticket struct {
		ProtocolVersion     int       `json:"protocol_version"`
		Ticket              string    `json:"ticket"`
		ConnectionExpiresAt time.Time `json:"connection_expires_at"`
		WebSocketURL        string    `json:"websocket_url"`
	}
	if err := c.Do(ctx, http.MethodPost, "/account/update-ticket", map[string]any{}, &ticket); err != nil {
		return err
	}
	if ticket.ProtocolVersion != wire.UpdatesProtocolVersion {
		return fmt.Errorf("live updates speak protocol %d; this barad speaks %d — update barad", ticket.ProtocolVersion, wire.UpdatesProtocolVersion)
	}
	u, err := url.Parse(ticket.WebSocketURL)
	if err != nil || (u.Scheme != "wss" && u.Scheme != "ws") {
		return errors.New("live update endpoint was invalid")
	}
	q := u.Query()
	q.Set("ticket", ticket.Ticket)
	u.RawQuery = q.Encode()

	// The connection ends 30 seconds before the server would end it.
	ctx, cancel := context.WithDeadline(ctx, ticket.ConnectionExpiresAt.Add(-30*time.Second))
	defer cancel()
	dial, dialCancel := context.WithTimeout(ctx, 15*time.Second)
	conn, _, err := websocket.Dial(dial, u.String(), nil)
	dialCancel()
	if err != nil {
		return fmt.Errorf("live updates: %w", err)
	}
	defer conn.CloseNow()
	connected()

	// Every 15 seconds a ping; no answer within 5 means the connection is gone.
	pong := make(chan struct{}, 1)
	go func() {
		tick := time.NewTicker(15 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
			ping, _ := json.Marshal(map[string]any{"type": "ping", "version": wire.UpdatesProtocolVersion})
			if conn.Write(ctx, websocket.MessageText, ping) != nil {
				conn.CloseNow()
				return
			}
			select {
			case <-pong:
			case <-time.After(5 * time.Second):
				conn.CloseNow()
				return
			case <-ctx.Done():
				return
			}
		}
	}()

	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			if ctx.Err() != nil && errors.Is(context.Cause(ctx), context.DeadlineExceeded) {
				return errors.New("live update connection reached its end; reconnecting")
			}
			return fmt.Errorf("live updates: %w", err)
		}
		var message wire.DirtyMessage
		if json.Unmarshal(data, &message) != nil || message.Version != wire.UpdatesProtocolVersion {
			return errors.New("live updates sent an unreadable message")
		}
		switch message.Type {
		case "pong":
			select {
			case pong <- struct{}{}:
			default:
			}
		case "dirty":
			select {
			case hints <- Dirty{Sessions: message.Sessions, Summaries: message.Summaries}:
			case <-ctx.Done():
				return ctx.Err()
			}
		default:
			return fmt.Errorf("live updates sent an unknown message %q", message.Type)
		}
	}
}
