package serve

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"darek/tools/whatsapp"

	"github.com/stretchr/testify/require"
)

// fakeWA is a stub WhatsAppManager for handler tests.
type fakeWA struct {
	state         whatsapp.PairingState
	groups        []whatsapp.Group
	groupsErr     error
	refreshCalled bool
	toggleCalls   []struct {
		JID string
		On  bool
	}
	unpairCalled bool
}

func (f *fakeWA) PairingState() whatsapp.PairingState { return f.state }
func (f *fakeWA) Groups(ctx context.Context) ([]whatsapp.Group, error) {
	return f.groups, f.groupsErr
}
func (f *fakeWA) RefreshGroups(ctx context.Context) error {
	f.refreshCalled = true
	return nil
}
func (f *fakeWA) SetIngestEnabled(ctx context.Context, jid string, on bool) error {
	f.toggleCalls = append(f.toggleCalls, struct {
		JID string
		On  bool
	}{jid, on})
	for i := range f.groups {
		if f.groups[i].JID == jid {
			f.groups[i].IngestEnabled = on
		}
	}
	return nil
}
func (f *fakeWA) Unpair(ctx context.Context) error {
	f.unpairCalled = true
	return nil
}

func newTestServerWithWA(t *testing.T, wa *fakeWA) *Server {
	t.Helper()
	s, err := New(nil, nil, nil, newTestAuth(time.Hour), &OIDC{}, wa, nil, "")
	require.NoError(t, err)
	return s
}

// authedRequest issues a request with a valid session cookie.
// Mints the cookie via setSessionCookie so the handler-side cookie
// shape always matches.
func authedRequest(t *testing.T, s *Server, method, target, body string) *http.Response {
	t.Helper()

	// Mint a session cookie via the server itself.
	sessRec := httptest.NewRecorder()
	s.setSessionCookie(sessRec, "test-user")

	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for _, c := range sessRec.Result().Cookies() {
		req.AddCookie(c)
	}
	resp := httptest.NewRecorder()
	s.Handler().ServeHTTP(resp, req)
	return resp.Result()
}

func TestHandleWhatsApp_RendersQRWhenNotPaired(t *testing.T) {
	wa := &fakeWA{state: whatsapp.PairingState{Paired: false, QRCode: "abc"}}
	s := newTestServerWithWA(t, wa)

	resp := authedRequest(t, s, "GET", "/whatsapp", "")
	require.Equal(t, 200, resp.StatusCode)
	body := readBody(t, resp)
	require.Contains(t, body, `src="/whatsapp/qr.png"`)
	require.Contains(t, body, "Pair a device")
}

func TestHandleWhatsApp_RendersGroupsWhenPaired(t *testing.T) {
	now := time.Now()
	wa := &fakeWA{
		state: whatsapp.PairingState{Paired: true, Connected: true, DeviceName: "MyPhone"},
		groups: []whatsapp.Group{
			{JID: "g1@g.us", Name: "Family", IngestEnabled: true, MessageCount: 7, LastMessageAt: &now},
			{JID: "g2@g.us", Name: "Work", IngestEnabled: false},
		},
	}
	s := newTestServerWithWA(t, wa)

	resp := authedRequest(t, s, "GET", "/whatsapp", "")
	require.Equal(t, 200, resp.StatusCode)
	body := readBody(t, resp)
	require.Contains(t, body, "Family")
	require.Contains(t, body, "Work")
	require.Contains(t, body, "MyPhone")
	require.Contains(t, body, "7 stored")
}

func TestHandleWhatsApp_ToggleFlipsAndReturnsRow(t *testing.T) {
	wa := &fakeWA{
		state:  whatsapp.PairingState{Paired: true},
		groups: []whatsapp.Group{{JID: "g1@g.us", Name: "G1", IngestEnabled: false}},
	}
	s := newTestServerWithWA(t, wa)

	form := url.Values{"enabled": {"1"}}.Encode()
	resp := authedRequest(t, s, "POST", "/whatsapp/groups/g1@g.us/toggle", form)
	require.Equal(t, 200, resp.StatusCode)
	require.Len(t, wa.toggleCalls, 1)
	require.True(t, wa.toggleCalls[0].On)
	body := readBody(t, resp)
	require.Contains(t, body, "checked")
}

// hx-target must be a relative selector (e.g. "closest tr") rather than
// "#wa-row-<jid>", because JIDs contain "@" and "." — both illegal in a
// CSS id selector. With a #-selector, htmx throws SyntaxError when
// resolving the target and silently aborts the request, so clicking the
// checkbox does nothing in the browser. This test guards against the
// regression.
func TestWhatsApp_GroupRowTargetIsRelative(t *testing.T) {
	wa := &fakeWA{
		state:  whatsapp.PairingState{Paired: true},
		groups: []whatsapp.Group{{JID: "g1@g.us", Name: "G1"}},
	}
	s := newTestServerWithWA(t, wa)
	resp := authedRequest(t, s, "GET", "/whatsapp", "")
	body := readBody(t, resp)
	require.Contains(t, body, `hx-target="closest tr"`,
		"group-row checkbox must use a relative target; a #-selector containing the JID is invalid CSS")
	require.NotContains(t, body, `hx-target="#wa-row-`,
		"the legacy #-selector form contains @ and . which CSS cannot parse")
}

func TestHandleWhatsApp_UnauthRedirects(t *testing.T) {
	wa := &fakeWA{state: whatsapp.PairingState{Paired: true}}
	s := newTestServerWithWA(t, wa)

	req := httptest.NewRequest("GET", "/whatsapp", nil)
	resp := httptest.NewRecorder()
	s.Handler().ServeHTTP(resp, req)
	got := resp.Result().StatusCode
	require.True(t, got == 302 || got == 303, "auth middleware redirects unauth'd, got %d", got)
	require.Contains(t, resp.Header().Get("Location"), "/login")
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(b)
}
