package server

import (
	"io"
	"net/http"
	"strconv"

	"github.com/nobanic/UroborOS/db"
)

type skillLibraryReply struct {
	status   int
	document any
	problem  string
}

func skillResponse(status int, field string, value any) *skillLibraryReply {
	return &skillLibraryReply{status: status, document: map[string]any{field: value}}
}
func skillProblem(status int, message string) *skillLibraryReply {
	return &skillLibraryReply{status: status, problem: message}
}
func skillFailure(err error) *skillLibraryReply { return skillProblem(500, err.Error()) }
func skillWritten(err error) *skillLibraryReply {
	if err != nil {
		return skillFailure(err)
	}
	return skillResponse(200, "ok", true)
}
func writeSkillReply(w http.ResponseWriter, reply *skillLibraryReply) {
	if reply.status >= 400 {
		writeErr(w, reply.status, reply.problem)
		return
	}
	writeJSON(w, reply.status, reply.document)
}

type skillExchange struct {
	library skillLibrary
	request *http.Request
}

func (s *Server) exchangeSkills(r *http.Request) skillExchange {
	library := skillLibrary{root: s.skillDir}
	if s.m != nil {
		library.ledger = s.m.pg
	}
	return skillExchange{library: library, request: r}
}
func (exchange skillExchange) folder() (skillFolder, *skillLibraryReply) {
	return exchange.library.folder(exchange.request.PathValue("name"))
}
func (exchange skillExchange) file() (skillFolder, skillEntry, *skillLibraryReply) {
	folder, failure := exchange.folder()
	if failure != nil {
		return folder, skillEntry{}, failure
	}
	entry, failure := folder.entry(exchange.request.PathValue("file"))
	return folder, entry, failure
}
func (exchange skillExchange) decode(body any) *skillLibraryReply {
	if err := decode(exchange.request, body); err != nil {
		return skillProblem(400, err.Error())
	}
	return nil
}

func (exchange skillExchange) create() *skillLibraryReply {
	var draft skillDraft
	if failure := exchange.decode(&draft); failure != nil {
		return failure
	}
	return exchange.library.create(draft)
}
func (exchange skillExchange) edit() *skillLibraryReply {
	folder, failure := exchange.folder()
	if failure != nil {
		return failure
	}
	var edit skillMetaEdit
	if failure := exchange.decode(&edit); failure != nil {
		return failure
	}
	return folder.editMetadata(edit)
}
func (exchange skillExchange) listFiles() *skillLibraryReply {
	folder, failure := exchange.folder()
	if failure != nil {
		return failure
	}
	return folder.files()
}
func (exchange skillExchange) read() *skillLibraryReply {
	_, entry, failure := exchange.file()
	if failure != nil {
		return failure
	}
	return entry.read()
}
func (exchange skillExchange) write() *skillLibraryReply {
	folder, entry, failure := exchange.file()
	if failure != nil {
		return failure
	}
	var document struct {
		Content string `json:"content"`
	}
	if failure := exchange.decode(&document); failure != nil {
		return failure
	}
	return folder.write(entry, document.Content)
}
func (exchange skillExchange) directory() *skillLibraryReply {
	folder, failure := exchange.folder()
	if failure != nil {
		return failure
	}
	var document struct {
		Path string `json:"path"`
	}
	if failure := exchange.decode(&document); failure != nil {
		return failure
	}
	entry, failure := folder.entry(document.Path)
	if failure != nil {
		return failure
	}
	return folder.createDirectory(entry)
}
func (exchange skillExchange) deletePath() *skillLibraryReply {
	_, entry, failure := exchange.file()
	if failure != nil {
		return failure
	}
	return entry.remove()
}
func (exchange skillExchange) deleteSkill(ledger *db.DB) *skillLibraryReply {
	folder, failure := exchange.folder()
	if failure != nil {
		return failure
	}
	return folder.remove(exchange.request.PathValue("name"), ledger)
}
func (exchange skillExchange) usage(ledger *db.DB) *skillLibraryReply {
	name := exchange.request.PathValue("name")
	if !validSkillName(name) {
		return skillProblem(400, "illegal skill name")
	}
	limit, _ := strconv.Atoi(exchange.request.URL.Query().Get("limit"))
	rows, err := ledger.RecentSkillCalls(name, limit)
	if err != nil {
		return skillFailure(err)
	}
	return skillResponse(200, "calls", rows)
}
func (exchange skillExchange) missing(ledger *db.DB) *skillLibraryReply {
	limit, _ := strconv.Atoi(exchange.request.URL.Query().Get("limit"))
	rows, err := ledger.MissingSkillStats(limit)
	if err != nil {
		return skillFailure(err)
	}
	return skillResponse(200, "missing", rows)
}
func (exchange skillExchange) upload(w http.ResponseWriter) *skillLibraryReply {
	r := exchange.request
	r.Body = http.MaxBytesReader(w, r.Body, maxSkillZipBytes)
	upload, header, err := r.FormFile("file")
	if err != nil {
		return skillProblem(400, "missing uploaded file (form field file) or exceeds the size limit")
	}
	defer upload.Close()
	compressed, err := io.ReadAll(upload)
	if err != nil {
		return skillProblem(400, err.Error())
	}
	archive, failure := inspectSkillPackage(compressed, header.Filename)
	if failure != nil {
		return failure
	}
	return archive.install(exchange.library, r.URL.Query().Get("overwrite") == "true")
}
