// Package cryptopay is the Go client for the gomod-cryptopay service.
//
// The service accepts crypto payments watch-only: it holds no keys, watches one
// receiving address per network, and identifies an invoice by a unique amount
// rather than by an address. This package covers its HTTP API and the receiving
// end of its webhooks.
//
// # Creating an invoice
//
//	c := cryptopay.New("http://cryptopay:8080", os.Getenv("CRYPTOPAY_KEY"))
//
//	inv, created, err := c.CreateInvoice(ctx, cryptopay.CreateInvoiceRequest{
//	    Network:    cryptopay.NetworkTron,
//	    Symbol:     "USDT",
//	    Amount:     "10.50",
//	    ExternalID: order.ID,     // idempotency: a retry returns the same invoice
//	})
//	if err != nil {
//	    return err
//	}
//	_ = created
//
//	// Show these two, and never inv.Amount — a transfer of the requested figure
//	// falls outside the credit window and is filed as an orphan transfer.
//	render(inv.PayAddress, inv.PayAmount)
//
// # Receiving the result
//
//	mux.Handle("/hooks/cryptopay", cryptopay.WebhookHandler(secret,
//	    func(ctx context.Context, e cryptopay.Event) error {
//	        if e.Event != cryptopay.EventConfirmed {
//	            return nil        // detected is not paid: a reorg can undo it
//	        }
//	        return orders.MarkPaid(ctx, e.InvoiceID)   // must be idempotent
//	    }))
//
// The handler reads the raw body, checks the HMAC over "<timestamp>.<body>" and
// rejects timestamps outside DefaultTolerance, which is what stops a captured
// delivery being replayed later.
//
// # Rules worth knowing before writing any of this
//
//   - The API key belongs on a server. Every endpoint is authenticated, and the
//     key grants listing every invoice, cancelling any of them and reading orphan
//     transfers — a browser must talk to your backend, not to this service.
//   - Amounts are decimal strings. Never parse one into a float: 18 decimals do
//     not survive a float64, and a rounded amount misses the matching window.
//   - Only invoice.confirmed means paid.
//   - Underpayment is not partial payment. It becomes an orphan transfer for a
//     human to resolve.
//   - Deduplicate on Event.ID. Redelivery is normal, and this package
//     deliberately keeps no record of what it has seen.
//
// Full documentation: https://github.com/gos0001/gomod-cryptopay
package cryptopay
