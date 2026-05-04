// Package wire speaks the Postgres frontend/backend protocol via
// jackc/pgproto3. One goroutine per accepted connection.
//
// Error mapping to SQLSTATE lives here; pgx pattern-matches on those.
package wire

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/kaeawc/pgmem-go/catalog"
	"github.com/kaeawc/pgmem-go/storage"
)

// Deps is the set of subsystems a connection needs to answer queries.
// Threading these through the connection handler explicitly (rather
// than wiring them via package-level globals) is what makes per-test
// isolation work.
type Deps struct {
	Schema catalog.Schema
	Engine storage.Engine
	// Now is the clock the now() builtin reads. nil means "use the
	// real wall clock" — the wire layer doesn't fall back to time.Now
	// itself; that's the builtin's job.
	Now func() time.Time
}

// Serve accepts connections on l and spawns a goroutine per connection.
// It returns when ctx is cancelled or l.Accept returns a permanent error.
// Active connections are drained before returning.
func Serve(ctx context.Context, l net.Listener, deps Deps) error {
	var wg sync.WaitGroup
	defer wg.Wait()

	go func() {
		<-ctx.Done()
		_ = l.Close()
	}()

	for {
		c, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("accept: %w", err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer c.Close()
			// Connection-level errors (client disconnect, malformed frame)
			// are normal and don't propagate. We'll plumb a logger when
			// there's something worth logging.
			_ = handleConn(ctx, c, deps)
		}()
	}
}
