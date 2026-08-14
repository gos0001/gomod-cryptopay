package invoice_get

import (
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
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
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		// 404, not 400: a malformed id and an id that does not exist are the
		// same answer from the caller's side, and distinguishing them only
		// tells a prober that their format was right.
		httpserver.NotFound(c, "not found")
		return
	}

	in := Input{ID: id}
	if err := in.Validate(); err != nil {
		apierr.Write(c, h.logger, "get invoice", err)
		return
	}

	out, err := h.uc.Execute(c.Request.Context(), in)
	if err != nil {
		apierr.Write(c, h.logger, "get invoice", err)
		return
	}

	httpserver.OK(c, out)
}
