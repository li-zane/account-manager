package httpapi

import (
	"net/http"
	"strconv"

	"github.com/li-zane/account-manager/backend/internal/domain"
	"github.com/li-zane/account-manager/backend/internal/service"
)

func (h *handler) listMailboxCachedMessages(w http.ResponseWriter, r *http.Request) {
	h.cachedMessages(w, r, service.CachedMessagesInput{
		MailboxID: r.PathValue("mailboxID"), Folder: domain.MessageFolder(r.URL.Query().Get("folder")),
		RetrievalMethod: domain.RetrievalMethod(r.URL.Query().Get("method")),
		Limit:           cacheQueryInt(r, "limit"), Offset: cacheQueryInt(r, "offset"),
	}, false)
}

func (h *handler) listAliasCachedMessages(w http.ResponseWriter, r *http.Request) {
	h.cachedMessages(w, r, service.CachedMessagesInput{
		AliasID: r.PathValue("aliasID"), Folder: domain.MessageFolder(r.URL.Query().Get("folder")),
		RetrievalMethod: domain.RetrievalMethod(r.URL.Query().Get("method")),
		Limit:           cacheQueryInt(r, "limit"), Offset: cacheQueryInt(r, "offset"),
	}, false)
}

func (h *handler) syncMailboxMessages(w http.ResponseWriter, r *http.Request) {
	h.cachedMessages(w, r, service.CachedMessagesInput{
		MailboxID: r.PathValue("mailboxID"), Folder: domain.MessageFolder(r.URL.Query().Get("folder")),
		RetrievalMethod: domain.RetrievalMethod(r.URL.Query().Get("method")),
		Limit:           cacheQueryInt(r, "limit"), Offset: cacheQueryInt(r, "offset"),
	}, true)
}

func (h *handler) syncAliasMessages(w http.ResponseWriter, r *http.Request) {
	h.cachedMessages(w, r, service.CachedMessagesInput{
		AliasID: r.PathValue("aliasID"), Folder: domain.MessageFolder(r.URL.Query().Get("folder")),
		RetrievalMethod: domain.RetrievalMethod(r.URL.Query().Get("method")),
		Limit:           cacheQueryInt(r, "limit"), Offset: cacheQueryInt(r, "offset"),
	}, true)
}

func (h *handler) cachedMessages(w http.ResponseWriter, r *http.Request, input service.CachedMessagesInput, syncMessages bool) {
	var result service.CachedMessagesResult
	var err error
	if syncMessages {
		result, err = h.deps.MessageCache.Sync(r.Context(), input)
	} else {
		result, err = h.deps.MessageCache.List(r.Context(), input)
	}
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

type messageProbeSettingsRequest struct {
	Enabled         bool  `json:"enabled"`
	IntervalMinutes int   `json:"interval_minutes"`
	Version         int64 `json:"version"`
}

func (h *handler) getMessageProbeSettings(w http.ResponseWriter, r *http.Request) {
	if err := h.requireAdmin(r); err != nil {
		writeError(w, err)
		return
	}
	settings, err := h.deps.MessageProbeSettings.Get(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (h *handler) updateMessageProbeSettings(w http.ResponseWriter, r *http.Request) {
	if err := h.requireAdmin(r); err != nil {
		writeError(w, err)
		return
	}
	var request messageProbeSettingsRequest
	if err := decodeJSON(w, r, &request); err != nil {
		return
	}
	settings, err := h.deps.MessageProbeSettings.Update(r.Context(), service.UpdateMessageProbeSettingsInput{
		Enabled: request.Enabled, IntervalMinutes: request.IntervalMinutes, Version: request.Version,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func cacheQueryInt(r *http.Request, key string) int {
	value, _ := strconv.Atoi(r.URL.Query().Get(key))
	return value
}
