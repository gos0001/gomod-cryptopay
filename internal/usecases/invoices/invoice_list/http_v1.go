package invoice_list

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/gos0001/gomod-cryptopay/internal/apierr"
	httpserver "github.com/gos0001/gomod-cryptopay/pkg/http_server"
)

type HTTPv1 struct {
	uc     *Usecase
	logger *zap.SugaredLogger
}

func NewHTTPv1(uc *Usecase, logger *zap.SugaredLogger) *HTTPv1 {
	return &HTTPv1{uc: uc, logger: logger}
}

func (h *HTTPv1) Handle(c *gin.Context) {
	limit, _ := strconv.Atoi(c.Query("limit"))
	assetID, _ := strconv.ParseInt(c.Query("asset_id"), 10, 64)

	in := Input{
		Status:     c.Query("status"),
		Network:    c.Query("network"),
		AssetID:    assetID,
		ExternalID: c.Query("external_id"),
		Cursor:     c.Query("cursor"),
		Limit:      int32(limit),
	}

	// Timestamps are RFC 3339. A malformed one is refused rather than dropped:
	// silently widening the window returns rows the caller did not ask for and
	// has no way to notice.
	var err error
	if in.CreatedFrom, err = parseTime(c.Query("created_from")); err != nil {
		apierr.BadRequest(c, "created_from must be an RFC 3339 timestamp")
		return
	}
	if in.CreatedTo, err = parseTime(c.Query("created_to")); err != nil {
		apierr.BadRequest(c, "created_to must be an RFC 3339 timestamp")
		return
	}

	if err := in.Validate(); err != nil {
		apierr.WriteInvalidInput(c, h.logger, "list invoices", err)
		return
	}

	out, err := h.uc.Execute(c.Request.Context(), in)
	if err != nil {
		apierr.Write(c, h.logger, "list invoices", err)
		return
	}

	httpserver.OK(c, out)
}

func parseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339, s)
}
