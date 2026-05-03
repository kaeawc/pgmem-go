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
	// ized queries we recognize there is no observable difference. M3
	// turns these into real maps when transactions arrive.
	stmt   *prepared
	portal *portal
}

type prepared struct {
	plan       ir.Node
	tag        string
	paramOIDs  []uint32 // from Parse
	paramTypes []types.Type
	noop       bool // true for SET and other ignored statements
}

type portal struct {
	stmt         *prepared
	resultFormat int16
	params       []exec.Param
}

func handleConn(ctx context.Context, c net.Conn, deps Deps) error {
	cn := &conn{be: pgproto3.NewBackend(c, c), tc: c, deps: deps}

	if err := cn.doStartup(); err != nil {
		return err
	}
	if err := cn.writeReady('I'); err != nil {
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
		return c.writeReady('I')
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

// classify turns SQL into something we can run. Statements pgx issues
// for connection setup that we don't model (SET, BEGIN, COMMIT) become
// recognized no-ops so client startup doesn't blow up.
func (c *conn) classify(sql string) (*prepared, error) {
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
		return c.writeReady('I')
	}
	if stmt.noop {
		c.be.Send(&pgproto3.CommandComplete{CommandTag: []byte(stmt.tag)})
		return c.writeReady('I')
	}
	if err := c.runQuery(ctx, stmt, 0 /* text */, true /* send RowDescription */, nil); err != nil {
		if err := c.sendError("XX000", err.Error()); err != nil {
			return err
		}
	}
	return c.writeReady('I')
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
		stmt:         c.stmt,
		resultFormat: pickResultFormat(m.ResultFormatCodes),
		params:       params,
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
		t, err := paramType(declared, i)
		if err != nil {
			return nil, err
		}
		if b == nil {
			out[i] = exec.Param{Type: t, Value: nil}
			continue
		}
		var v any
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

func paramType(declared []types.Type, i int) (types.Type, error) {
	if i < len(declared) && declared[i] != nil {
		return declared[i], nil
	}
	// pgx normally sends OIDs in Parse; if it didn't, default to text and
	// hope for the best. M5 widens this with type inference.
	return types.Text, nil
}

func pickResultFormat(codes []int16) int16 {
	if len(codes) == 0 {
		return 0
	}
	return codes[0]
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
		return c.sendError("XX000", err.Error())
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
	if p.stmt.noop {
		c.be.Send(&pgproto3.CommandComplete{CommandTag: []byte(p.stmt.tag)})
		return nil
	}
	return c.runQuery(ctx, p.stmt, p.resultFormat, false, p.params)
}

// --- execution ---

// runQuery builds and drains the operator pipeline for a prepared
// statement. The simple-query path asks us to emit RowDescription up
// front; the extended path has already sent one in response to Describe.
func (c *conn) runQuery(ctx context.Context, stmt *prepared, format int16, sendRowDesc bool, params []exec.Param) error {
	txn, err := c.deps.Engine.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer txn.Rollback()

	op, err := exec.Build(stmt.plan, &exec.Env{
		Schema: c.deps.Schema,
		Engine: c.deps.Engine,
		Txn:    txn,
		Params: params,
	})
	if err != nil {
		return err
	}
	defer op.Close()

	schema := op.OutputSchema()
	if sendRowDesc {
		if len(schema) == 0 {
			c.be.Send(&pgproto3.NoData{})
		} else {
			c.be.Send(rowDescription(schema, format))
		}
	}

	count := 0
	for {
		row, err := op.Next(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if len(schema) == 0 {
			// DDL/DML side-effect operators emit nil row before EOF; skip.
			continue
		}
		dr, err := encodeDataRow(row, schema, format)
		if err != nil {
			return err
		}
		c.be.Send(dr)
		count++
	}
	c.be.Send(&pgproto3.CommandComplete{CommandTag: []byte(commandTag(op, stmt.tag, count))})
	return nil
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
	defer txn.Rollback()
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
	format := int16(0)
	if c.portal != nil {
		format = c.portal.resultFormat
	}
	return rowDescription(schema, format), nil
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

func rowDescription(cols []exec.Column, format int16) *pgproto3.RowDescription {
	fields := make([]pgproto3.FieldDescription, len(cols))
	for i, c := range cols {
		fields[i] = pgproto3.FieldDescription{
			Name:                 []byte(c.Name),
			TableOID:             0,
			TableAttributeNumber: 0,
			DataTypeOID:          c.Type.OID(),
			DataTypeSize:         c.Type.Size(),
			TypeModifier:         -1,
			Format:               format,
		}
	}
	return &pgproto3.RowDescription{Fields: fields}
}

func encodeDataRow(r exec.Row, cols []exec.Column, format int16) (*pgproto3.DataRow, error) {
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
		if format == 1 {
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

func (c *conn) writeReady(txStatus byte) error {
	c.be.Send(&pgproto3.ReadyForQuery{TxStatus: txStatus})
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
