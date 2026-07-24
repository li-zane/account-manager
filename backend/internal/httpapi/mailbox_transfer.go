package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/li-zane/account-manager/backend/internal/domain"
	"github.com/li-zane/account-manager/backend/internal/service"
)

func (h *handler) mailboxDetail(w http.ResponseWriter, r *http.Request) {
	detail, err := h.deps.Details.Get(r.Context(), r.PathValue("mailboxID"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

type revealCredentialRequest struct {
	CredentialType domain.CredentialKind `json:"credential_type"`
}

func (h *handler) revealCredential(w http.ResponseWriter, r *http.Request) {
	if err := h.requireAdmin(r); err != nil {
		writeError(w, err)
		return
	}
	var request revealCredentialRequest
	if r.ContentLength != 0 {
		if err := decodeJSON(w, r, &request); err != nil {
			return
		}
	}
	revealed, err := h.deps.Details.Reveal(r.Context(), r.PathValue("mailboxID"), request.CredentialType)
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	writeJSON(w, http.StatusOK, revealed)
}

type mailboxFormatRequest struct {
	Name         string                        `json:"name"`
	Kind         domain.MailboxFormatKind      `json:"kind"`
	Direction    domain.MailboxFormatDirection `json:"direction"`
	Delimiter    string                        `json:"delimiter"`
	Fields       []domain.MailboxFormatField   `json:"fields"`
	Provider     *domain.ProviderKey           `json:"provider"`
	HasHeader    bool                          `json:"has_header"`
	Template     string                        `json:"template"`
	ParserConfig json.RawMessage               `json:"parser_config"`
	Enabled      *bool                         `json:"enabled"`
	Version      int64                         `json:"version"`
}

func (r mailboxFormatRequest) input() service.SaveMailboxFormatInput {
	return service.SaveMailboxFormatInput{
		Name: r.Name, Kind: r.Kind, Direction: r.Direction, Delimiter: r.Delimiter,
		Fields: r.Fields, Provider: r.Provider, HasHeader: r.HasHeader,
		Template: r.Template, ParserConfig: r.ParserConfig, Enabled: r.Enabled, Version: r.Version,
	}
}

func (h *handler) listMailboxFormats(w http.ResponseWriter, r *http.Request) {
	items, err := h.deps.Formats.List(r.Context(), parseListOptions(r))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

func (h *handler) createMailboxFormat(w http.ResponseWriter, r *http.Request) {
	var request mailboxFormatRequest
	if err := decodeJSON(w, r, &request); err != nil {
		return
	}
	format, err := h.deps.Formats.Create(r.Context(), request.input())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, format)
}

func (h *handler) getMailboxFormat(w http.ResponseWriter, r *http.Request) {
	format, err := h.deps.Formats.Get(r.Context(), r.PathValue("formatID"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, format)
}

func (h *handler) updateMailboxFormat(w http.ResponseWriter, r *http.Request) {
	var request mailboxFormatRequest
	if err := decodeJSON(w, r, &request); err != nil {
		return
	}
	format, err := h.deps.Formats.Update(r.Context(), r.PathValue("formatID"), request.input())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, format)
}

type mailboxImportHTTPRequest struct {
	FormatID         string                  `json:"format_id"`
	Data             string                  `json:"data"`
	ConflictStrategy domain.ConflictStrategy `json:"conflict_strategy"`
	DryRun           bool                    `json:"dry_run"`
}

func (r mailboxImportHTTPRequest) serviceRequest() service.MailboxImportRequest {
	return service.MailboxImportRequest{FormatID: r.FormatID, Data: r.Data, ConflictStrategy: r.ConflictStrategy}
}

func (h *handler) previewMailboxImport(w http.ResponseWriter, r *http.Request) {
	var request mailboxImportHTTPRequest
	if err := decodeJSON(w, r, &request); err != nil {
		return
	}
	preview, err := h.deps.Transfers.Preview(r.Context(), request.serviceRequest())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, preview)
}

func (h *handler) importMailboxes(w http.ResponseWriter, r *http.Request) {
	var request mailboxImportHTTPRequest
	if err := decodeJSON(w, r, &request); err != nil {
		return
	}
	if request.DryRun {
		preview, err := h.deps.Transfers.Preview(r.Context(), request.serviceRequest())
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, preview)
		return
	}
	result, err := h.deps.Transfers.Import(r.Context(), request.serviceRequest())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *handler) previewMailboxExport(w http.ResponseWriter, r *http.Request) {
	request, authorized, ok := h.decodeExportRequest(w, r)
	if !ok {
		return
	}
	result, err := h.deps.Transfers.Export(r.Context(), request, authorized)
	if err != nil {
		writeError(w, err)
		return
	}
	if result.SensitiveIncluded {
		w.Header().Set("Cache-Control", "no-store, max-age=0")
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *handler) exportMailboxes(w http.ResponseWriter, r *http.Request) {
	request, authorized, ok := h.decodeExportRequest(w, r)
	if !ok {
		return
	}
	result, err := h.deps.Transfers.Export(r.Context(), request, authorized)
	if err != nil {
		writeError(w, err)
		return
	}
	if result.SensitiveIncluded {
		w.Header().Set("Cache-Control", "no-store, max-age=0")
		w.Header().Set("Pragma", "no-cache")
	}
	contentType := "text/plain; charset=utf-8"
	if strings.HasSuffix(result.Filename, ".json") {
		contentType = "application/json; charset=utf-8"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, result.Filename))
	w.Header().Set("X-Mailbox-Count", fmt.Sprint(result.Count))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(result.Content))
}

func (h *handler) decodeExportRequest(w http.ResponseWriter, r *http.Request) (service.MailboxExportRequest, bool, bool) {
	var request service.MailboxExportRequest
	if err := decodeJSON(w, r, &request); err != nil {
		return service.MailboxExportRequest{}, false, false
	}
	authorized := false
	if request.IncludeSensitive {
		if err := h.requireAdmin(r); err != nil {
			writeError(w, err)
			return service.MailboxExportRequest{}, false, false
		}
		authorized = true
	}
	return request, authorized, true
}

func (h *handler) requireAdmin(r *http.Request) error {
	if h.admin == nil || !h.admin.Configured() {
		return domain.ErrUnauthorized
	}
	token := strings.TrimSpace(r.Header.Get("X-Admin-Token"))
	if authorization := r.Header.Get("Authorization"); strings.HasPrefix(authorization, "Bearer ") {
		token = strings.TrimSpace(strings.TrimPrefix(authorization, "Bearer "))
	}
	if !h.admin.Verify(token) {
		return domain.ErrUnauthorized
	}
	return nil
}
