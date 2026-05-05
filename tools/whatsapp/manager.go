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

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Options configures NewManager. StorePath is the SQLite session file (created
// if missing). Pool is the existing darek wrapped Postgres pool. Logger may be nil.
type Options struct {
	StorePath string
	Pool      *db.Pool
	Logger    waLog.Logger
}

// Manager owns the WhatsApp connection lifecycle. Methods are safe for
// concurrent use by HTTP handlers and the internal event goroutine.
type Manager struct {
	pool      *db.Pool
	store     *Store
	storePath string
	logger    waLog.Logger

	pair pairing

	mu        sync.Mutex
	client    *whatsmeow.Client
	container *sqlstore.Container
}

// NewManager loads (or creates) the SQLite session store. It does not connect
// — call Run to start the connection loop.
func NewManager(opts Options) (*Manager, error) {
	if opts.Pool == nil {
		return nil, errors.New("whatsapp.NewManager: Pool is required")
	}
	if opts.StorePath == "" {
		opts.StorePath = filepath.Join(os.Getenv("HOME"), ".darek", "whatsapp", "store.db")
	}
	if err := os.MkdirAll(filepath.Dir(opts.StorePath), 0o700); err != nil {
		return nil, fmt.Errorf("whatsapp store dir: %w", err)
	}
	if opts.Logger == nil {
		opts.Logger = waLog.Stdout("whatsmeow", "INFO", true)
	}

	dbLog := waLog.Stdout("wadb", "WARN", true)
	// modernc.org/sqlite uses ?_pragma=foreign_keys(on) syntax (not the
	// mattn/go-sqlite3 ?_foreign_keys=on shorthand). Whatsmeow's sqlstore
	// fails to start if foreign_keys is off — its migrations rely on cascades.
	container, err := sqlstore.New(context.Background(), "sqlite3",
		fmt.Sprintf("file:%s?_pragma=foreign_keys(on)", opts.StorePath), dbLog)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: %w", err)
	}

	return &Manager{
		pool:      opts.Pool,
		store:     NewStore(opts.Pool),
		storePath: opts.StorePath,
		logger:    opts.Logger,
		container: container,
	}, nil
}

// Run blocks until ctx is canceled. It connects to whatsmeow, registers the
// event handler, and orchestrates the QR pairing flow on first run.
func (m *Manager) Run(ctx context.Context) error {
	device, err := m.container.GetFirstDevice(ctx)
	if err != nil {
		return fmt.Errorf("get device: %w", err)
	}

	m.mu.Lock()
	m.client = whatsmeow.NewClient(device, m.logger)
	m.client.AddEventHandler(m.handleEvent)
	m.mu.Unlock()

	// Already paired: connect and stream events.
	if m.client.Store.ID != nil {
		m.pair.setPaired(deviceName(m.client), phoneE164(m.client))
		if err := m.client.Connect(); err != nil {
			return fmt.Errorf("connect: %w", err)
		}
		<-ctx.Done()
		m.client.Disconnect()
		return ctx.Err()
	}

	// Not paired: drive the QR pairing flow.
	qrChan, err := m.client.GetQRChannel(ctx)
	if err != nil {
		return fmt.Errorf("qr channel: %w", err)
	}
	if err := m.client.Connect(); err != nil {
		return fmt.Errorf("connect: %w", err)
	}

	for evt := range qrChan {
		switch evt.Event {
		case "code":
			m.pair.setQR(evt.Code)
		case "success":
			m.pair.setPaired(deviceName(m.client), phoneE164(m.client))
		case "timeout", "err-client-outdated", "err-scanned-without-multidevice":
			m.client.Disconnect()
			return fmt.Errorf("pairing failed: %s", evt.Event)
		}
		select {
		case <-ctx.Done():
			m.client.Disconnect()
			return ctx.Err()
		default:
		}
	}

	<-ctx.Done()
	m.client.Disconnect()
	return ctx.Err()
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

// Unpair logs out, disconnects, and deletes the SQLite store. Postgres data
// (whatsapp_messages, whatsapp_groups) is preserved.
func (m *Manager) Unpair(ctx context.Context) error {
	m.mu.Lock()
	cli := m.client
	m.mu.Unlock()
	if cli != nil {
		_ = cli.Logout(ctx)
		cli.Disconnect()
	}
	m.pair.reset()
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
	case *events.Disconnected, *events.LoggedOut:
		m.pair.setConnected(false)
	case *events.Message:
		if e.Info.Chat.Server != types.GroupServer {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		m.ingestMessage(ctx, e)
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
