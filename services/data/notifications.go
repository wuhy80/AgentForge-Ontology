package data

import (
	"net/http"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

const commentReplyNoticeKind = "comment_reply"

func (h *handler) recordNotifications(w http.ResponseWriter, r *http.Request) {
	h.readReplyInbox(w, r, true)
}

func (h *handler) markRecordNotification(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("kind") != commentReplyNoticeKind {
		writeError(w, problem(http.StatusNotFound, "not_found", "通知不可访问"))
		return
	}
	r.SetPathValue("replyId", r.PathValue("noticeId"))
	h.commentReplies(w, r)
}

func recordNotice(item inboxReply) c.RecordNotice {
	return c.RecordNotice{ID: item.ID, Kind: commentReplyNoticeKind, SchemaID: item.SchemaID,
		RecordID: item.RecordID, At: item.At, Read: item.read}
}
