package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"darek/db"
	"darek/obs"

	// Registers the "pgx" database/sql driver, used when the whatsmeow
	// session store is backed by Postgres instead of SQLite.
	_ "github.com/jackc/pgx/v5/stdlib"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Options configures NewManager. Exactly one session store is chosen: if
// PostgresDSN is set the whatsmeow session is stored in Postgres; otherwise
// StorePath is used for a SQLite session file (created if missing). Pool is
// the existing darek wrapped Postgres pool. Logger may be nil.
type Options struct {
	StorePath   string
	PostgresDSN string
	Pool        *db.Pool
	Logger      waLog.Logger
}

// Manager owns the WhatsApp connection lifecycle. Methods are safe for
// concurrent use by HTTP handlers and the internal event goroutine.
type Manager struct {
	pool      *db.Pool
	store     *Store
	storePath string
	postgres  bool
	logger    waLog.Logger

	pair pairing

	// drop is signaled whenever the connection drops or the device is logged
	// out, so Run can react by re-pairing or reconnecting.
	drop chan struct{}

	mu        sync.Mutex
	client    *whatsmeow.Client
	container *sqlstore.Container
}

// NewManager loads (or creates) the whatsmeow session store. It does not
// connect — call Run to start the connection loop.
func NewManager(opts Options) (*Manager, error) {
	if opts.Pool == nil {
		return nil, errors.New("whatsapp.NewManager: Pool is required")
	}
	if opts.Logger == nil {
		opts.Logger = waLog.Stdout("whatsmeow", "INFO", true)
	}

	dbLog := waLog.Stdout("wadb", "WARN", true)

	var container *sqlstore.Container
	if opts.PostgresDSN != "" {
		var err error
		container, err = sqlstore.New(context.Background(), "pgx", opts.PostgresDSN, dbLog)
		if err != nil {
			return nil, fmt.Errorf("sqlstore (postgres): %w", err)
		}
	} else {
		if opts.StorePath == "" {
			opts.StorePath = filepath.Join(os.Getenv("HOME"), ".darek", "whatsapp", "store.db")
		}
		if err := os.MkdirAll(filepath.Dir(opts.StorePath), 0o700); err != nil {
			return nil, fmt.Errorf("whatsapp store dir: %w", err)
		}
		// modernc.org/sqlite uses ?_pragma=foreign_keys(on) syntax (not the
		// mattn/go-sqlite3 ?_foreign_keys=on shorthand). Whatsmeow's sqlstore
		// fails to start if foreign_keys is off — its migrations rely on cascades.
		var err error
		container, err = sqlstore.New(context.Background(), "sqlite3",
			fmt.Sprintf("file:%s?_pragma=foreign_keys(on)", opts.StorePath), dbLog)
		if err != nil {
			return nil, fmt.Errorf("sqlstore (sqlite): %w", err)
		}
	}

	return &Manager{
		pool:      opts.Pool,
		store:     NewStore(opts.Pool),
		storePath: opts.StorePath,
		postgres:  opts.PostgresDSN != "",
		logger:    opts.Logger,
		drop:      make(chan struct{}, 1),
		container: container,
	}, nil
}

// Run blocks until ctx is canceled. It owns the connection lifecycle: while
// unpaired (fresh install, explicit logout, or an external logout that deletes
// the stored device) it drives the QR pairing flow and keeps rotating a fresh
// QR on failures; once paired it stays connected and reconnects after drops.
func (m *Manager) Run(ctx context.Context) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		device, err := m.container.GetFirstDevice(ctx)
		if err != nil {
			return fmt.Errorf("get device: %w", err)
		}

		m.mu.Lock()
		// Rebuild the client whenever the device is gone (logged out) or not
		// yet paired. GetQRChannel needs a fresh, unconnected client each
		// attempt and the previous client's QR emitter disconnects it.
		if m.client == nil || m.client.Store.Deleted || m.client.Store.ID == nil {
			if m.client != nil {
				m.client.Disconnect()
			}
			m.client = whatsmeow.NewClient(device, m.logger)
			m.client.EnableAutoReconnect = false
			m.client.AddEventHandler(m.handleEvent)
		}
		cli := m.client
		m.mu.Unlock()

		// Not paired: drive the QR pairing flow until it succeeds.
		if cli.Store.ID == nil {
			if err := m.runPairing(ctx, cli); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				// Let the QR rotate rather than hot-looping on failures.
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(3 * time.Second):
				}
			}
			continue
		}

		m.pair.setPaired(deviceName(cli), phoneE164(cli))
		if !cli.IsConnected() {
			if err := cli.Connect(); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(5 * time.Second):
				}
				continue
			}
		}

		// Connected: stream events until a drop (reconnect) or cancellation.
		select {
		case <-m.drop:
		case <-ctx.Done():
			cli.Disconnect()
			return ctx.Err()
		}
	}
}

// pair drives the QR channel on an unpaired client. It returns nil once a
// phone has scanned the QR and the session is stored, and an error if pairing
// timed out or the client is outdated (caller retries with a fresh QR).
func (m *Manager) runPairing(ctx context.Context, cli *whatsmeow.Client) error {
	m.pair.reset()
	qrChan, err := cli.GetQRChannel(ctx)
	if err != nil {
		return fmt.Errorf("qr channel: %w", err)
	}
	if err := cli.Connect(); err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	for evt := range qrChan {
		switch evt.Event {
		case "code":
			m.pair.setQR(evt.Code)
		case "success":
			m.pair.setPaired(deviceName(cli), phoneE164(cli))
			return nil
		case "timeout", "err-client-outdated", "err-scanned-without-multidevice":
			return fmt.Errorf("pairing failed: %s", evt.Event)
		}
	}
	return errors.New("pairing qr channel closed")
}

// Close is safe to call after Run returns.
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.client != nil {
		m.client.Disconnect()
	}
	return nil
}

// PairingState returns a read-only snapshot.
func (m *Manager) PairingState() PairingState {
	return m.pair.snapshot()
}

// IsConnected reports whether the underlying client is currently connected.
func (m *Manager) IsConnected() bool {
	return m.pair.snapshot().Connected
}

// Groups returns the persisted view (joined groups + counts).
func (m *Manager) Groups(ctx context.Context) ([]Group, error) {
	return m.store.Groups(ctx)
}

// RefreshGroups asks whatsmeow for the live joined-groups list and upserts
// the rows. Existing ingest_enabled flags are preserved.
func (m *Manager) RefreshGroups(ctx context.Context) error {
	m.mu.Lock()
	cli := m.client
	m.mu.Unlock()
	if cli == nil {
		return errors.New("whatsapp: client not initialized")
	}
	groups, err := cli.GetJoinedGroups(ctx)
	if err != nil {
		return fmt.Errorf("joined groups: %w", err)
	}
	for _, g := range groups {
		if err := m.store.UpsertGroup(ctx, g.JID.String(), g.Name); err != nil {
			return fmt.Errorf("upsert group %s: %w", g.JID, err)
		}
	}
	return nil
}

// SetIngestEnabled flips a single group's flag.
func (m *Manager) SetIngestEnabled(ctx context.Context, jid string, on bool) error {
	return m.store.SetIngestEnabled(ctx, jid, on)
}

// Unpair logs out, disconnects, and deletes the stored session (the SQLite
// file, or the device row in Postgres). Postgres data (whatsapp_messages,
// whatsapp_groups) is preserved.
func (m *Manager) Unpair(ctx context.Context) error {
	m.mu.Lock()
	cli := m.client
	m.mu.Unlock()
	if cli != nil {
		_ = cli.Logout(ctx)
		cli.Disconnect()
	}
	m.pair.reset()

	if m.postgres {
		// Logout already deletes the device on success; if it failed (e.g.
		// offline) delete the stored device directly so the next Run starts a
		// fresh pairing. GetFirstDevice returns an unsaved device (ID nil)
		// when nothing is stored, so the guard skips the no-op case.
		device, err := m.container.GetFirstDevice(ctx)
		if err != nil {
			return fmt.Errorf("get device: %w", err)
		}
		if device != nil && device.ID != nil {
			if err := device.Delete(ctx); err != nil {
				return fmt.Errorf("delete device: %w", err)
			}
		}
		return nil
	}

	if err := os.Remove(m.storePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove store: %w", err)
	}
	return nil
}

// handleEvent is the sole whatsmeow event handler.
func (m *Manager) handleEvent(evt any) {
	switch e := evt.(type) {
	case *events.Connected:
		m.pair.setConnected(true)
	case *events.Disconnected, *events.StreamReplaced:
		m.pair.setConnected(false)
		m.signalDrop()
	case *events.LoggedOut:
		m.pair.reset()
		m.signalDrop()
	case *events.Message:
		if e.Info.Chat.Server != types.GroupServer {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		m.ingestMessage(ctx, e)
	}
}

// signalDrop wakes Run after a drop or logout, coalescing notifications.
func (m *Manager) signalDrop() {
	select {
	case m.drop <- struct{}{}:
	default:
	}
}

// ingestMessage runs the per-message pipeline.
func (m *Manager) ingestMessage(ctx context.Context, e *events.Message) {
	groupJID := e.Info.Chat.String()

	exists, enabled, err := m.store.IngestEnabled(ctx, groupJID)
	if err != nil {
		m.logger.Warnf("ingest enabled lookup failed: %v", err)
		return
	}
	if !exists {
		// Best-effort: register the group as known (disabled) so the user can
		// opt in via the UI without waiting for a Refresh.
		_ = m.store.UpsertGroup(ctx, groupJID, e.Info.PushName)
		return
	}
	if !enabled {
		return
	}

	kind, body := decodeMessage(e)

	senderName := e.Info.PushName
	if senderName == "" {
		senderName = e.Info.Sender.String()
	}

	outcome := "ok"
	if err := m.store.InsertMessage(ctx, Message{
		ID:         e.Info.ID,
		GroupJID:   groupJID,
		SenderJID:  e.Info.Sender.String(),
		SenderName: senderName,
		Kind:       kind,
		Body:       body,
		SentAt:     e.Info.Timestamp,
	}); err != nil {
		m.logger.Warnf("insert message: %v", err)
		outcome = "error"
	}
	if mInst, _ := obs.MetricsInstance(); mInst != nil {
		mInst.WhatsAppMessages.Add(ctx, 1, metric.WithAttributes(
			attribute.String("kind", kind),
			attribute.String("outcome", outcome),
		))
	}
}

// deviceName / phoneE164 best-effort extract human-readable identifiers.
func deviceName(cli *whatsmeow.Client) string {
	if cli == nil || cli.Store == nil || cli.Store.ID == nil {
		return ""
	}
	if cli.Store.PushName != "" {
		return cli.Store.PushName
	}
	return cli.Store.ID.User
}

func phoneE164(cli *whatsmeow.Client) string {
	if cli == nil || cli.Store == nil || cli.Store.ID == nil {
		return ""
	}
	return "+" + cli.Store.ID.User
}
