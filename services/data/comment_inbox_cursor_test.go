package data

import (
	"strings"
	"testing"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func TestReplyInboxCursorBoundToRecipient(t *testing.T) {
	secret := strings.Repeat("s", 32)
	owner := c.Principal{TenantID: "tenant", Subject: "author"}
	position := replyInboxPosition{ID: c.NewID(), RecordID: c.NewID(), SchemaID: c.NewID(), At: time.Now().UTC().Truncate(time.Microsecond)}
	_, token, err := replyInboxCursor(secret, owner, "", position)
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := replyInboxCursor(secret, owner, token, replyInboxPosition{})
	if err != nil || got.ID != position.ID || got.RecordID != position.RecordID || got.SchemaID != position.SchemaID || !got.At.Equal(position.At) {
		t.Fatalf("cursor round trip failed: %v", err)
	}
	for _, other := range []c.Principal{{TenantID: owner.TenantID, Subject: "other"}, {TenantID: "other", Subject: owner.Subject}} {
		if _, _, err := replyInboxCursor(secret, other, token, replyInboxPosition{}); err == nil {
			t.Fatal("cursor accepted outside original session identity")
		}
	}
	if _, _, err := replyInboxCursor(secret, owner, token+"A", replyInboxPosition{}); err == nil {
		t.Fatal("tampered cursor accepted")
	}
}
