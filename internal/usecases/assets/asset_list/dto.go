package asset_list

import "github.com/gos0001/gomod-cryptopay/internal/view"

type Input struct{}

type Output struct {
	Assets []view.Asset `json:"assets"`
}
