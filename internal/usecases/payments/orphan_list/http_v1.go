package orphan_list

import (
	"strconv"

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

	in := Input{Limit: int32(limit)}
	if err := in.Validate(); err != nil {
		apierr.WriteInvalidInput(c, h.logger, "list orphan transfers", err)
		return
	}

	out, err := h.uc.Execute(c.Request.Context(), in)
	if err != nil {
		apierr.Write(c, h.logger, "list orphan transfers", err)
		return
	}

	httpserver.OK(c, out)
}
