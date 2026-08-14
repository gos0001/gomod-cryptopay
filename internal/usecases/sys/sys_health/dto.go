package sys_health

const (
	StatusOK          = "ok"
	StatusUnavailable = "unavailable"
)

type Input struct{}

type Output struct {
	Status   string `json:"status"`
	Database string `json:"database"`
}

func (o Output) Healthy() bool { return o.Status == StatusOK }
