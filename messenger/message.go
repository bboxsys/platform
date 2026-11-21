package messenger

type Message struct {
	Method      string            `json:"method"`
	Path        string            `json:"path"`
	QueryParams map[string]string `json:"query_params"`
	Payload     interface{}       `json:"payload"`
}
