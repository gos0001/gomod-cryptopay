package sys_health

import (
	"net/http"

	"github.com/gin-gonic/gin"

	httpserver "github.com/gos0001/gomod-cryptopay/pkg/http_server"
)

type HTTPv1 struct {
	uc *Usecase
}

func NewHTTPv1(uc *Usecase) *HTTPv1 {
	return &HTTPv1{uc: uc}
}

// Handle answers 200 when the service is usable and 503 when it is not.
//
// The status code is what matters: a container healthcheck and a load balancer
// both read it and neither parses the body. Answering 200 with
// {"status":"unavailable"} would keep an unusable instance in rotation.
func (h *HTTPv1) Handle(c *gin.Context) {
	out, _ := h.uc.Execute(c.Request.Context(), Input{})

	if !out.Healthy() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"data": out})
		return
	}

	httpserver.OK(c, out)
}
