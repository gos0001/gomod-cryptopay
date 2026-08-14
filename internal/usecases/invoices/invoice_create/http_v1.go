package invoice_create

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/gos0001/gomod-cryptopay/internal/apierr"
	"github.com/gos0001/gomod-cryptopay/internal/middleware"
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
	var in Input
	if err := c.ShouldBindJSON(&in); err != nil {
		apierr.BadRequest(c, "request body is not valid JSON")
		return
	}

	if err := in.Validate(); err != nil {
		// Validate's messages name the offending field, so they are worth
		// showing rather than collapsing into "invalid request".
		apierr.BadRequest(c, apierr.Message(err))
		return
	}

	if middleware.IsPublic(c) {
		if err := in.RestrictToPublic(); err != nil {
			apierr.BadRequest(c, apierr.Message(err))
			return
		}
	}

	out, err := h.uc.Execute(c.Request.Context(), in)
	if err != nil {
		// The one case the generic table cannot phrase: the answer has to list
		// the contracts the caller must choose between.
		var ambiguous *AmbiguousAssetError
		if errors.As(err, &ambiguous) {
			httpserver.Conflict(c, ambiguous.Error())
			return
		}
		apierr.WriteInvalidInput(c, h.logger, "create invoice", err)
		return
	}

	// 201 for a new invoice, 200 for one returned again under the same
	// external_id — so a client can tell a successful retry from a first call.
	if out.Created {
		httpserver.Created(c, out)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": out})
}
