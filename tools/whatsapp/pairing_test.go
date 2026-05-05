package whatsapp

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPairing_InitialState(t *testing.T) {
	var p pairing
	st := p.snapshot()
	require.False(t, st.Paired)
	require.False(t, st.Connected)
	require.Empty(t, st.QRCode)
}

func TestPairing_SetQR(t *testing.T) {
	var p pairing
	p.setQR("data-here")
	st := p.snapshot()
	require.False(t, st.Paired)
	require.Equal(t, "data-here", st.QRCode)
	require.False(t, st.QRRotatedAt.IsZero())
}

func TestPairing_SetPaired(t *testing.T) {
	var p pairing
	p.setQR("data-here")
	p.setPaired("Device", "+447700900000")
	st := p.snapshot()
	require.True(t, st.Paired)
	require.Equal(t, "Device", st.DeviceName)
	require.Equal(t, "+447700900000", st.PhoneE164)
	require.Empty(t, st.QRCode, "QR cleared once paired")
}

func TestPairing_Connect_Disconnect(t *testing.T) {
	var p pairing
	p.setPaired("Device", "+1")
	require.False(t, p.snapshot().Connected)

	p.setConnected(true)
	require.True(t, p.snapshot().Connected)

	p.setConnected(false)
	require.False(t, p.snapshot().Connected)
}

func TestPairing_Reset(t *testing.T) {
	var p pairing
	p.setPaired("Device", "+1")
	p.setConnected(true)

	p.reset()
	st := p.snapshot()
	require.False(t, st.Paired)
	require.False(t, st.Connected)
	require.Empty(t, st.QRCode)
	require.Empty(t, st.DeviceName)
	require.Empty(t, st.PhoneE164)
}

func TestPairing_SnapshotIsCopy(t *testing.T) {
	var p pairing
	p.setQR("first")
	st := p.snapshot()
	p.setQR("second")
	require.Equal(t, "first", st.QRCode, "snapshot must not reflect later mutations")
}
