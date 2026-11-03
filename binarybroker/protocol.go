package binarybroker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"sync"
	"time"
)

const (
	ProtocolVersion              = 1
	maxProtocolRequestFrameSize  = 64 << 10
	maxProtocolResponseFrameSize = 16 << 20
	defaultIOTimeout             = 15 * time.Minute
)

const (
	OperationStage     = "stage"
	OperationCommit    = "commit"
	OperationBind      = "bind"
	OperationRelease   = "release"
	OperationDiscard   = "discard_receipt"
	OperationInventory = "inventory"
	OperationReconcile = "reconcile"
	OperationAnalyze   = "analyze"
)

type Request struct {
	Version       int    `json:"version"`
	ID            string `json:"id"`
	Operation     string `json:"operation"`
	ExpectedSize  int64  `json:"expected_size,omitempty"`
	ReceiptID     string `json:"receipt_id,omitempty"`
	ReferenceID   string `json:"reference_id,omitempty"`
	SourceRefID   string `json:"source_reference_id,omitempty"`
	DestinationID string `json:"destination_reference_id,omitempty"`
	JobID         string `json:"job_id,omitempty"`
	LeaseNonce    string `json:"lease_nonce,omitempty"`
	Fingerprint   string `json:"fingerprint,omitempty"`
}

type Response struct {
	Version   int              `json:"version"`
	ID        string           `json:"id"`
	OK        bool             `json:"ok"`
	ErrorCode string           `json:"error_code,omitempty"`
	Error     string           `json:"error,omitempty"`
	Receipt   *Receipt         `json:"receipt,omitempty"`
	Reference *Reference       `json:"reference,omitempty"`
	Inventory *Inventory       `json:"inventory,omitempty"`
	Reconcile *ReconcileReport `json:"reconcile,omitempty"`
	Analysis  *AnalysisResult  `json:"analysis,omitempty"`
}

type ProtocolServer struct {
	Store     *Store
	GCGrace   time.Duration
	IOTimeout time.Duration
	Workers   *WorkerCoordinator
}

// ServeOnce accepts exactly one bootstrap connection, closes the listener, and
// invokes afterAccept (normally unlinking the Unix socket) before serving any
// operation. No reconnect listener remains reachable by later agent children.
func (server ProtocolServer) ServeOnce(listener net.Listener, afterAccept func() error) error {
	return server.ServeOnceContext(context.Background(), listener, afterAccept)
}

// ServeOnceContext also closes the bootstrap listener or established
// connection when the host shuts down. This matters after signal.Notify has
// replaced the process's default termination behavior.
func (server ProtocolServer) ServeOnceContext(ctx context.Context, listener net.Listener, afterAccept func() error) error {
	if listener == nil || server.Store == nil {
		return errors.New("binary broker server is not configured")
	}
	acceptDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = listener.Close()
		case <-acceptDone:
		}
	}()
	connection, err := listener.Accept()
	close(acceptDone)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	if err := listener.Close(); err != nil {
		_ = connection.Close()
		return err
	}
	if afterAccept != nil {
		if err := afterAccept(); err != nil {
			_ = connection.Close()
			return err
		}
	}
	defer connection.Close()
	connectionDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = connection.Close()
		case <-connectionDone:
		}
	}()
	defer close(connectionDone)
	return server.ServeConnectionContext(ctx, connection)
}

func (server ProtocolServer) ServeConnection(connection net.Conn) error {
	return server.ServeConnectionContext(context.Background(), connection)
}

func (server ProtocolServer) ServeConnectionContext(ctx context.Context, connection net.Conn) error {
	if server.Store == nil || connection == nil {
		return errors.New("binary broker server is not configured")
	}
	stopCancellation := context.AfterFunc(ctx, func() { _ = connection.Close() })
	defer stopCancellation()
	timeout := server.IOTimeout
	if timeout <= 0 {
		timeout = defaultIOTimeout
	}
	grace := server.GCGrace
	if grace == 0 {
		grace = MinimumGCGrace
	}
	reader := bufio.NewReader(connection)
	for {
		_ = connection.SetReadDeadline(time.Now().Add(timeout))
		var request Request
		if err := readFrame(reader, &request, maxProtocolRequestFrameSize); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		response := Response{Version: ProtocolVersion, ID: request.ID, OK: true}
		if request.Version != ProtocolVersion || !validID(request.ID) {
			response.OK = false
			response.ErrorCode = "invalid_request"
			response.Error = "invalid protocol version or request id"
			if err := writeFrame(connection, response, maxProtocolResponseFrameSize); err != nil {
				return err
			}
			return errors.New("invalid broker protocol header")
		}
		var operationErr error
		switch request.Operation {
		case OperationStage:
			if request.ExpectedSize < 0 || request.ExpectedSize > MaxArtifactBytes {
				operationErr = ErrInvalidSize
				break
			}
			receipt, err := server.Store.Stage(io.LimitReader(reader, request.ExpectedSize), request.ExpectedSize)
			operationErr = err
			if err == nil {
				response.Receipt = &receipt
			}
		case OperationCommit:
			reference, err := server.Store.Commit(request.ReceiptID, request.ReferenceID)
			operationErr = err
			if err == nil {
				response.Reference = &reference
			}
		case OperationBind:
			reference, err := server.Store.Bind(request.SourceRefID, request.DestinationID)
			operationErr = err
			if err == nil {
				response.Reference = &reference
			}
		case OperationRelease:
			operationErr = server.Store.Release(request.ReferenceID)
		case OperationDiscard:
			operationErr = server.Store.DiscardReceipt(request.ReceiptID)
		case OperationInventory:
			inventory, err := server.Store.Inventory()
			operationErr = err
			if err == nil {
				response.Inventory = &inventory
			}
		case OperationReconcile:
			report, err := server.Store.Reconcile(time.Now().UTC(), grace)
			operationErr = err
			if err == nil {
				response.Reconcile = &report
			}
		case OperationAnalyze:
			if server.Workers == nil {
				operationErr = ErrWorkerUnavailable
				break
			}
			result, err := server.Workers.Analyze(ctx, AnalysisRequest{
				JobID: request.JobID, LeaseNonce: request.LeaseNonce, ReferenceID: request.ReferenceID,
				Fingerprint: request.Fingerprint,
			})
			operationErr = err
			if err == nil {
				response.Analysis = &result
			}
		default:
			operationErr = errors.New("unknown broker operation")
		}
		if operationErr != nil {
			response.OK = false
			response.ErrorCode, response.Error = publicProtocolError(operationErr)
		}
		_ = connection.SetWriteDeadline(time.Now().Add(timeout))
		if err := writeFrame(connection, response, maxProtocolResponseFrameSize); err != nil {
			return err
		}
	}
}

func publicProtocolError(err error) (string, string) {
	switch {
	case errors.Is(err, ErrInvalidID):
		return "invalid_id", "broker identifier is invalid"
	case errors.Is(err, ErrArtifactTooLarge):
		return "artifact_too_large", "artifact exceeds the broker size ceiling"
	case errors.Is(err, ErrInvalidSize):
		return "invalid_size", "artifact size does not match the request"
	case errors.Is(err, ErrReceiptNotFound):
		return "receipt_not_found", "broker receipt was not found"
	case errors.Is(err, ErrReferenceConflict):
		return "reference_conflict", "broker reference conflicts with existing state"
	case errors.Is(err, ErrCorruptStore):
		return "integrity_failure", "broker integrity verification failed"
	case errors.Is(err, ErrGraceTooShort):
		return "unsafe_retention", "broker retention grace is below the safety floor"
	case errors.Is(err, ErrWorkerUnavailable):
		return "worker_unavailable", "binary analysis worker is unavailable"
	case errors.Is(err, ErrWorkerProtocol):
		return "worker_protocol", "binary analysis worker failed closed"
	case os.IsNotExist(err):
		return "object_not_found", "broker object was not found"
	default:
		return "storage_failure", "broker storage operation failed"
	}
}

type Client struct {
	connection net.Conn
	reader     *bufio.Reader
	mu         sync.Mutex
	sequence   uint64
	broken     bool
}

// NewClient adopts an already-established bootstrap connection. It deliberately
// has no reconnect logic; loss of the descriptor fails closed until the host
// performs a new bootstrap.
func NewClient(connection net.Conn) (*Client, error) {
	if connection == nil {
		return nil, errors.New("nil binary broker connection")
	}
	return &Client{connection: connection, reader: bufio.NewReader(connection)}, nil
}

func (client *Client) Close() error {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.broken = true
	return client.connection.Close()
}

func (client *Client) Stage(ctx context.Context, reader io.Reader, expectedSize int64) (Receipt, error) {
	if reader == nil || expectedSize < 0 || expectedSize > MaxArtifactBytes {
		return Receipt{}, ErrInvalidSize
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	request := Request{Operation: OperationStage}
	request.ExpectedSize = expectedSize
	response, err := client.roundTripLocked(ctx, request, reader)
	if err != nil {
		return Receipt{}, err
	}
	if response.Receipt == nil {
		return Receipt{}, errors.New("broker response omitted receipt")
	}
	return *response.Receipt, nil
}

func (client *Client) Commit(ctx context.Context, receiptID, referenceID string) (Reference, error) {
	request := Request{Operation: OperationCommit, ReceiptID: receiptID, ReferenceID: referenceID}
	response, err := client.roundTrip(ctx, request)
	if err != nil {
		return Reference{}, err
	}
	if response.Reference == nil {
		return Reference{}, errors.New("broker response omitted reference")
	}
	return *response.Reference, nil
}

func (client *Client) Bind(ctx context.Context, sourceID, destinationID string) (Reference, error) {
	request := Request{Operation: OperationBind, SourceRefID: sourceID, DestinationID: destinationID}
	response, err := client.roundTrip(ctx, request)
	if err != nil {
		return Reference{}, err
	}
	if response.Reference == nil {
		return Reference{}, errors.New("broker response omitted reference")
	}
	return *response.Reference, nil
}

func (client *Client) Release(ctx context.Context, referenceID string) error {
	_, err := client.roundTrip(ctx, Request{Operation: OperationRelease, ReferenceID: referenceID})
	return err
}

func (client *Client) DiscardReceipt(ctx context.Context, receiptID string) error {
	_, err := client.roundTrip(ctx, Request{Operation: OperationDiscard, ReceiptID: receiptID})
	return err
}

func (client *Client) Inventory(ctx context.Context) (Inventory, error) {
	response, err := client.roundTrip(ctx, Request{Operation: OperationInventory})
	if err != nil {
		return Inventory{}, err
	}
	if response.Inventory == nil {
		return Inventory{}, errors.New("broker response omitted inventory")
	}
	return *response.Inventory, nil
}

func (client *Client) Reconcile(ctx context.Context) (ReconcileReport, error) {
	response, err := client.roundTrip(ctx, Request{Operation: OperationReconcile})
	if err != nil {
		return ReconcileReport{}, err
	}
	if response.Reconcile == nil {
		return ReconcileReport{}, errors.New("broker response omitted reconcile report")
	}
	return *response.Reconcile, nil
}

func (client *Client) Analyze(ctx context.Context, request AnalysisRequest) (AnalysisResult, error) {
	response, err := client.roundTrip(ctx, Request{Operation: OperationAnalyze, JobID: request.JobID,
		LeaseNonce: request.LeaseNonce, ReferenceID: request.ReferenceID, Fingerprint: request.Fingerprint})
	if err != nil {
		return AnalysisResult{}, err
	}
	if response.Analysis == nil {
		return AnalysisResult{}, errors.New("broker response omitted analysis result")
	}
	return *response.Analysis, nil
}

func (client *Client) roundTrip(ctx context.Context, request Request) (Response, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.roundTripLocked(ctx, request, nil)
}

func (client *Client) roundTripLocked(ctx context.Context, request Request, payload io.Reader) (Response, error) {
	if client.broken {
		return Response{}, errors.New("binary broker connection is closed")
	}
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	stopCancellation := context.AfterFunc(ctx, func() {
		// A canceled request can leave the stream between frames or inside a byte
		// payload, so wake the I/O and let the error path permanently close it.
		_ = client.connection.SetDeadline(time.Now())
	})
	defer stopCancellation()
	request.Version = ProtocolVersion
	request.ID = client.nextRequestID()
	if deadline, ok := ctx.Deadline(); ok {
		_ = client.connection.SetDeadline(deadline)
	} else {
		_ = client.connection.SetDeadline(time.Time{})
	}
	if err := writeFrame(client.connection, request, maxProtocolRequestFrameSize); err != nil {
		client.breakConnection()
		if ctx.Err() != nil {
			return Response{}, ctx.Err()
		}
		return Response{}, err
	}
	if payload != nil {
		written, err := io.CopyN(client.connection, payload, request.ExpectedSize)
		if err != nil || written != request.ExpectedSize {
			client.breakConnection()
			if ctx.Err() != nil {
				return Response{}, ctx.Err()
			}
			if err == nil {
				err = io.ErrUnexpectedEOF
			}
			return Response{}, err
		}
	}
	var response Response
	if err := readFrame(client.reader, &response, maxProtocolResponseFrameSize); err != nil {
		client.breakConnection()
		if ctx.Err() != nil {
			return Response{}, ctx.Err()
		}
		return Response{}, err
	}
	if response.Version != ProtocolVersion || response.ID != request.ID {
		client.breakConnection()
		return Response{}, errors.New("binary broker response identity mismatch")
	}
	if !response.OK {
		return Response{}, fmt.Errorf("binary broker %s: %s", response.ErrorCode, response.Error)
	}
	return response, nil
}

func (client *Client) nextRequestID() string {
	client.sequence++
	return "request-" + strconv.FormatUint(client.sequence, 10)
}

func (client *Client) breakConnection() {
	client.broken = true
	_ = client.connection.Close()
}

func writeFrame(writer io.Writer, value any, maximum int) error {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	// Projection hashes bind the exact canonical JSON emitted by the isolated
	// worker. Default HTML escaping would rewrite <, >, and & inside an
	// embedded json.RawMessage while forwarding the accepted result.
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return err
	}
	raw := bytes.TrimSuffix(encoded.Bytes(), []byte{'\n'})
	if maximum <= 0 || len(raw) == 0 || len(raw) > maximum {
		return errors.New("binary broker protocol frame is outside bounds")
	}
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(raw)))
	if err := writeAll(writer, size[:]); err != nil {
		return err
	}
	return writeAll(writer, raw)
}

func writeAll(writer io.Writer, raw []byte) error {
	for len(raw) > 0 {
		written, err := writer.Write(raw)
		if err != nil {
			return err
		}
		if written <= 0 {
			return io.ErrShortWrite
		}
		raw = raw[written:]
	}
	return nil
}

func readFrame(reader io.Reader, destination any, maximum uint32) error {
	var size [4]byte
	if _, err := io.ReadFull(reader, size[:]); err != nil {
		return err
	}
	length := binary.BigEndian.Uint32(size[:])
	if maximum == 0 || length == 0 || length > maximum {
		return errors.New("binary broker protocol frame is outside bounds")
	}
	raw := make([]byte, int(length))
	if _, err := io.ReadFull(reader, raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytesReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("binary broker protocol frame has trailing JSON")
	}
	return nil
}

// bytesReader avoids retaining a bufio.Reader around a frame while still
// keeping strict JSON decoding local to this package.
func bytesReader(raw []byte) io.Reader { return &sliceReader{raw: raw} }

type sliceReader struct{ raw []byte }

func (reader *sliceReader) Read(destination []byte) (int, error) {
	if len(reader.raw) == 0 {
		return 0, io.EOF
	}
	n := copy(destination, reader.raw)
	reader.raw = reader.raw[n:]
	return n, nil
}
