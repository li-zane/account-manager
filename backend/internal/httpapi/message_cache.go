package httpapi

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/li-zane/account-manager/backend/internal/domain"
	"github.com/li-zane/account-manager/backend/internal/service"
)

func (h *handler) manageCachedMessages(w http.ResponseWriter, r *http.Request) {
	input, err := managedCacheInput(r)
	if err != nil {
		writeError(w, err)
		return
	}
	result, err := h.deps.MessageCache.QueryManaged(r.Context(), input)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *handler) deleteManagedCachedMessages(w http.ResponseWriter, r *http.Request) {
	if err := h.requireAdmin(r); err != nil {
		writeError(w, err)
		return
	}
	input, err := managedCacheInput(r)
	if err != nil {
		writeError(w, err)
		return
	}
	deleted, err := h.deps.MessageCache.DeleteManaged(r.Context(), input)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": deleted})
}

func (h *handler) exportCachedMessages(w http.ResponseWriter, r *http.Request) {
	input, err := managedCacheInput(r)
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="mail-cache.csv"`)
	_, _ = w.Write([]byte{0xef, 0xbb, 0xbf})
	writer := csv.NewWriter(w)
	_ = writer.Write([]string{"mailbox_id", "folder", "received_at", "from", "to", "cc", "subject", "text", "html", "retrieval_method"})
	for offset := 0; ; offset += 1000 {
		input.Limit, input.Offset = 1000, offset
		result, queryErr := h.deps.MessageCache.QueryManaged(r.Context(), input)
		if queryErr != nil {
			return
		}
		for _, item := range result.Messages {
			_ = writer.Write([]string{item.MailboxID, string(item.Folder), item.ReceivedAt.Format(time.RFC3339), item.From,
				strings.Join(item.To, "; "), strings.Join(item.Cc, "; "), item.Subject, item.Text, item.HTML, string(item.RetrievalMethod)})
		}
		writer.Flush()
		if len(result.Messages) < 1000 || int64(offset+len(result.Messages)) >= result.Count {
			break
		}
	}
}

func managedCacheInput(r *http.Request) (service.ManageCachedMessagesInput, error) {
	after, err := optionalCacheTime(r.URL.Query().Get("after"))
	if err != nil {
		return service.ManageCachedMessagesInput{}, err
	}
	before, err := optionalCacheTime(r.URL.Query().Get("before"))
	if err != nil {
		return service.ManageCachedMessagesInput{}, err
	}
	folder := domain.MessageFolder(strings.TrimSpace(r.URL.Query().Get("folder")))
	if folder != "" && folder != domain.MessageFolderInbox && folder != domain.MessageFolderJunk {
		return service.ManageCachedMessagesInput{}, fmt.Errorf("%w: invalid cache folder", domain.ErrInvalid)
	}
	return service.ManageCachedMessagesInput{
		MailboxID: strings.TrimSpace(r.URL.Query().Get("mailbox_id")), Folder: folder, After: after, Before: before,
		Search: strings.TrimSpace(r.URL.Query().Get("q")), Limit: cacheQueryInt(r, "limit"), Offset: cacheQueryInt(r, "offset"),
	}, nil
}

func optionalCacheTime(raw string) (*time.Time, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, fmt.Errorf("%w: cache time must be RFC3339", domain.ErrInvalid)
	}
	parsed = parsed.UTC()
	return &parsed, nil
}

func (h *handler) listMailboxCachedMessages(w http.ResponseWriter, r *http.Request) {
	h.cachedMessages(w, r, service.CachedMessagesInput{
		MailboxID: r.PathValue("mailboxID"), Folder: domain.MessageFolder(r.URL.Query().Get("folder")),
		RetrievalMethod: domain.RetrievalMethod(r.URL.Query().Get("method")),
		Limit:           cacheQueryInt(r, "limit"), Offset: cacheQueryInt(r, "offset"),
	}, false)
}

func (h *handler) markMailboxCachedMessageViewed(w http.ResponseWriter, r *http.Request) {
	if err := h.deps.MessageCache.MarkViewed(r.Context(), r.PathValue("mailboxID"), r.PathValue("messageID")); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"viewed": true})
}

func (h *handler) purgeMailboxCachedMessages(w http.ResponseWriter, r *http.Request) {
	if err := h.requireAdmin(r); err != nil {
		writeError(w, err)
		return
	}
	var before *time.Time
	if value := r.URL.Query().Get("before"); value != "" {
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			writeError(w, domain.ErrInvalid)
			return
		}
		before = &parsed
	}
	deleted, err := h.deps.MessageCache.Purge(r.Context(), service.PurgeCachedMessagesInput{
		MailboxID: r.PathValue("mailboxID"), Folder: domain.MessageFolder(r.URL.Query().Get("folder")),
		Before: before, Limit: cacheQueryInt(r, "limit"), ResetCursor: r.URL.Query().Get("reset_cursor") == "true",
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": deleted})
}

func (h *handler) restoreMailboxCachedMessageRange(w http.ResponseWriter, r *http.Request) {
	input, err := managedCacheInput(r)
	if err != nil {
		writeError(w, err)
		return
	}
	input.MailboxID = r.PathValue("mailboxID")
	count, err := h.deps.MessageCache.RestoreRange(r.Context(), input)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cached": count})
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
