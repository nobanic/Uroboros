package server

// Binary job pagination keeps the original field names and JSON fold behavior.
type binaryJobPageInput = struct {
	JobID         string `json:"job_id"`
	Limit, Offset int
}
