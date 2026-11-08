package contracts

// QueryBinding describes a logically validated query and its public output
// fields. It is not an authorization token; execution checks current authority.
type QueryBinding struct {
	Query  QuerySpec  `json:"query"`
	Fields []Property `json:"fields"`
}
