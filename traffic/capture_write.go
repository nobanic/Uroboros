package traffic

import (
	"database/sql"
	"fmt"
	"log"
	"strings"
	"time"

	mproxy "github.com/lqqyt2423/go-mitmproxy/proxy"
	"github.com/nobanic/UroborOS/db"
)

// The caller owns wmu throughout preparation and commit. A nonempty identity
// escapes only after all index/body/reference/optional FTS writes commit.
type captureExchangeWrite struct {
	store                                        *Traffic
	flow                                         *mproxy.Flow
	id, host, method, template, url, contentType string
	timestamp                                    time.Time
	requestHead, responseHead                    string
	request, response                            storedBody
}

func (write *captureExchangeWrite) prepare(flow *mproxy.Flow) {
	write.flow = flow
	write.host = flow.Request.URL.Hostname()
	write.method = flow.Request.Method
	write.template = db.TemplatePath(flow.Request.URL.EscapedPath())
	write.timestamp = time.Now()
	write.id = flow.Id.String()
	if write.id == "" || write.id == "00000000-0000-0000-0000-000000000000" {
		sequence := write.store.seq.Add(1)
		write.id = fmt.Sprintf("%d-%04d", write.timestamp.Unix(), sequence%10000)
	}
	write.contentType = flow.Response.Header.Get("Content-Type")
	write.url = flow.Request.URL.String()
	write.requestHead = fmt.Sprintf("%s %s %s\n%s", write.method, flow.Request.URL.RequestURI(), flow.Request.Proto, requestHeaderLines(flow.Request))
	write.responseHead = fmt.Sprintf("HTTP %d\n%s", flow.Response.StatusCode, headerLines(flow.Response.Header))
	write.request = write.store.spill(flow.Request.Body, flow.Request.Header.Get("Content-Type"))
	write.response = write.store.spill(flow.Response.Body, write.contentType)
}

func (write *captureExchangeWrite) persist(flow *mproxy.Flow) string {
	write.prepare(flow)
	transaction, err := write.store.db.Begin()
	if err != nil {
		return write.failed("begin transaction", err)
	}
	defer transaction.Rollback()
	rowID, ok := write.metadata(transaction)
	if !ok {
		return ""
	}
	if !write.bodies(transaction) || !write.references(transaction) || !write.fullText(transaction, rowID) {
		return ""
	}
	if err := transaction.Commit(); err != nil {
		return write.failed("commit", err)
	}
	return write.id
}

func (write *captureExchangeWrite) failed(stage string, err error) string {
	log.Printf("[traffic] failed to record %s (%s): %v", write.url, stage, err)
	return ""
}

func (write *captureExchangeWrite) metadata(transaction *sql.Tx) (int64, bool) {
	result, err := transaction.Exec(`INSERT OR REPLACE INTO exchanges(id,ts,host,method,url_template,url,status,content_type,req_len,resp_len,path)
VALUES(?,?,?,?,?,?,?,?,?,?,'')`,
		write.id, write.timestamp.Unix(), write.host, write.method, write.template, write.url, write.flow.Response.StatusCode, write.contentType,
		len(write.flow.Request.Body), len(write.flow.Response.Body))
	if err != nil {
		write.failed("write index", err)
		return 0, false
	}
	rowID, err := result.LastInsertId()
	if err != nil {
		write.failed("get rowid", err)
		return 0, false
	}
	return rowID, true
}

func (write *captureExchangeWrite) bodies(transaction *sql.Tx) bool {
	_, err := transaction.Exec(`INSERT OR REPLACE INTO exchange_bodies(id,req_head,req_body,req_blob,resp_head,resp_body,resp_blob)
VALUES(?,?,?,?,?,?,?)`, write.id, write.requestHead, write.request.inline, nullIfEmpty(write.request.hash), write.responseHead, write.response.inline, nullIfEmpty(write.response.hash))
	if err != nil {
		write.failed("write body", err)
		return false
	}
	return true
}

func (write *captureExchangeWrite) references(transaction *sql.Tx) bool {
	for _, hash := range []string{write.request.hash, write.response.hash} {
		if hash == "" {
			continue
		}
		if _, err := transaction.Exec(`INSERT OR IGNORE INTO blob_refs(hash,exchange_id) VALUES(?,?)`, hash, write.id); err != nil {
			write.failed("register blob reference", err)
			return false
		}
	}
	return true
}

func (write *captureExchangeWrite) fullText(transaction *sql.Tx, rowID int64) bool {
	if !write.store.fts {
		return true
	}
	content := strings.Join([]string{write.url, write.requestHead, write.request.index, write.responseHead, write.response.index}, "\n")
	if _, err := transaction.Exec(`INSERT INTO ex_fts(rowid,content) VALUES(?,?)`, rowID, content); err != nil {
		write.failed("write full-text index", err)
		return false
	}
	return true
}
