package server

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/nobanic/UroborOS/traffic"
)

type trafficHTTP struct {
	server  *Server
	writer  http.ResponseWriter
	request *http.Request
}

func (s *Server) trafficRequest(w http.ResponseWriter, r *http.Request) trafficHTTP {
	return trafficHTTP{server: s, writer: w, request: r}
}

// Capture availability is resolved before parsing. List routes keep their own
// successful empty response; commands and body reads retain a disabled error.
func (operation trafficHTTP) store(empty any) *traffic.Traffic {
	store := operation.server.m.Traffic()
	if store == nil {
		if empty != nil {
			writeJSON(operation.writer, 200, empty)
		} else {
			writeErr(operation.writer, 404, "traffic disabled")
		}
	}
	return store
}

func (operation trafficHTTP) requiredQuery(key string, trim bool) (string, bool) {
	value := operation.request.URL.Query().Get(key)
	if trim {
		value = strings.TrimSpace(value)
	}
	if value == "" {
		writeErr(operation.writer, 400, "missing "+key)
		return "", false
	}
	return value, true
}

func (operation trafficHTTP) deleted(count int64, err error) {
	if err != nil {
		writeErr(operation.writer, 500, err.Error())
		return
	}
	writeJSON(operation.writer, 200, map[string]any{"deleted": count})
}

func (operation trafficHTTP) list() {
	store := operation.store(map[string]any{"enabled": false, "exchanges": []any{}})
	if store == nil {
		return
	}
	query := operation.request.URL.Query()
	page, size := atoiDefault(query.Get("page"), 0), atoiDefault(query.Get("size"), 100)
	rows, matched, _ := store.Page(traffic.PageQuery{
		Host:    query.Get("host"),
		Method:  query.Get("method"),
		Query:   query.Get("q"),
		Body:    query.Get("body"),
		Path:    query.Get("path"),
		Status:  query.Get("status"),
		RespMin: int64(atoiDefault(query.Get("resp_min"), -1)),
		RespMax: int64(atoiDefault(query.Get("resp_max"), -1)),
		Sort:    query.Get("sort"),
		Order:   query.Get("order"),
	}, page, size)
	count, _ := store.Count()
	writeJSON(operation.writer, 200, map[string]any{
		"enabled":   operation.server.m.TrafficEnabled(),
		"proxy":     operation.server.m.ProxyAddr(),
		"count":     count,
		"total":     matched,
		"page":      page,
		"size":      size,
		"exchanges": trafficDTOs(rows),
	})
}

func (operation trafficHTTP) hosts() {
	store := operation.store(map[string]any{"hosts": []any{}})
	if store == nil {
		return
	}
	hosts, err := store.Hosts()
	if err != nil {
		writeErr(operation.writer, 500, err.Error())
		return
	}
	writeJSON(operation.writer, 200, map[string]any{"hosts": hosts})
}

func (operation trafficHTTP) deleteHost() {
	store := operation.store(nil)
	if store == nil {
		return
	}
	host, ok := operation.requiredQuery("host", true)
	if !ok {
		return
	}
	operation.deleted(store.DeleteHost(host))
}

func (operation trafficHTTP) deleteHosts() {
	store := operation.store(nil)
	if store == nil {
		return
	}
	var body struct {
		Hosts []string `json:"hosts"`
	}
	if err := json.NewDecoder(operation.request.Body).Decode(&body); err != nil {
		writeErr(operation.writer, 400, "invalid body")
		return
	}
	hosts := make([]string, 0, len(body.Hosts))
	for _, value := range body.Hosts {
		if value = strings.TrimSpace(value); value != "" {
			hosts = append(hosts, value)
		}
	}
	if len(hosts) == 0 {
		writeErr(operation.writer, 400, "missing hosts")
		return
	}
	operation.deleted(store.DeleteHostsExact(hosts))
}

func (operation trafficHTTP) deleteAll() {
	store := operation.store(nil)
	if store == nil {
		return
	}
	deleted, reclaimed, err := store.DeleteAll()
	if err != nil {
		writeErr(operation.writer, 500, err.Error())
		return
	}
	writeJSON(operation.writer, 200, map[string]any{"deleted": deleted, "reclaimed": reclaimed})
}

func (operation trafficHTTP) exchange() {
	store := operation.store(nil)
	if store == nil {
		return
	}
	id, ok := operation.requiredQuery("id", false)
	if !ok {
		return
	}
	request, response, err := store.Get(id)
	if err != nil {
		writeErr(operation.writer, 404, err.Error())
		return
	}
	writeJSON(operation.writer, 200, map[string]any{"req": request, "resp": response})
}

func (operation trafficHTTP) blob() {
	store := operation.store(nil)
	if store == nil {
		return
	}
	hash, ok := operation.requiredQuery("hash", true)
	if !ok {
		return
	}
	file, size, err := store.Blob(hash)
	if err != nil {
		writeErr(operation.writer, 404, err.Error())
		return
	}
	defer file.Close()
	operation.writer.Header().Set("Content-Type", "application/octet-stream")
	operation.writer.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	operation.writer.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", hash+".bin"))
	if _, err := io.Copy(operation.writer, file); err != nil {
		log.Printf("[traffic] blob %s download interrupted: %v", hash, err)
	}
}
