// Metadata common to a recorded exchange and an immutable evidence snapshot.
// Request/response hashes, provenance and evidence roles stay on those models.
export interface HTTPObservation {
  url: string;
  method: string;
  status: number;
  content_type: string;
  resp_len: number;
}
