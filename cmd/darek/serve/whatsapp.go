package serve

import (
	"net/http"

	"darek/tools/whatsapp"

	"github.com/skip2/go-qrcode"
)

type whatsAppPageVM struct {
	State  whatsapp.PairingState
	Groups []whatsAppGroupVM
}

type whatsAppGroupVM struct {
	JID              string
	Name             string
	IngestEnabled    bool
	MessageCount     int
	LastMessageAtRel string
}

func (s *Server) handleWhatsApp(w http.ResponseWriter, r *http.Request) {
	state := s.whatsApp.PairingState()
	vm := whatsAppPageVM{State: state}
	if state.Paired {
		groups, err := s.whatsApp.Groups(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		vm.Groups = make([]whatsAppGroupVM, 0, len(groups))
		for _, g := range groups {
			vm.Groups = append(vm.Groups, toWhatsAppGroupVM(g))
		}
	}
	if err := s.tmpl.ExecuteTemplate(w, "whatsapp.html", vm); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleWhatsAppQR(w http.ResponseWriter, r *http.Request) {
	state := s.whatsApp.PairingState()
	if state.Paired {
		w.Header().Set("HX-Redirect", "/whatsapp")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if state.QRCode == "" {
		http.Error(w, "no QR yet", http.StatusServiceUnavailable)
		return
	}
	png, err := qrcode.Encode(state.QRCode, qrcode.Medium, 256)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(png)
}

func (s *Server) handleWhatsAppToggleGroup(w http.ResponseWriter, r *http.Request) {
	jid := r.PathValue("jid")
	if jid == "" {
		http.Error(w, "bad jid", http.StatusBadRequest)
		return
	}
	enabled := r.FormValue("enabled") != ""
	if err := s.whatsApp.SetIngestEnabled(r.Context(), jid, enabled); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	groups, err := s.whatsApp.Groups(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for _, g := range groups {
		if g.JID == jid {
			_ = s.tmpl.ExecuteTemplate(w, "_whatsapp_group_row.html", toWhatsAppGroupVM(g))
			return
		}
	}
	http.Error(w, "group not found after toggle", http.StatusNotFound)
}

func (s *Server) handleWhatsAppRefreshGroups(w http.ResponseWriter, r *http.Request) {
	if err := s.whatsApp.RefreshGroups(r.Context()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/whatsapp", http.StatusSeeOther)
}

func (s *Server) handleWhatsAppUnpair(w http.ResponseWriter, r *http.Request) {
	if err := s.whatsApp.Unpair(r.Context()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/whatsapp", http.StatusSeeOther)
}

func toWhatsAppGroupVM(g whatsapp.Group) whatsAppGroupVM {
	vm := whatsAppGroupVM{
		JID:           g.JID,
		Name:          g.Name,
		IngestEnabled: g.IngestEnabled,
		MessageCount:  g.MessageCount,
	}
	if g.LastMessageAt != nil {
		vm.LastMessageAtRel = relTime(*g.LastMessageAt)
	}
	return vm
}
