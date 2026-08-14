package http_v1

import (
	"github.com/gin-gonic/gin"

	"github.com/gos0001/gomod-cryptopay/internal/middleware"
	"github.com/gos0001/gomod-cryptopay/internal/usecases/assets/asset_list"
	"github.com/gos0001/gomod-cryptopay/internal/usecases/invoices/invoice_cancel"
	"github.com/gos0001/gomod-cryptopay/internal/usecases/invoices/invoice_create"
	"github.com/gos0001/gomod-cryptopay/internal/usecases/invoices/invoice_get"
	"github.com/gos0001/gomod-cryptopay/internal/usecases/invoices/invoice_list"
	"github.com/gos0001/gomod-cryptopay/internal/usecases/payments/orphan_list"
	"github.com/gos0001/gomod-cryptopay/internal/usecases/sys/sys_health"
	// gostack:imports
)

// New builds the router and registers every JSON API route.
// Routing only — no business logic, no adapters, no domain types.
func New(
	mw *middleware.Middleware,
	health *sys_health.HTTPv1,
	invoiceCreate *invoice_create.HTTPv1,
	invoiceList *invoice_list.HTTPv1,
	invoiceGet *invoice_get.HTTPv1,
	invoiceCancel *invoice_cancel.HTTPv1,
	assetList *asset_list.HTTPv1,
	orphanList *orphan_list.HTTPv1,
	// gostack:params
) *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery())

	// Before the key guard, and that ordering is load-bearing: a browser sends no
	// X-Api-Key on a preflight, so an OPTIONS answered 401 would stop the real
	// request from ever being sent.
	r.Use(mw.CORS())

	// Outside the key: a container healthcheck and a load balancer have no
	// credentials, and the endpoint reveals nothing beyond whether the database
	// is reachable.
	r.GET("/healthz", health.Handle)

	// Everything under /api/v1 is behind the key. The guard is attached to the
	// group rather than to each route so a new endpoint is authenticated by
	// default — forgetting to add it is the failure mode worth designing out.
	v1 := r.Group("/api/v1", mw.APIKey())
	{
		v1.GET("/invoices", invoiceList.Handle)
		v1.GET("/invoices/:id", invoiceGet.Handle)
		v1.POST("/invoices/:id/cancel", invoiceCancel.Handle)

		v1.GET("/assets", assetList.Handle)

		// Reconciliation: transfers that arrived and matched no invoice.
		v1.GET("/orphans", orphanList.Handle)
	}

	// Creation is the one endpoint a customer's browser may reach, and only when
	// public_api.invoice_create says so. It sits outside the group above because
	// its guard is a different one; with the flag off, APIKeyOrPublic answers 401
	// exactly as APIKey would.
	//
	// Nothing else is opened: an anonymous caller cannot list invoices, read one
	// back, cancel one, or see orphan transfers.
	r.POST("/api/v1/invoices", mw.APIKeyOrPublic(), invoiceCreate.Handle)

	// gostack:routes

	return r
}
