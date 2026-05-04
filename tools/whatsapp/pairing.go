package whatsapp

import (
	"sync"
	"time"
)

// PairingState is the read-only view UI handlers consume via
// Manager.PairingState(). All fields are zero-valued in the initial state
// (no session, no QR, not connected).
type PairingState struct {
	Paired      bool
	Connected   bool
	QRCode      string
	QRRotatedAt time.Time
	DeviceName  string
	PhoneE164   string
}

// pairing holds the mutable inner state. Methods are guarded by an internal
// mutex; the only external read path is snapshot(), which copies the struct.
type pairing struct {
	mu sync.RWMutex
	st PairingState
}

func (p *pairing) snapshot() PairingState {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.st
}

func (p *pairing) setQR(code string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.st.QRCode = code
	p.st.QRRotatedAt = time.Now()
}

func (p *pairing) setPaired(deviceName, phoneE164 string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.st.Paired = true
	p.st.DeviceName = deviceName
	p.st.PhoneE164 = phoneE164
	p.st.QRCode = ""
	p.st.QRRotatedAt = time.Time{}
}

func (p *pairing) setConnected(on bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.st.Connected = on
}

func (p *pairing) reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.st = PairingState{}
}
