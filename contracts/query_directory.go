package contracts

// QueryDirectoryRequest is accepted only by the service-authenticated Metadata
// bridge. Check is the original external caller's credential and request; Path
// selects only current schema/link or category listings needed by Data. It does
// not permit choosing an identity or substituting service privileges for a user.
type QueryDirectoryRequest struct {
	Check SessionCheck `json:"check"`
	Path  string       `json:"path"`
}
