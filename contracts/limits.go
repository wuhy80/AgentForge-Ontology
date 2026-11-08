package contracts

// Text limits apply to decoded UTF-8 bytes. The request limit applies to the
// entire JSON body on the wire, including escaping and all other fields.
const (
	MaxTextBytes              = 65535
	MaxLongTextBytes          = 4 << 20
	MaxDataRequestBytes       = 8 << 20
	MaxDataResponseBytes      = 8 << 20
	MaxDataConcurrentRequests = 1
	MaxDataQueuedRequests     = 4
	MaxDataQueueWaitMS        = 250
)
