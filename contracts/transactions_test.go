package contracts

import (
	"encoding/json"
	"testing"
)

func TestTransactionOptionalExtensionsPreserveOriginalReceiptIntent(t *testing.T) {
	// Existing durable request hashes were computed from this exact encoding.
	// Adding optional Link plans must not invalidate recoverable old requests.
	original := `{"idempotencyKey":"old-intent","operations":[{"schemaId":"schema","operation":"create","clientKey":"item","values":{"field":"9007199254740993.123"}}]}`
	var request WriteTransactionRequest
	if err := json.Unmarshal([]byte(original), &request); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(request)
	if err != nil || string(encoded) != original {
		t.Fatal("optional transaction extensions changed old receipt hashes")
	}
	request.Links = []TransactionLinkCreate{}
	request.LinkDeletes = []TransactionLinkDelete{}
	encoded, err = json.Marshal(request)
	if err != nil || string(encoded) != original {
		t.Fatal("empty optional Link plan changed old receipt hashes")
	}
}
