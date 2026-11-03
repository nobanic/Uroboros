package server

import (
	"github.com/nobanic/UroborOS/traffic"
	"time"
)

// Lists in the wire contract allocate an empty result and preserve eager order.
func projectDTOs[Input, Output any](input []Input, convert func(Input) Output) []Output {
	output := make([]Output, 0, len(input))
	for _, value := range input {
		output = append(output, convert(value))
	}
	return output
}

// Selected asset references retain nil when every source row is absent.
func selectDTOs[Input, Output any](input []Input, convert func(Input) (Output, bool)) []Output {
	var output []Output
	for _, value := range input {
		if item, keep := convert(value); keep {
			output = append(output, item)
		}
	}
	return output
}

func dtoSourceTaskID(id int64) string {
	if id > 0 {
		return i64s(id)
	}
	return ""
}

func trafficExchangeDTO(e traffic.ExchangeMeta) TrafficExchangeDTO {
	return TrafficExchangeDTO{
		ID:          e.ID,
		TS:          rfc3339(time.Unix(e.TS, 0)),
		Host:        e.Host,
		Method:      e.Method,
		URL:         e.URL,
		Status:      e.Status,
		ContentType: e.ContentType,
		RespLen:     e.RespLen,
	}
}
