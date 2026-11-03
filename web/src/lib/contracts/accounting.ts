// Reported counters retain their source's optionality. Absence is never filled
// with zero, and older three-counter buckets do not acquire a fourth field.
export interface TokenPair {
  input_tokens: number;
  output_tokens: number;
}

export interface TokenReads extends TokenPair {
  cache_read_tokens: number;
}

export interface TokenCounters extends TokenReads {
  cache_write_tokens: number;
}

export interface SeverityCounts {
  critical: number;
  high: number;
  medium: number;
  low: number;
}
