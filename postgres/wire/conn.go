package wire

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"

	"github.com/jackc/pgx/v5/pgproto3"

	"github.com/kaeawc/pgmem-go/exec"
	"github.com/kaeawc/pgmem-go/ir"
	"github.com/kaeawc/pgmem-go/postgres/parse"
	"github.com/kaeawc/pgmem-go/storage"
	"github.com/kaeawc/pgmem-go/types"
)

// conn is the per-connection state. Each accepted TCP connection gets
// its own conn and its own goroutine.
type conn struct {
	be   *pgproto3.Backend
	tc   net.Conn
	deps Deps

	// Prepared statements and portals. M2 keeps a single anonymous
	// statement and portal — pgx names them, but for the unparameter-
	// ized queries we recognize there is no observable difference.
	stmt   *prepared
	portal *portal

	// Conn-scoped transaction state. currentTxn is non-nil when the
	// client is inside an explicit BEGIN block; statements while it's
	// set reuse the same txn rather than creating per-statement ones.
	// txState is the byte we emit in ReadyForQuery: 'I' (idle), 'T' (in
	// transaction), 'E' (in failed transaction). pgx uses it to decide
	// whether to issue ROLLBACK on its own.
	currentTxn storage.Txn
	txState    byte
}

// txAction tags the transaction-control statements. They look like
// ordinary statements over the wire but bypass the parser/exec pipeline
// entirely — they only manipulate conn-scoped state.
type txAction int

const (
	txNone txAction = iota
	txBegin
	txCommit
	txRollback
	txSavepoint        // SAVEPOINT <name>
	txReleaseSavepoint // RELEASE [SAVEPOINT] <name>
	txRollbackTo       // ROLLBACK TO [SAVEPOINT] <name>
)

type prepared struct {
	plan       ir.Node
	tag        string
	paramOIDs  []uint32 // from Parse
	paramTypes []types.Type
	noop       bool     // true for SET and other ignored statements
	txAction   txAction // non-zero for BEGIN/COMMIT/ROLLBACK family
	txName     string   // savepoint identifier (only for the SAVEPOINT family)
}

type portal struct {
	stmt *prepared
	// resultFormats holds Bind's ResultFormatCodes verbatim. Length 0
	// means "all text", length 1 means "uniform across columns", and
	// length N means per-column. We only know the column count at
	// Describe/Execute time, so we postpone the lookup with formatFor.
	resultFormats []int16
	params        []exec.Param
}

func handleConn(ctx context.Context, c net.Conn, deps Deps) error {
	cn := &conn{be: pgproto3.NewBackend(c, c), tc: c, deps: deps, txState: 'I'}

	if err := cn.doStartup(); err != nil {
		return err
	}
	if err := cn.writeReady(); err != nil {
		return err
	}
	return cn.queryLoop(ctx)
}

// --- startup ---

func (c *conn) doStartup() error {
	for {
		msg, err := c.be.ReceiveStartupMessage()
		if err != nil {
			return fmt.Errorf("receive startup: %w", err)
		}
		switch msg.(type) {
		case *pgproto3.SSLRequest:
			if _, err := c.tc.Write([]byte{'N'}); err != nil {
				return fmt.Errorf("ssl refuse: %w", err)
			}
		case *pgproto3.GSSEncRequest:
			if _, err := c.tc.Write([]byte{'N'}); err != nil {
				return fmt.Errorf("gss refuse: %w", err)
			}
		case *pgproto3.CancelRequest:
			return nil
		case *pgproto3.StartupMessage:
			return c.sendStartupResponse()
		default:
			return fmt.Errorf("unexpected startup message %T", msg)
		}
	}
}

func (c *conn) sendStartupResponse() error {
	c.be.Send(&pgproto3.AuthenticationOk{})
	for k, v := range startupParams {
		c.be.Send(&pgproto3.ParameterStatus{Name: k, Value: v})
	}
	c.be.Send(&pgproto3.BackendKeyData{ProcessID: 1, SecretKey: []byte{0, 0, 0, 1}})
	return c.be.Flush()
}

var startupParams = map[string]string{
	"server_version":              "16.0 (pgmem-go)",
	"server_encoding":             "UTF8",
	"client_encoding":             "UTF8",
	"DateStyle":                   "ISO, MDY",
	"TimeZone":                    "UTC",
	"IntervalStyle":               "postgres",
	"integer_datetimes":           "on",
	"standard_conforming_strings": "on",
	"is_superuser":                "off",
	"session_authorization":       "pgmem",
	"application_name":            "",
}

// --- main loop ---

func (c *conn) queryLoop(ctx context.Context) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		msg, err := c.be.Receive()
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("receive: %w", err)
		}
		if err := c.dispatch(ctx, msg); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

func (c *conn) dispatch(ctx context.Context, msg pgproto3.FrontendMessage) error {
	switch m := msg.(type) {
	case *pgproto3.Query:
		return c.handleSimpleQuery(ctx, m.String)
	case *pgproto3.Parse:
		return c.handleParse(m)
	case *pgproto3.Bind:
		return c.handleBind(m)
	case *pgproto3.Describe:
		return c.handleDescribe(m)
	case *pgproto3.Execute:
		return c.handleExecute(ctx, m)
	case *pgproto3.Sync:
		return c.writeReady()
	case *pgproto3.Flush:
		return c.be.Flush()
	case *pgproto3.Terminate:
		return io.EOF
	case *pgproto3.Close:
		c.be.Send(&pgproto3.CloseComplete{})
		return c.be.Flush()
	default:
		return c.sendError("08P01", fmt.Sprintf("unsupported message %T", msg))
	}
}

// --- statement handling ---

// classify turns SQL into something we can run. Recognition order:
//  1. Transaction control (BEGIN / COMMIT / ROLLBACK) — handled at the
//     wire layer because they manipulate conn state, not table state.
//  2. SET and friends — silent no-ops so client startup doesn't blow up.
//  3. Anything else — handed to the SQL parser.
func (c *conn) classify(sql string) (*prepared, error) {
	if cmd, ok := classifyTx(sql); ok {
		return &prepared{txAction: cmd.action, tag: cmd.tag, txName: cmd.name}, nil
	}
	if isClientNoop(sql) {
		return &prepared{noop: true, tag: noopTag(sql)}, nil
	}
	plan, err := parse.Parse(sql)
	if err != nil {
		return nil, err
	}
	return &prepared{plan: plan, tag: defaultTag(plan)}, nil
}

func defaultTag(plan ir.Node) string {
	switch plan.(type) {
	case *ir.Project, *ir.Scan, *ir.Values, *ir.Filter, *ir.Sort, *ir.Limit:
		return "SELECT"
	case *ir.CreateTable:
		return "CREATE TABLE"
	case *ir.Insert:
		return "INSERT"
	default:
		return ""
	}
}

func (c *conn) handleSimpleQuery(ctx context.Context, sql string) error {
	stmt, err := c.classify(sql)
	if err != nil {
		if err := c.sendError("0A000", err.Error()); err != nil {
			return err
		}
		return c.writeReady()
	}
	if err := c.runStatement(ctx, stmt, nil /* all text */, true /* send RowDescription */, nil); err != nil {
		if err := c.sendErrorFor(err); err != nil {
			return err
		}
	}
	return c.writeReady()
}

// runStatement is the dispatch that branches on what `classify`
// produced: transaction control, ack-only no-op, or a real query.
func (c *conn) runStatement(ctx context.Context, stmt *prepared, formats []int16, sendRowDesc bool, params []exec.Param) error {
	switch {
	case stmt.txAction != txNone:
		return c.handleTxAction(ctx, stmt)
	case stmt.noop:
		c.be.Send(&pgproto3.CommandComplete{CommandTag: []byte(stmt.tag)})
		return nil
	}
	// "Current transaction is aborted" — once a statement inside an
	// explicit BEGIN block fails, subsequent statements until ROLLBACK
	// must error with 25P02. PG behavior; pgx relies on it.
	if c.txState == 'E' {
		return &exec.SQLError{Code: "25P02", Message: "current transaction is aborted, commands ignored until end of transaction block"}
	}
	return c.runQuery(ctx, stmt, formats, sendRowDesc, params)
}

func (c *conn) handleTxAction(ctx context.Context, stmt *prepared) error {
	switch stmt.txAction {
	case txBegin:
		return c.beginTx(ctx, stmt.tag)
	case txCommit:
		return c.commitTx(stmt.tag)
	case txRollback:
		return c.rollbackTx(stmt.tag)
	case txSavepoint:
		return c.savepointTx(stmt.tag, stmt.txName)
	case txReleaseSavepoint:
		return c.releaseSavepointTx(stmt.tag, stmt.txName)
	case txRollbackTo:
		return c.rollbackToSavepointTx(stmt.tag, stmt.txName)
	}
	return nil
}

// savepointTx names a sub-transaction. Outside an explicit BEGIN block
// this is an error (PG's 25P01: no_active_sql_transaction).
func (c *conn) savepointTx(tag, name string) error {
	if c.currentTxn == nil {
		return &exec.SQLError{Code: "25P01", Message: "SAVEPOINT can only be used in transaction blocks"}
	}
	if err := c.currentTxn.Savepoint(name); err != nil {
		return err
	}
	c.be.Send(&pgproto3.CommandComplete{CommandTag: []byte(tag)})
	return nil
}

func (c *conn) releaseSavepointTx(tag, name string) error {
	if c.currentTxn == nil {
		return &exec.SQLError{Code: "25P01", Message: "RELEASE SAVEPOINT can only be used in transaction blocks"}
	}
	if err := c.currentTxn.ReleaseSavepoint(name); err != nil {
		return mapSavepointErr(err)
	}
	c.be.Send(&pgproto3.CommandComplete{CommandTag: []byte(tag)})
	return nil
}

// rollbackToSavepointTx restores per-tx state to the named savepoint.
// PG semantics: a successful ROLLBACK TO clears the failed-tx state,
// so a poisoned block can recover by rolling back to a savepoint set
// before the failure.
func (c *conn) rollbackToSavepointTx(tag, name string) error {
	if c.currentTxn == nil {
		return &exec.SQLError{Code: "25P01", Message: "ROLLBACK TO SAVEPOINT can only be used in transaction blocks"}
	}
	if err := c.currentTxn.RollbackToSavepoint(name); err != nil {
		return mapSavepointErr(err)
	}
	c.txState = 'T'
	c.be.Send(&pgproto3.CommandComplete{CommandTag: []byte(tag)})
	return nil
}

// mapSavepointErr converts the storage layer's typed savepoint errors
// to the SQLSTATE pgx pattern-matches on.
func mapSavepointErr(err error) error {
	var spErr *storage.SavepointError
	if errors.As(err, &spErr) {
		return &exec.SQLError{Code: "3B001", Message: err.Error()}
	}
	return err
}

func (c *conn) beginTx(ctx context.Context, tag string) error {
	if c.currentTxn != nil {
		// Real PG emits a warning notice and continues; we just ack.
		c.be.Send(&pgproto3.CommandComplete{CommandTag: []byte(tag)})
		return nil
	}
	t, err := c.deps.Engine.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	c.currentTxn = t
	c.txState = 'T'
	c.be.Send(&pgproto3.CommandComplete{CommandTag: []byte(tag)})
	return nil
}

func (c *conn) commitTx(tag string) error {
	if c.currentTxn == nil {
		c.be.Send(&pgproto3.CommandComplete{CommandTag: []byte(tag)})
		return nil
	}
	// PG behavior: COMMIT after a failed statement acts like ROLLBACK.
	if c.txState == 'E' {
		_ = c.currentTxn.Rollback()
		c.currentTxn = nil
		c.txState = 'I'
		c.be.Send(&pgproto3.CommandComplete{CommandTag: []byte("ROLLBACK")})
		return nil
	}
	if err := c.currentTxn.Commit(); err != nil {
		c.currentTxn = nil
		c.txState = 'I'
		return err
	}
	c.currentTxn = nil
	c.txState = 'I'
	c.be.Send(&pgproto3.CommandComplete{CommandTag: []byte(tag)})
	return nil
}

func (c *conn) rollbackTx(tag string) error {
	if c.currentTxn != nil {
		_ = c.currentTxn.Rollback()
		c.currentTxn = nil
	}
	c.txState = 'I'
	c.be.Send(&pgproto3.CommandComplete{CommandTag: []byte(tag)})
	return nil
}

func (c *conn) handleParse(m *pgproto3.Parse) error {
	stmt, err := c.classify(m.Query)
	if err != nil {
		return c.sendError("0A000", err.Error())
	}
	declared := make([]types.Type, len(m.ParameterOIDs))
	for i, oid := range m.ParameterOIDs {
		if oid == 0 {
			continue
		}
		t, ok := types.ByOID(oid)
		if !ok {
			return c.sendError("42704", fmt.Sprintf("unsupported parameter OID %d", oid))
		}
		declared[i] = t
	}
	if stmt.plan != nil {
		stmt.paramTypes = exec.InferParamTypes(stmt.plan, c.deps.Schema, declared)
	} else {
		stmt.paramTypes = declared
	}
	stmt.paramOIDs = make([]uint32, len(stmt.paramTypes))
	for i, t := range stmt.paramTypes {
		if t != nil {
			stmt.paramOIDs[i] = t.OID()
		}
	}
	c.stmt = stmt
	c.be.Send(&pgproto3.ParseComplete{})
	return nil
}

func (c *conn) handleBind(m *pgproto3.Bind) error {
	if c.stmt == nil {
		return c.sendError("26000", "no prepared statement")
	}
	params, err := decodeParams(c.stmt.paramTypes, m.ParameterFormatCodes, m.Parameters)
	if err != nil {
		return c.sendError("22023", err.Error())
	}
	c.portal = &portal{
		stmt:          c.stmt,
		resultFormats: append([]int16(nil), m.ResultFormatCodes...),
		params:        params,
	}
	c.be.Send(&pgproto3.BindComplete{})
	return nil
}

// decodeParams turns Bind's wire bytes into typed Go values. Format
// codes can be empty (all text), length 1 (uniform), or per-parameter.
func decodeParams(declared []types.Type, formats []int16, raw [][]byte) ([]exec.Param, error) {
	out := make([]exec.Param, len(raw))
	for i, b := range raw {
		fmtCode := formatFor(formats, i)
		t := paramType(declared, i)
		if b == nil {
			out[i] = exec.Param{Type: t, Value: nil}
			continue
		}
		var (
			v   any
			err error
		)
		if fmtCode == 1 {
			v, err = t.DecodeBinary(b)
		} else {
			v, err = t.DecodeText(b)
		}
		if err != nil {
			return nil, fmt.Errorf("param %d: %w", i+1, err)
		}
		out[i] = exec.Param{Type: t, Value: v}
	}
	return out, nil
}

func formatFor(codes []int16, i int) int16 {
	switch len(codes) {
	case 0:
		return 0
	case 1:
		return codes[0]
	default:
		return codes[i]
	}
}

func paramType(declared []types.Type, i int) types.Type {
	if i < len(declared) && declared[i] != nil {
		return declared[i]
	}
	// pgx normally sends OIDs in Parse; if it didn't, default to text and
	// hope for the best. M5 widens this with type inference.
	return types.Text
}

func (c *conn) handleDescribe(m *pgproto3.Describe) error {
	var stmt *prepared
	switch m.ObjectType {
	case 'S':
		stmt = c.stmt
		if stmt != nil {
			c.be.Send(&pgproto3.ParameterDescription{ParameterOIDs: stmt.paramOIDs})
		} else {
			c.be.Send(&pgproto3.ParameterDescription{ParameterOIDs: nil})
		}
	case 'P':
		if c.portal != nil {
			stmt = c.portal.stmt
		}
	}
	if stmt == nil || stmt.plan == nil {
		c.be.Send(&pgproto3.NoData{})
		return nil
	}
	rd, err := c.rowDescriptionFor(stmt)
	if err != nil {
		return c.sendErrorFor(err)
	}
	if rd == nil {
		c.be.Send(&pgproto3.NoData{})
		return nil
	}
	c.be.Send(rd)
	return nil
}

func (c *conn) handleExecute(ctx context.Context, _ *pgproto3.Execute) error {
	p := c.portal
	if p == nil || p.stmt == nil {
		return c.sendError("26000", "no portal bound")
	}
	// In extended-query mode, an Execute failure must surface as
	// ErrorResponse on the wire (pgx pattern-matches on it). Sync, sent
	// by the client right after Execute, drives the ReadyForQuery that
	// closes out the failed exchange — so we don't return the error,
	// only its wire-level rendering.
	if err := c.runStatement(ctx, p.stmt, p.resultFormats, false, p.params); err != nil {
		return c.sendErrorFor(err)
	}
	return nil
}

// --- execution ---

// runQuery builds and drains the operator pipeline for a prepared
// statement. The simple-query path asks us to emit RowDescription up
// front; the extended path has already sent one in response to Describe.
//
// Transaction handling: if the connection is inside an explicit BEGIN
// block we reuse that transaction; otherwise we start an implicit one
// and commit on success / rollback on failure. The behaviour matches
// PG's auto-commit mode for stand-alone statements.
func (c *conn) runQuery(ctx context.Context, stmt *prepared, formats []int16, sendRowDesc bool, params []exec.Param) error {
	txn, ownsTxn, err := c.acquireTxn(ctx)
	if err != nil {
		return err
	}
	commit := false
	defer func() {
		if ownsTxn {
			if commit {
				_ = txn.Commit()
			} else {
				_ = txn.Rollback()
			}
		}
	}()

	op, err := exec.Build(stmt.plan, &exec.Env{
		Schema: c.deps.Schema,
		Engine: c.deps.Engine,
		Txn:    txn,
		Params: params,
	})
	if err != nil {
		c.markTxFailedIfInBlock()
		return err
	}
	defer op.Close()

	schema := op.OutputSchema()
	if sendRowDesc {
		if len(schema) == 0 {
			c.be.Send(&pgproto3.NoData{})
		} else {
			c.be.Send(rowDescription(schema, formats))
		}
	}

	count := 0
	for {
		row, err := op.Next(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			c.markTxFailedIfInBlock()
			return err
		}
		if len(schema) == 0 {
			// DDL/DML side-effect operators emit nil row before EOF; skip.
			continue
		}
		dr, err := encodeDataRow(row, schema, formats)
		if err != nil {
			c.markTxFailedIfInBlock()
			return err
		}
		c.be.Send(dr)
		count++
	}
	c.be.Send(&pgproto3.CommandComplete{CommandTag: []byte(commandTag(op, stmt.tag, count))})
	commit = true
	return nil
}

// markTxFailedIfInBlock flips the conn into the "transaction aborted"
// state when a statement fails inside an explicit BEGIN block. Outside
// such a block (auto-commit) failures don't poison anything; the
// implicit txn is rolled back and we stay 'I'.
func (c *conn) markTxFailedIfInBlock() {
	if c.currentTxn != nil {
		c.txState = 'E'
	}
}

// acquireTxn returns the txn the current statement should run against.
// Inside an explicit BEGIN block the conn-scoped txn is reused and
// ownsTxn is false; otherwise we begin a fresh implicit txn and the
// caller commits/rolls back per its outcome.
func (c *conn) acquireTxn(ctx context.Context) (txn storage.Txn, ownsTxn bool, err error) {
	if c.currentTxn != nil {
		return c.currentTxn, false, nil
	}
	t, err := c.deps.Engine.Begin(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("begin: %w", err)
	}
	return t, true, nil
}

// commandTag picks the right CommandComplete payload. Side-effect
// operators (CreateTable, Insert) supply their own; SELECT-ish plans
// get the row count appended.
func commandTag(op exec.Operator, defaultTag string, count int) string {
	if t, ok := exec.CommandTag(op); ok {
		return t
	}
	if defaultTag == "SELECT" {
		return fmt.Sprintf("SELECT %d", count)
	}
	return defaultTag
}

func (c *conn) rowDescriptionFor(stmt *prepared) (*pgproto3.RowDescription, error) {
	txn, err := c.deps.Engine.Begin(context.Background())
	if err != nil {
		return nil, err
	}
	defer func() { _ = txn.Rollback() }()
	// Build with empty params just to learn the output schema. Real
	// param values aren't bound yet at Describe('S') time.
	op, err := exec.Build(stmt.plan, &exec.Env{
		Schema: c.deps.Schema,
		Engine: c.deps.Engine,
		Txn:    txn,
		Params: dummyParams(stmt.paramTypes),
	})
	if err != nil {
		return nil, err
	}
	defer op.Close()
	schema := op.OutputSchema()
	if len(schema) == 0 {
		return nil, nil
	}
	var formats []int16
	if c.portal != nil {
		formats = c.portal.resultFormats
	}
	return rowDescription(schema, formats), nil
}

// dummyParams supplies zero values of the right type so resolveExpr can
// fill in ParamRef.T without exec actually evaluating anything.
func dummyParams(decl []types.Type) []exec.Param {
	out := make([]exec.Param, len(decl))
	for i, t := range decl {
		out[i] = exec.Param{Type: t}
	}
	return out
}

func rowDescription(cols []exec.Column, formats []int16) *pgproto3.RowDescription {
	fields := make([]pgproto3.FieldDescription, len(cols))
	for i, c := range cols {
		fields[i] = pgproto3.FieldDescription{
			Name:                 []byte(c.Name),
			TableOID:             0,
			TableAttributeNumber: 0,
			DataTypeOID:          c.Type.OID(),
			DataTypeSize:         c.Type.Size(),
			TypeModifier:         -1,
			Format:               formatFor(formats, i),
		}
	}
	return &pgproto3.RowDescription{Fields: fields}
}

func encodeDataRow(r exec.Row, cols []exec.Column, formats []int16) (*pgproto3.DataRow, error) {
	values := make([][]byte, len(cols))
	for i, col := range cols {
		if i >= len(r) || r[i] == nil {
			values[i] = nil
			continue
		}
		var (
			b   []byte
			err error
		)
		if formatFor(formats, i) == 1 {
			b, err = col.Type.EncodeBinary(r[i])
		} else {
			b, err = col.Type.EncodeText(r[i])
		}
		if err != nil {
			return nil, fmt.Errorf("encode column %d (%s): %w", i, col.Name, err)
		}
		values[i] = b
	}
	return &pgproto3.DataRow{Values: values}, nil
}

// --- shared helpers ---

// writeReady emits ReadyForQuery with the conn's current transaction
// status. pgx pattern-matches on this byte to decide whether to issue
// ROLLBACK / COMMIT on its own.
func (c *conn) writeReady() error {
	state := c.txState
	if state == 0 {
		state = 'I'
	}
	c.be.Send(&pgproto3.ReadyForQuery{TxStatus: state})
	return c.be.Flush()
}

func (c *conn) sendError(sqlstate, message string) error {
	c.be.Send(&pgproto3.ErrorResponse{
		Severity: "ERROR",
		Code:     sqlstate,
		Message:  message,
	})
	return c.be.Flush()
}

// sendErrorFor maps a Go error to the most specific ErrorResponse we
// can produce. Executor SQLErrors carry the SQLSTATE the standard
// defines for the failure; everything else falls back to XX000 with
// the raw message body.
func (c *conn) sendErrorFor(err error) error {
	var sqlErr *exec.SQLError
	if errors.As(err, &sqlErr) {
		return c.sendError(sqlErr.Code, sqlErr.Message)
	}
	return c.sendError("XX000", err.Error())
}
